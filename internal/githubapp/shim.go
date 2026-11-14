package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallShim atomically installs one private executable named gh. It contains
// no credential. The desktop recognizes this invocation before initializing
// MyGo. No global PATH, gh config, git config or executable is changed.
func InstallShim(root, executable string) (string, error) {
	info, err := os.Stat(executable)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "github-app-gh", "bin")
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(dir, name)
	if existing, err := os.Stat(dest); err == nil && os.SameFile(info, existing) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// The app starts after its prior agents have stopped. Replace a complete
	// binary atomically; if Windows still has it open, leave it untouched and
	// report the error instead of accumulating old runtime executables.
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	tmp := filepath.Join(dir, ".gh-"+hex.EncodeToString(nonce))
	if err := os.Link(executable, tmp); err == nil {
		defer os.Remove(tmp)
		if err := os.Rename(tmp, dest); err != nil {
			return "", err
		}
		return dir, nil
	}
	// Cross-volume app/data layouts cannot hard-link.
	source, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer source.Close()
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if _, err = io.Copy(f, source); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dir, nil
}

// IsShim is checked before app startup/single-instance handling. The alternate
// flag allows isolated helpers to exercise the identical path in tests.
func IsShim(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	return name == "gh" || name == "gh.exe" || len(args) > 1 && args[1] == "--yesman-github-app-gh"
}

// createInvocation finds the command after persistent flags, rather than
// assuming pr/create are adjacent. A command-looking flag value is still data.
// Ambiguous unsupported flags in the pr group fail closed when create follows.
func createInvocation(args []string) ([]string, bool) {
	root := -1
	ambiguous := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return possibleCreate(args)
		}
		if strings.HasPrefix(arg, "-") {
			name, _, inline := optionParts(arg)
			if takesValue(name) && !inline {
				i++
			} else if !inline && !booleanOption(name) {
				ambiguous = true
			}
			continue
		}
		if root < 0 {
			if arg != "pr" {
				if ambiguous {
					return possibleCreate(args)
				}
				return nil, false
			}
			root = i
			continue
		}
		if arg != "create" {
			// A recognized subcommand ends command discovery. Later strings
			// named pr/create (e.g. a title) cannot change its identity.
			if ambiguous {
				return possibleCreate(args)
			}
			return nil, false
		}
		out := []string{"pr", "create"}
		for j, value := range args {
			if j != root && j != i {
				out = append(out, value)
			}
		}
		return out, true
	}
	return nil, false
}

// Unknown flag arity cannot be allowed to hide a create operation. Preserve
// every ambiguous argument so ParseCreate rejects it in App mode.
func possibleCreate(args []string) ([]string, bool) {
	for i, arg := range args {
		if arg != "pr" {
			continue
		}
		for j := i + 1; j < len(args); j++ {
			if args[j] == "create" {
				out := []string{"pr", "create"}
				for k, value := range args {
					if k != i && k != j {
						out = append(out, value)
					}
				}
				return out, true
			}
		}
	}
	return nil, false
}

func optionParts(arg string) (name, value string, inline bool) {
	name, value, inline = strings.Cut(arg, "=")
	short := map[string]string{"-R": "--repo", "-H": "--head", "-B": "--base", "-t": "--title", "-b": "--body", "-F": "--body-file", "-d": "--draft", "-h": "--help"}
	if len(name) > 2 && !strings.HasPrefix(name, "--") {
		if full, ok := short[name[:2]]; ok && full != "--draft" && full != "--help" {
			return full, name[2:], true
		}
	}
	if full, ok := short[name]; ok {
		name = full
	}
	return
}

func takesValue(name string) bool {
	switch name {
	case "--repo", "--head", "--base", "--title", "--body", "--body-file", "--assignee", "-a", "--reviewer", "-r", "--label", "-l", "--milestone", "-m", "--project", "-p", "--template", "-T", "--recover", "--hostname", "--json", "--jq":
		return true
	}
	return false
}

func booleanOption(name string) bool {
	switch name {
	case "--help", "--draft", "--web", "-w", "--fill", "--fill-first", "--fill-verbose", "--no-maintainer-edit", "--dry-run", "--editor", "-e", "--version":
		return true
	}
	return false
}

func helpRequested(args []string) bool {
	requested := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return false
		}
		name, value, inline := optionParts(args[i])
		if strings.HasPrefix(args[i], "-") && !takesValue(name) && !booleanOption(name) {
			return false
		}
		if name == "--help" && (!inline || value == "true") {
			requested = true
		}
		if takesValue(name) && !inline {
			i++
		}
	}
	return requested
}

