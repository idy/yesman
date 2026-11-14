package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise installed gh itself through an isolated TLS proxy. Only fixture
// credentials and GitHub-shaped mock responses are used; no network request can
// leave the loopback proxy, and no user's gh config is loaded by the child.
func TestRealGHAppSubprocess(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("CI requires gh for the App subprocess test")
		}
		t.Skip("GitHub CLI is not installed")
	}
	_, app, key := fixture(t)
	tlsKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "yesman isolated fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"api.github.com", "github.com"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &tlsKey.PublicKey, tlsKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(tlsKey)}))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var queries []string
	creates := 0
	repository := map[string]any{"id": "R_fixture", "name": "yesman", "nameWithOwner": "idy/yesman", "owner": map[string]any{"login": "idy", "__typename": "User"}, "isFork": false, "isPrivate": false, "isArchived": false, "isEmpty": false, "hasIssuesEnabled": true, "hasWikiEnabled": false, "viewerPermission": "READ", "defaultBranchRef": map[string]any{"name": "main"}, "pullRequests": map[string]any{"nodes": []any{}}, "mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": true}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			writeJSON(w, installation())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			writeJSON(w, tokenJSON(fixtureToken, time.Now().Add(time.Hour)))
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+fixtureToken && auth != "token "+fixtureToken {
			t.Error("gh did not use the repository installation token")
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/repos/idy/yesman":
			writeJSON(w, map[string]string{"full_name": "idy/yesman"})
		case "/repos/idy/yesman/pulls/77":
			writeJSON(w, map[string]any{"number": 77, "html_url": "https://github.com/idy/yesman/pull/77", "draft": true, "user": map[string]string{"type": "Bot", "login": app.Slug + "[bot]", "avatar_url": "https://avatars.githubusercontent.com/fixture"}, "head": map[string]any{"ref": "test/app-identity", "sha": strings.Repeat("a", 40), "repo": map[string]string{"full_name": "idy/yesman"}}})
		case "/graphql":
			var request struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("bad GraphQL request")
				return
			}
			mu.Lock()
			queries = append(queries, request.Query)
			mu.Unlock()
			if strings.Contains(request.Query, "createPullRequest") {
				mu.Lock()
				creates++
				mu.Unlock()
				input, _ := request.Variables["input"].(map[string]any)
				if input["headRefName"] != "test/app-identity" || input["baseRefName"] != "main" || input["draft"] != true || input["body"] != "Synthetic fixture body.\n" {
					t.Errorf("wrong create variables: %#v", input)
				}
				writeJSON(w, map[string]any{"data": map[string]any{"createPullRequest": map[string]any{"pullRequest": map[string]any{"id": "PR_fixture", "number": 77, "url": "https://github.com/idy/yesman/pull/77"}}}})
			} else {
				writeJSON(w, map[string]any{"data": map[string]any{"repository": repository, "viewer": map[string]string{"login": app.Slug + "[bot]"}}})
			}
		default:
			t.Errorf("unexpected gh request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(handler))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	upstream.StartTLS()
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "api.github.com:443" {
			t.Error("refused non-GitHub proxy target")
			http.Error(w, "refused", 403)
			return
		}
		target, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			target.Close()
			return
		}
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		go func() { defer target.Close(); defer connection.Close(); _, _ = io.Copy(target, buffer) }()
		_, _ = io.Copy(connection, target)
	}))
	defer proxy.Close()
	caFile := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	t.Setenv("SSL_CERT_FILE", caFile)
	t.Setenv("GH_TOKEN", "personal-fixture-must-not-win")
	t.Setenv("GITHUB_TOKEN", "other-personal-fixture")
	t.Setenv("GH_DEBUG", "api")
	t.Setenv("OPENAI_API_KEY", "unrelated-fixture-must-not-leak")
	client, err := ClientFromKey(app, key, []string{"idy/yesman"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.base = upstream.URL
	client.http = upstream.Client()
	if runtime.GOOS == "windows" || os.Getenv("YESMAN_TEST_GH_SOCKET") == "1" {
		// gh's supported http_unix_socket transport works on Windows without
		// changing a trust store, hostname or production TLS configuration.
		// Keep the socket path short enough for sockaddr_un on every OS.
		socketDir, err := os.MkdirTemp("", "gh-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(socketDir)
		socketPath := filepath.Join(socketDir, "s")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal("local gh socket fixture:", err)
		}
		socketServer := &http.Server{Handler: http.HandlerFunc(handler)}
		served := make(chan struct{})
		go func() { defer close(served); _ = socketServer.Serve(listener) }()
		defer func() { _ = socketServer.Close(); <-served }()
		client.ghEnv = func(dir, token string) []string {
			config, _ := json.Marshal(map[string]string{"http_unix_socket": socketPath})
			if err := os.WriteFile(filepath.Join(dir, "config.yml"), config, 0600); err != nil {
				t.Fatal(err)
			}
			return ghEnvironment(dir, token)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pull, err := client.CreateWithGH(ctx, gh, PullInput{Repository: "idy/yesman", Head: "test/app-identity", Base: "main", Title: "test: App identity fixture", Body: "Synthetic fixture body.\n", Draft: true})
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("real gh: %v; observed query shapes: %v", err, queries)
	}
	if pull.User.Type != "Bot" || pull.User.Login != app.Slug+"[bot]" || pull.Number != 77 {
		t.Fatalf("wrong result: %+v", pull)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Fatalf("creates = %d", creates)
	}
	env := strings.Join(ghEnvironment(t.TempDir(), fixtureToken), "\n")
	for _, unexpected := range []string{"personal-fixture", "other-personal", "OPENAI_API_KEY", "GH_DEBUG"} {
		if strings.Contains(env, unexpected) {
			t.Fatal("unexpected credential/debug inheritance")
		}
	}
}
