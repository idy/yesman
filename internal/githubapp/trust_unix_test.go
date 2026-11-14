//go:build !windows

package githubapp

import "testing"

// Unix gh reads the test's SSL_CERT_FILE, without changing an OS trust store.
func trustGHFixture(t *testing.T, der []byte) {}