// ParseCreate handles noninteractive PR creation only. Unknown options fail
// clearly instead of changing identity or allowing gh to push a branch.
func ParseCreate(args []string, stdin io.Reader) (PullInput, error) {
	var in PullInput
	normalized, ok := createInvocation(args)
	if !ok {
		return in, errors.New("expected gh pr create with explicit flags")
	}
	args = normalized[2:]
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, value, hasValue := optionParts(args[i])
		if seen[name] {
			return in, fmt.Errorf("App PR creation does not accept repeated %s", name)
		}
		seen[name] = true
		if name == "--draft" {
			if !hasValue || value == "true" {
				in.Draft = true
			} else if value != "false" {
				return in, errors.New("--draft must be true or false")
			}
			continue
		}
		switch name {
		case "--repo", "--head", "--base", "--title", "--body", "--body-file":
		default:
			return in, errors.New("App PR creation supports --repo, --head, --base, --title, --body/--body-file and --draft; push the branch first")
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return in, fmt.Errorf("%s requires a value", name)
			}
			value = args[i]
		}
		switch name {
		case "--repo":
			in.Repository = strings.ToLower(value)
		case "--head":
			in.Head = value
		case "--base":
			in.Base = value
		case "--title":
			in.Title = value
		case "--body":
			in.Body = value
		case "--body-file":
			reader := stdin
			if value != "-" {
				f, err := os.Open(value)
				if err != nil {
					return in, errors.New("could not open PR body file")
				}
				defer f.Close()
				reader = f
			}
			data, err := io.ReadAll(io.LimitReader(reader, 65537))
			if err != nil || len(data) > 65536 {
				return in, errors.New("PR body must be at most 64 KiB")
			}
			in.Body = string(data)
		}
	}
	if seen["--body"] && seen["--body-file"] {
		return in, errors.New("use --body or --body-file, not both")
	}
	if !validRepository(in.Repository) || !validRef(in.Head) || !validRef(in.Base) || strings.TrimSpace(in.Title) == "" {
		return in, errors.New("App PR creation requires explicit --repo owner/repo, --head, --base and --title")
	}
	return in, nil
}

func brokerCall(ctx context.Context, method, path string, body, out any) error {
	base, err := url.Parse(os.Getenv("YESMAN_API"))
	if err != nil || base.Scheme != "http" || base.Hostname() != "127.0.0.1" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" || os.Getenv("YESMAN_TOKEN") == "" {
		return errors.New("yesman's local GitHub identity service is unavailable; no personal fallback was used")
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errors.New("invalid PR request")
		}
		payload = strings.NewReader(string(data))
	}
	request, err := http.NewRequestWithContext(ctx, method, base.String()+path, payload)
	if err != nil {
		return errors.New("invalid local identity request")
	}
	request.Header.Set("Authorization", "Bearer "+os.Getenv("YESMAN_TOKEN"))
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("yesman's GitHub identity service did not answer; inspect the repository before retrying a create request")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 70001))
	if err != nil || len(data) > 70000 {
		return errors.New("invalid local identity response")
	}
	if response.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" && len(failure.Error) < 4096 {
			return errors.New(failure.Error)
		}
		return errors.New("yesman's GitHub identity service refused the request")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid local identity response")
	}
	return nil
}

// RunShim never receives an installation token. Only the trusted server's gh
// child does. Personal mode passes all arguments/environment/stdin through;
// App mode has no fallback on server, parsing, authentication or HTTP failures.
func RunShim(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	realGH := os.Getenv("YESMAN_GH_REAL")
	if !filepath.IsAbs(realGH) {
		fmt.Fprintln(stderr, "yesman: the original GitHub CLI is unavailable")
		return 1
	}
	own, _ := os.Executable()
	ownInfo, _ := os.Stat(own)
	realInfo, _ := os.Stat(realGH)
	if ownInfo != nil && realInfo != nil && os.SameFile(ownInfo, realInfo) {
		fmt.Fprintln(stderr, "yesman: GitHub CLI wrapper recursion refused")
		return 1
	}
	personal := func() int {
		cmd := exec.CommandContext(ctx, realGH, args...)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.Env = os.Environ()
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exit.ExitCode()
			}
			fmt.Fprintln(stderr, "yesman: could not start GitHub CLI")
			return 1
		}
		return 0
	}
	if _, create := createInvocation(args); !create {
		return personal()
	}
	if helpRequested(args) {
		return personal()
	}
	var mode struct {
		Mode string `json:"mode"`
	}
	if err := brokerCall(ctx, "GET", "/v1/github-app/mode", nil, &mode); err != nil {
		fmt.Fprintln(stderr, "yesman:", err)
		return 1
	}
	if mode.Mode == "personal" {
		return personal()
	}
	if mode.Mode != "app" {
		fmt.Fprintln(stderr, "yesman: unknown PR identity mode; no personal fallback was used")
		return 1
	}
	if host := os.Getenv("GH_HOST"); host != "" && !strings.EqualFold(host, "github.com") {
		fmt.Fprintln(stderr, "yesman: App PR identity supports github.com only; remove the conflicting GH_HOST or change Settings")
		return 1
	}
	input, err := ParseCreate(args, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "yesman:", err)
		return 1
	}
	var pull Pull
	if err := brokerCall(ctx, "POST", "/v1/github-app/pulls", input, &pull); err != nil {
		fmt.Fprintln(stderr, "yesman:", err)
		return 1
	}
	number, urlErr := pullNumber(pull.URL, input.Repository)
	if urlErr != nil || pull.Number != number || pull.User.Type != "Bot" {
		fmt.Fprintln(stderr, "yesman: invalid PR identity response; inspect the repository before retrying")
		return 1
	}
	fmt.Fprintln(stdout, pull.URL)
	fmt.Fprintln(stderr, "Created as", pull.User.Login)
	return 0
}
