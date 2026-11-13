// Package secrets keeps the environment variables yesman sets in every
// agent's commands: secrets, in a sops file encrypted with age to a key
// that the user's password protects, and variables, in a plain YAML file.
// Each is a name, a value and a description of what it is for, which
// agents can read; agents never read a secret's value.
package secrets

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Entry is an environment variable for agents' commands.
type Entry struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// Kind is what a file keeps: secrets or variables, its top key.
type Kind string

const (
	KindSecrets   Kind = "secrets"
	KindVariables Kind = "variables"
)

// fileVersion is the version of the format yesman writes.
const fileVersion = 1

// Error is a mistake in what the user wrote, in English with fmt verbs,
// for the app to translate.
type Error struct {
	Msg  string
	Args []any
}

func (e *Error) Error() string { return fmt.Sprintf(e.Msg, e.Args...) }

func errorf(msg string, args ...any) error { return &Error{Msg: msg, Args: args} }

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reserved are names the system or yesman sets, which an entry must not
// replace.
var reserved = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "OLDPWD": true, "SHELL": true, "USER": true,
	"LOGNAME": true, "TERM": true, "TMPDIR": true, "SHLVL": true, "IFS": true,
}

// CheckName says whether name may name an entry.
func CheckName(name string) error {
	switch {
	case name == "":
		return errorf("every entry needs a name")
	case len(name) > 128:
		return errorf("%s is longer than 128 characters", name[:32]+"…")
	case !nameRE.MatchString(name):
		return errorf("%q is not an environment variable name: use letters, digits and underscores, not starting with a digit", name)
	case reserved[strings.ToUpper(name)]:
		return errorf("%s is set by the system, so it cannot be set here", name)
	case strings.HasPrefix(strings.ToUpper(name), "YESMAN_"), strings.HasPrefix(strings.ToUpper(name), "PI_"):
		return errorf("%s is set by yesman or pi, so it cannot be set here", name)
	}
	return nil
}

// Check says whether entries can be kept: each well named, once, with a
// value an environment can hold.
func Check(entries []Entry) error {
	seen := map[string]bool{}
	for _, e := range entries {
		if err := CheckName(e.Name); err != nil {
			return err
		}
		if seen[e.Name] {
			return errorf("%s is there twice", e.Name)
		}
		seen[e.Name] = true
		if strings.ContainsRune(e.Value, 0) {
			return errorf("the value of %s has a NUL character, which an environment variable cannot hold", e.Name)
		}
	}
	return nil
}

// Names lists the entries' names.
func Names(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// toNode makes the document a file keeps for entries.
func toNode(kind Kind, entries []Entry) *yaml.Node {
	list := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, e := range entries {
		list.Content = append(list.Content, strNode(e.Name),
			mapNode("description", strNode(e.Description), "value", strNode(e.Value)))
	}
	return mapNode("version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(fileVersion)}, string(kind), list)
}

// fromNode reads entries from a document. A name may stand for its value
// alone (NAME: value), or for a mapping of value and description.
func fromNode(kind Kind, doc *yaml.Node) ([]Entry, error) {
	if doc == nil || doc.Kind == 0 { // nothing, or only comments
		return nil, nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil, nil // an empty file
		}
		doc = doc.Content[0]
	}
	if doc.Kind == yaml.ScalarNode && doc.ShortTag() == "!!null" {
		return nil, nil
	}
	if doc.Kind != yaml.MappingNode {
		return nil, errorf("the file should be a mapping with version and %s", string(kind))
	}
	var entries []Entry
	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i], doc.Content[i+1]
		switch k.Value {
		case "version":
			var n int
			if v.Kind != yaml.ScalarNode || v.Decode(&n) != nil {
				return nil, errorf("version should be a number")
			}
			if n > fileVersion {
				return nil, errorf("this file is for a newer yesman (version %d); update yesman to read it", n)
			}
			if n < 1 {
				return nil, errorf("version %d is unknown", n)
			}
		case string(kind):
			if v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
				continue
			}
			if v.Kind != yaml.MappingNode {
				return nil, errorf("line %d: %s should be a mapping of names to values", v.Line, string(kind))
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				e, err := entryOf(v.Content[j], v.Content[j+1])
				if err != nil {
					return nil, err
				}
				entries = append(entries, e)
			}
		default:
			return nil, errorf("line %d: %q is not a key of this file; it has version and %s", k.Line, k.Value, string(kind))
		}
	}
	return entries, Check(entries)
}

func entryOf(k, v *yaml.Node) (Entry, error) {
	e := Entry{Name: k.Value}
	if k.Kind != yaml.ScalarNode {
		return e, errorf("line %d: a name should be a word", k.Line)
	}
	switch v.Kind {
	case yaml.ScalarNode:
		s, err := scalar(v)
		e.Value = s
		return e, err
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			key, val := v.Content[i], v.Content[i+1]
			s, err := scalar(val)
			if err != nil {
				return e, err
			}
			switch key.Value {
			case "value":
				e.Value = s
			case "description":
				e.Description = s
			default:
				return e, errorf("line %d: %s has %q; an entry has only value and description", key.Line, e.Name, key.Value)
			}
		}
		return e, nil
	}
	return e, errorf("line %d: %s should have a value, or a value and a description", v.Line, e.Name)
}

// scalar is a value as text: a number or true stays as it was written.
func scalar(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", errorf("line %d: a value should be text", n.Line)
	}
	if n.ShortTag() == "!!null" {
		return "", nil
	}
	return n.Value, nil
}

// headers open the YAML yesman writes of each kind.
var headers = map[Kind]string{
	KindSecrets: `# yesman secrets. Every agent's commands get each as an environment variable.
# Agents read the names and descriptions, never the values.
# Comments are not kept: say what a secret is for in its description.
`,
	KindVariables: `# yesman variables. Every agent's commands get each as an environment variable.
# Agents read the names, values and descriptions.
# Comments are not kept: say what a variable is for in its description.
`,
}

// Format writes entries as the YAML the page edits and exports.
func Format(kind Kind, entries []Entry) []byte {
	var out bytes.Buffer
	out.WriteString(headers[kind])
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	_ = enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{toNode(kind, entries)}})
	_ = enc.Close()
	return out.Bytes()
}

// Parse reads the YAML Format writes, or one written by hand.
func Parse(kind Kind, data []byte) ([]Entry, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errorf("this is not YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if err := checkUnique(&doc); err != nil {
		return nil, err
	}
	return fromNode(kind, &doc)
}

// checkUnique refuses a mapping with a key twice, which YAML forbids but
// a node tree keeps.
func checkUnique(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return errorf("line %d: %s is there twice", n.Content[i].Line, k)
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if err := checkUnique(c); err != nil {
			return err
		}
	}
	return nil
}
