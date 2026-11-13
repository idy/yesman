package secrets

// yesman writes and reads sops's YAML format with age keys itself, so that
// neither sops nor its cloud key services are needed, and the sops command
// can still edit the file with the same age key.
//
// Each value of the document is encrypted with AES-256-GCM under a random
// data key, with the keys of its path as additional data; the data key is
// encrypted to each age recipient. A MAC over every value, encrypted with
// the time it was last modified, catches changes made without the key.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"go.yaml.in/yaml/v3"
)

// sopsVersion is the sops version whose format yesman writes.
const sopsVersion = "3.13.3"

// unencryptedSuffix leaves a value plain when a key of its path ends in it,
// as sops does by default.
const unencryptedSuffix = "_unencrypted"

// nonceSize is the GCM nonce sops uses, longer than GCM's usual 12 bytes.
const nonceSize = 32

var encRE = regexp.MustCompile(`^ENC\[AES256_GCM,data:([^,]*),iv:([^,]+),tag:([^,]+),type:([a-z]+)\]$`)

// ErrNoKey says none of the identities decrypts the file's data key.
var ErrNoKey = errors.New("this key does not open the secrets file")

// sopsMeta is the sops key of a file, as yesman reads it.
type sopsMeta struct {
	Age []struct {
		Recipient string `yaml:"recipient"`
		Enc       string `yaml:"enc"`
	} `yaml:"age"`
	KeyGroups        []yaml.Node `yaml:"key_groups"`
	LastModified     string      `yaml:"lastmodified"`
	MAC              string      `yaml:"mac"`
	MACOnlyEncrypted bool        `yaml:"mac_only_encrypted"`
	Version          string      `yaml:"version"`
}

// leaf is a scalar value as sops encrypts it: its type, the bytes it
// encrypts and the bytes the MAC covers, which differ only for booleans.
type leaf struct {
	typ        string
	plain, mac []byte
}

func leafOf(n *yaml.Node) (leaf, bool, error) {
	switch n.ShortTag() {
	case "!!null":
		return leaf{}, false, nil // sops leaves nulls alone
	case "!!str":
		return leaf{"str", []byte(n.Value), []byte(n.Value)}, true, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err != nil {
			return leaf{}, false, err
		}
		s := []byte(strconv.FormatInt(i, 10))
		return leaf{"int", s, s}, true, nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return leaf{}, false, err
		}
		s := []byte(strconv.FormatFloat(f, 'f', -1, 64))
		return leaf{"float", s, s}, true, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return leaf{}, false, err
		}
		mac := "False"
		if b {
			mac = "True"
		}
		return leaf{"bool", []byte(strconv.FormatBool(b)), []byte(mac)}, true, nil
	}
	return leaf{}, false, fmt.Errorf("line %d: sops cannot encrypt a %s value", n.Line, n.ShortTag())
}

// setLeaf puts a decrypted value back as a scalar of its type.
func setLeaf(n *yaml.Node, typ string, plain []byte) error {
	n.Kind, n.Style, n.Value = yaml.ScalarNode, 0, string(plain)
	switch typ {
	case "str":
		n.Tag = "!!str"
		if strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		}
	case "int":
		n.Tag = "!!int"
	case "float":
		n.Tag = "!!float"
	case "bool":
		b, err := strconv.ParseBool(n.Value)
		if err != nil {
			return err
		}
		n.Tag, n.Value = "!!bool", strconv.FormatBool(b)
	default:
		return fmt.Errorf("sops value of unknown type %q", typ)
	}
	return nil
}

// walk calls fn on each scalar under n, in document order, with the keys
// of its path; sequence items share their sequence's path, as in sops.
func walk(n *yaml.Node, path []string, fn func(n *yaml.Node, path []string) error) error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := walk(c, path, fn); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if err := walk(n.Content[i+1], append(path[:len(path):len(path)], k.Value), fn); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		return fn(n, path)
	case yaml.AliasNode:
		return fmt.Errorf("line %d: aliases are not supported", n.Line)
	}
	return nil
}

