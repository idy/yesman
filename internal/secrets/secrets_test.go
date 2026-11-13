package secrets

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"go.yaml.in/yaml/v3"
)

func testKey(t *testing.T) *age.X25519Identity {
	t.Helper()
	f, err := os.Open("testdata/test-key.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		t.Fatal(err)
	}
	return ids[0].(*age.X25519Identity)
}

func decodeAny(t *testing.T, n *yaml.Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

var sopsMadeWant = map[string]any{
	"version": 1,
	"secrets": map[string]any{
		"GH_TOKEN": map[string]any{"description": "GitHub token, for gh", "value": "ghp_example0123456789"},
		"EMPTY":    map[string]any{"description": "", "value": ""},
		"PEM":      map[string]any{"description": "A key over lines", "value": "-----BEGIN KEY-----\nabc\n-----END KEY-----\n"},
		"SHORT":    "plain-shorthand",
	},
	"typed": map[string]any{
		"count": 42, "ratio": 1.5, "on": true, "off": false, "nothing": nil,
		"list": []any{"one", 2}, "note_unencrypted": "stays plain",
	},
}

// testdata/sops-made.yaml was encrypted by sops 3.13.3 itself.
func TestDecryptWhatSopsWrote(t *testing.T) {
	data, err := os.ReadFile("testdata/sops-made.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := Decrypt(data, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAny(t, tree); !reflect.DeepEqual(got, sopsMadeWant) {
		t.Errorf("decrypted\n%#v\nwant\n%#v", got, sopsMadeWant)
	}
	entries, err := fromNode(KindSecrets, &yaml.Node{Kind: yaml.MappingNode, Content: tree.Content[:4]})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GH_TOKEN", "EMPTY", "PEM", "SHORT"}; !slices.Equal(Names(entries), want) {
		t.Errorf("names %v, want %v", Names(entries), want)
	}

	other, _ := age.GenerateX25519Identity()
	if _, err := Decrypt(data, other); !errors.Is(err, ErrNoKey) {
		t.Errorf("another key: %v, want ErrNoKey", err)
	}
}

