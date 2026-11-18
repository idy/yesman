package githubapp

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWTSignatureAndClaims(t *testing.T) {
	client := clientFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP call") })
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	jwt, err := client.jwt()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&client.key.PublicKey, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatal(err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var parsed struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claims, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Iss != "123" || parsed.Iat != now.Add(-time.Minute).Unix() || parsed.Exp != now.Add(9*time.Minute).Unix() {
		t.Fatal("wrong JWT claims")
	}
}

func TestCancelWaiterDoesNotCancelSharedMint(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			writeJSON(w, installation())
			return
		}
		close(started)
		<-release
		writeJSON(w, tokenJSON(fixtureToken, time.Now().Add(time.Hour)))
	})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := client.token(ctx, "idy/yesman"); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() { _, err := client.token(context.Background(), "idy/yesman"); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("other waiter: %v", err)
	}
}

func TestForeignOwnerRegistrationDoesNotSaveKey(t *testing.T) {
	dir, app, key := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		writeJSON(w, map[string]any{"id": app.ID, "slug": app.Slug, "html_url": app.URL, "pem": string(key), "owner": map[string]string{"login": "other", "type": "User"}})
	}))
	defer server.Close()
	s := NewRegistrationServer("idy", "yesman test", dir)
	s.base = server.URL
	_, err := s.exchange(context.Background(), "valid_code_123")
	if err == nil || !strings.Contains(err.Error(), "unexpected owner") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("owner check: %v", err)
	}
}

func TestUnexpectedPRIdentityReportedWithoutRetry(t *testing.T) {
	creates := 0
	client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			writeJSON(w, installation())
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			writeJSON(w, tokenJSON(fixtureToken, time.Now().Add(time.Hour)))
		default:
			creates++
			w.WriteHeader(201)
			writeJSON(w, map[string]any{"number": 77, "user": map[string]string{"login": "idy", "type": "User"}})
		}
	})
	pull, err := client.CreatePull(context.Background(), PullInput{Repository: "idy/yesman", Head: "test/identity", Base: "main", Title: "test: Identity", Draft: true})
	if err == nil || pull.Number != 77 || creates != 1 {
		t.Fatalf("identity mismatch: %v, attempts %d", err, creates)
	}
}