func additionalData(path []string) string { return strings.Join(path, ":") + ":" }

func plainPath(path []string) bool {
	for _, k := range path {
		if strings.HasSuffix(k, unencryptedSuffix) {
			return true
		}
	}
	return false
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}

func encryptValue(gcm cipher.AEAD, typ string, plain []byte, aad string) (string, error) {
	iv := make([]byte, nonceSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	out := gcm.Seal(nil, iv, plain, []byte(aad))
	n := len(out) - gcm.Overhead()
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("ENC[AES256_GCM,data:%s,iv:%s,tag:%s,type:%s]", b64(out[:n]), b64(iv), b64(out[n:]), typ), nil
}

func decryptValue(gcm cipher.AEAD, value, aad string) (typ string, plain []byte, err error) {
	m := encRE.FindStringSubmatch(value)
	if m == nil {
		return "", nil, errors.New("not a sops value")
	}
	var parts [3][]byte
	for i, s := range m[1:4] {
		if parts[i], err = base64.StdEncoding.DecodeString(s); err != nil {
			return "", nil, err
		}
	}
	data, iv, tag := parts[0], parts[1], parts[2]
	if len(iv) != nonceSize {
		return "", nil, errors.New("a sops value has a nonce of the wrong size")
	}
	plain, err = gcm.Open(nil, iv, append(data, tag...), []byte(aad))
	if err != nil {
		return "", nil, errors.New("a value of the secrets file does not decrypt: it was changed, or moved to another key")
	}
	return m[4], plain, nil
}

// Encrypt encrypts a YAML document, a mapping, for the recipients, as sops
// would. doc is not changed.
func Encrypt(doc *yaml.Node, recipients []*age.X25519Recipient, now time.Time) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no age recipient to encrypt to")
	}
	root := mappingOf(doc)
	if root == nil {
		return nil, errors.New("sops encrypts a mapping")
	}
	tree := deepCopy(root)
	for i := 0; i+1 < len(tree.Content); i += 2 {
		if tree.Content[i].Value == "sops" {
			return nil, errors.New(`the document cannot have a key named "sops"`)
		}
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	mac := sha512.New()
	err = walk(tree, nil, func(n *yaml.Node, path []string) error {
		l, ok, err := leafOf(n)
		if err != nil || !ok {
			return err
		}
		mac.Write(l.mac)
		if plainPath(path) || len(l.plain) == 0 {
			return nil // sops writes empty values as they are
		}
		enc, err := encryptValue(gcm, l.typ, l.plain, additionalData(path))
		if err != nil {
			return err
		}
		n.Tag, n.Style, n.Value = "!!str", 0, enc
		n.HeadComment, n.LineComment, n.FootComment = "", "", ""
		return nil
	})
	if err != nil {
		return nil, err
	}

	modified := now.UTC().Truncate(time.Second).Format(time.RFC3339)
	macValue, err := encryptValue(gcm, "str", []byte(fmt.Sprintf("%X", mac.Sum(nil))), modified)
	if err != nil {
		return nil, err
	}
	var keys []*yaml.Node
	for _, r := range recipients {
		var buf bytes.Buffer
		aw := armor.NewWriter(&buf)
		w, err := age.Encrypt(aw, r)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(key); err != nil {
			return nil, err
		}
		if err := errors.Join(w.Close(), aw.Close()); err != nil {
			return nil, err
		}
		enc := buf.String()
		if !strings.HasSuffix(enc, "\n") {
			enc += "\n"
		}
		keys = append(keys, mapNode("recipient", strNode(r.String()), "enc", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.LiteralStyle, Value: enc}))
	}
	meta := mapNode(
		"age", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: keys},
		"lastmodified", strNode(modified),
		"mac", strNode(macValue),
		"unencrypted_suffix", strNode(unencryptedSuffix),
		"version", strNode(sopsVersion),
	)
	tree.Content = append(tree.Content, strNode("sops"), meta)

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(4)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{tree}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// readSops parses a sops file into its document, without the sops key, and
// that key.
func readSops(data []byte) (*yaml.Node, sopsMeta, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, sopsMeta{}, fmt.Errorf("the secrets file is not YAML: %w", err)
	}
	root := mappingOf(&doc)
	if root == nil {
		return nil, sopsMeta{}, errors.New("the secrets file is not a sops file")
	}
	var meta sopsMeta
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "sops" {
			continue
		}
		if err := root.Content[i+1].Decode(&meta); err != nil {
			return nil, sopsMeta{}, fmt.Errorf("the secrets file's sops key: %w", err)
		}
		root.Content = append(root.Content[:i:i], root.Content[i+2:]...)
		found = true
		break
	}
	if !found {
		return nil, sopsMeta{}, errors.New("the secrets file is not encrypted with sops")
	}
	if len(meta.KeyGroups) > 0 {
		return nil, sopsMeta{}, errors.New("the secrets file uses sops key groups, which yesman does not read")
	}
	if len(meta.Age) == 0 {
		return nil, sopsMeta{}, errors.New("the secrets file has no age key; yesman reads only files encrypted with age")
	}
	return root, meta, nil
}