func TestDecryptRefusesChanges(t *testing.T) {
	data, _ := os.ReadFile("testdata/sops-made.yaml")
	id := testKey(t)
	lines := strings.Split(string(data), "\n")
	// Swapping two encrypted values moves each to a path it was not
	// encrypted for.
	swapped := slices.Clone(lines)
	i := slices.IndexFunc(swapped, func(l string) bool { return strings.Contains(l, "count:") })
	j := slices.IndexFunc(swapped, func(l string) bool { return strings.Contains(l, "version:") && strings.Contains(l, "ENC[") })
	ci, cj := strings.Index(swapped[i], "ENC["), strings.Index(swapped[j], "ENC[")
	swapped[i], swapped[j] = swapped[i][:ci]+swapped[j][cj:], swapped[j][:cj]+swapped[i][ci:]
	if _, err := Decrypt([]byte(strings.Join(swapped, "\n")), id); err == nil {
		t.Error("swapped values decrypted")
	}
	// A plain value the MAC covers, changed.
	changed := strings.Replace(string(data), "stays plain", "was changed", 1)
	if _, err := Decrypt([]byte(changed), id); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Errorf("changed plain value: %v, want a MAC error", err)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	id := testKey(t)
	entries := []Entry{
		{Name: "GH_TOKEN", Value: "ghp_x", Description: "GitHub"},
		{Name: "EMPTY"},
		{Name: "PEM", Value: "-----BEGIN-----\nline\n-----END-----\n", Description: "two\nlines"},
		{Name: "NUMBERISH", Value: "0123", Description: "true"},
	}
	data, err := Encrypt(toNode(KindSecrets, entries), []*age.X25519Recipient{id.Recipient()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ghp_x") || strings.Contains(string(data), "GitHub") {
		t.Errorf("a value is in the clear:\n%s", data)
	}
	tree, err := Decrypt(data, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromNode(KindSecrets, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, entries) {
		t.Errorf("round trip\n%#v\nwant\n%#v", got, entries)
	}
}

// The sops command reads what yesman writes, when it is installed.
func TestSopsReadsWhatYesmanWrites(t *testing.T) {
	sops, err := exec.LookPath("sops")
	if err != nil {
		t.Skip("sops is not installed")
	}
	data, _ := os.ReadFile("testdata/sops-made.yaml")
	tree, err := Decrypt(data, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encrypt(tree, []*age.X25519Recipient{testKey(t).Recipient()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets.sops.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := filepath.Abs("testdata/test-key.txt")
	cmd := exec.Command(sops, "decrypt", path)
	cmd.Env = append(os.Environ(), "SOPS_AGE_KEY_FILE="+key)
	plain, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sops decrypt: %v\n%s", err, plain)
	}
	var got map[string]any
	if err := yaml.Unmarshal(plain, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sopsMadeWant) {
		t.Errorf("sops decrypted\n%#v\nwant\n%#v", got, sopsMadeWant)
	}
}

func TestParseAndFormat(t *testing.T) {
	entries := []Entry{
		{Name: "B", Value: "multi\nline\n", Description: "first"},
		{Name: "A", Value: "yes", Description: ""},
		{Name: "N", Value: "12"},
	}
	text := Format(KindVariables, entries)
	got, err := Parse(KindVariables, text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !reflect.DeepEqual(got, entries) {
		t.Errorf("parsed\n%#v\nwant\n%#v\nfrom\n%s", got, entries, text)
	}
	if !strings.HasPrefix(string(text), "# yesman variables.") || !strings.Contains(string(text), "\nversion: 1\nvariables:\n  B:\n") {
		t.Errorf("formatted:\n%s", text)
	}

	short, err := Parse(KindSecrets, []byte("secrets:\n  TOKEN: abc\n  PORT: 8080\n"))
	if err != nil || !reflect.DeepEqual(short, []Entry{{Name: "TOKEN", Value: "abc"}, {Name: "PORT", Value: "8080"}}) {
		t.Errorf("shorthand: %#v, %v", short, err)
	}
	if empty, err := Parse(KindSecrets, []byte("# nothing yet\n")); err != nil || len(empty) != 0 {
		t.Errorf("empty file: %#v, %v", empty, err)
	}

	for _, bad := range []string{
		"secrets:\n  1BAD: x\n",
		"secrets:\n  PATH: /bin\n",
		"secrets:\n  YESMAN_TOKEN: x\n",
		"secrets:\n  A: x\n  A: y\n",
		"secrets:\n  A:\n    value: x\n    note: y\n",
		"secrets:\n  A: [x]\n",
		"variables:\n  A: x\n", // a variables file is not a secrets file
		"version: 2\nsecrets: {}\n",
		"secrets:\n  TOKEN_unencrypted: x\n", // sops would keep it in the clear
		"secrets: [\n",
	} {
		if _, err := Parse(KindSecrets, []byte(bad)); err == nil {
			t.Errorf("Parse accepted %q", bad)
		}
	}
	if got, err := Parse(KindVariables, []byte("variables:\n  NOTE_unencrypted: x\n")); err != nil || len(got) != 1 {
		t.Errorf("a variable may end in the suffix: %#v, %v", got, err)
	}
	var e *Error
	if _, err := Parse(KindSecrets, []byte("secrets:\n  A-B: x\n")); !errors.As(err, &e) {
		t.Errorf("a mistake is %T, not *Error", err)
	}
}

func newVault(t *testing.T) *Vault {
	v := NewVault(filepath.Join(t.TempDir(), "secrets"))
	v.WorkFactor = 10 // fast; age's default takes a second
	return v
}

func TestVault(t *testing.T) {
	v := newVault(t)
	if v.Configured() {
		t.Fatal("configured before Create")
	}
	if err := v.Unlock("password1"); !errors.Is(err, ErrNotSetUp) {
		t.Errorf("Unlock before Create: %v", err)
	}
	if err := v.Create("short"); err == nil {
		t.Error("Create took a short password")
	}
	if err := v.Create("password1"); err != nil {
		t.Fatal(err)
	}
	if !v.Configured() || !v.Unlocked() {
		t.Fatal("not configured and unlocked after Create")
	}
	if err := v.Create("password1"); err == nil {
		t.Error("Create ran twice")
	}
	if info, err := os.Stat(v.File()); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("secrets file: %v, %v", info.Mode(), err)
	}

	entries := []Entry{{Name: "GH_TOKEN", Value: "ghp_secret", Description: "GitHub"}, {Name: "OPENAI_API_KEY", Value: "sk-x"}}
	if err := v.Save(entries); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(v.File())
	if strings.Contains(string(data), "ghp_secret") {
		t.Error("a secret is in the clear")
	}

	v.Lock()
	if _, err := v.Entries(); !errors.Is(err, ErrLocked) {
		t.Errorf("Entries while locked: %v", err)
	}
	if err := v.Save([]Entry{{Name: "KEY_unencrypted", Value: "x"}}); err == nil || errors.Is(err, ErrLocked) {
		t.Errorf("Save of a name sops keeps plain: %v", err)
	}
	if err := v.Save(nil); !errors.Is(err, ErrLocked) {
		t.Errorf("Save while locked: %v", err)
	}
	if names, err := v.Names(); err != nil || !slices.Equal(names, []string{"GH_TOKEN", "OPENAI_API_KEY"}) {
		t.Errorf("Names while locked: %v, %v", names, err)
	}
	if err := v.Unlock("wrong password"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
	if err := v.Unlock("password1"); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Entries(); err != nil || !reflect.DeepEqual(got, entries) {
		t.Errorf("Entries: %#v, %v", got, err)
	}

	// The key opens the file for sops too.
	key, err := v.Key()
	if err != nil {
		t.Fatal(err)
	}
	ids, err := age.ParseIdentities(strings.NewReader(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(data, ids...); err != nil {
		t.Errorf("the exported key does not open the file: %v", err)
	}

	if err := v.ChangePassword("wrong password", "password2"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("ChangePassword with a wrong one: %v", err)
	}
	if err := v.ChangePassword("password1", "password2"); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if err := v.Unlock("password1"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("old password still unlocks: %v", err)
	}
	if err := v.Unlock("password2"); err != nil {
		t.Fatal(err)
	}
}

func TestVaultKeepsOtherRecipientsAndFollowsTheFile(t *testing.T) {
	v := newVault(t)
	if err := v.Create("password1"); err != nil {
		t.Fatal(err)
	}
	// Someone added the test key with sops, and changed the file.
	me, _ := v.Key()
	ids, _ := age.ParseIdentities(strings.NewReader(me))
	mine := ids[0].(*age.X25519Identity)
	other := testKey(t)
	data, err := Encrypt(toNode(KindSecrets, []Entry{{Name: "FROM_SOPS", Value: "v"}}),
		[]*age.X25519Recipient{mine.Recipient(), other.Recipient()}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.File(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := v.Entries()
	if err != nil || len(got) != 1 || got[0].Name != "FROM_SOPS" {
		t.Fatalf("after the file changed: %#v, %v", got, err)
	}
	if err := v.Save(append(got, Entry{Name: "MORE", Value: "w"})); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(v.File())
	if _, err := Decrypt(data, other); err != nil {
		t.Errorf("saving dropped the other recipient: %v", err)
	}
}

func TestVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "variables.yaml")
	v := NewVariables(path)
	if got, err := v.Entries(); err != nil || len(got) != 0 {
		t.Fatalf("no file: %#v, %v", got, err)
	}
	entries := []Entry{{Name: "LARK_APP_ID", Value: "cli_1", Description: "Lark app"}}
	if err := v.Save(entries); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Entries(); !reflect.DeepEqual(got, entries) {
		t.Errorf("Entries: %#v", got)
	}
	// Edited by hand.
	if err := os.WriteFile(path, []byte("variables:\n  REGION: eu\n  LONGER_NAME: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Entries(); err != nil || len(got) != 2 || got[0].Value != "eu" {
		t.Errorf("after a hand edit: %#v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("variables: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Entries(); err == nil {
		t.Error("a broken file read")
	}
}

func TestPlanImportAndMerge(t *testing.T) {
	entries := []Entry{{Name: "A", Value: "1"}, {Name: "B", Value: "2", Description: "b"}}
	current := []Entry{{Name: "B", Value: "old"}, {Name: "C", Value: "3"}, {Name: "A", Value: "1"}}
	plan := PlanImport(current, entries)
	if want := (Plan{Add: []string{}, Change: []string{"B"}, Same: []string{"A"}, Extra: []string{"C"}}); !reflect.DeepEqual(plan, want) {
		t.Errorf("plan %#v, want %#v", plan, want)
	}
	if got := Merge(current, entries, false); !reflect.DeepEqual(got, []Entry{entries[1], current[1], entries[0]}) {
		t.Errorf("merge: %#v", got)
	}
	if got := Merge(current, entries, true); !reflect.DeepEqual(got, entries) {
		t.Errorf("replace: %#v", got)
	}
}