// Recipients lists the age recipients a sops file is encrypted to.
func Recipients(data []byte) ([]string, error) {
	_, meta, err := readSops(data)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range meta.Age {
		out = append(out, a.Recipient)
	}
	return out, nil
}

// Decrypt decrypts a sops file with one of the identities and checks its
// MAC. It returns the document's mapping, without the sops key.
func Decrypt(data []byte, identities ...age.Identity) (*yaml.Node, error) {
	tree, meta, err := readSops(data)
	if err != nil {
		return nil, err
	}
	var key []byte
	for _, a := range meta.Age {
		r, err := age.Decrypt(armor.NewReader(strings.NewReader(a.Enc)), identities...)
		if err != nil {
			continue
		}
		if k, err := io.ReadAll(r); err == nil && len(k) == 32 {
			key = k
			break
		}
	}
	if key == nil {
		return nil, ErrNoKey
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	mac := sha512.New()
	err = walk(tree, nil, func(n *yaml.Node, path []string) error {
		n.HeadComment, n.LineComment, n.FootComment = "", "", ""
		if n.ShortTag() == "!!str" && strings.HasPrefix(n.Value, "ENC[") {
			typ, plain, err := decryptValue(gcm, n.Value, additionalData(path))
			if err != nil {
				return err
			}
			if err := setLeaf(n, typ, plain); err != nil {
				return err
			}
			l, _, err := leafOf(n)
			if err != nil {
				return err
			}
			mac.Write(l.mac)
			return nil
		}
		l, ok, err := leafOf(n)
		if err != nil || !ok {
			return err
		}
		if !meta.MACOnlyEncrypted {
			mac.Write(l.mac)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	modified, err := time.Parse(time.RFC3339, meta.LastModified)
	if err != nil {
		return nil, fmt.Errorf("the secrets file's lastmodified: %w", err)
	}
	_, want, err := decryptValue(gcm, meta.MAC, modified.Format(time.RFC3339))
	if err != nil {
		return nil, errors.New("the secrets file's MAC does not decrypt: it was changed without its key")
	}
	if !strings.EqualFold(string(want), fmt.Sprintf("%X", mac.Sum(nil))) {
		return nil, errors.New("the secrets file does not match its MAC: it was changed without its key")
	}
	clearComments(tree)
	return tree, nil
}

// mappingOf is the mapping at the top of a document, or nil.
func mappingOf(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func deepCopy(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = deepCopy(child)
	}
	return &c
}

func clearComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		clearComments(c)
	}
}

func strNode(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

// mapNode makes a mapping of key, value pairs.
func mapNode(pairs ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(pairs); i += 2 {
		m.Content = append(m.Content, strNode(pairs[i].(string)), pairs[i+1].(*yaml.Node))
	}
	return m
}
