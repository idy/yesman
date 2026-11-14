//go:build windows

package githubapp

import (
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows gh uses the native trust store. certutil can open an interactive root
// trust prompt even with -f, so the disposable CI fixture uses CryptoAPI. ADD_NEW
// never replaces an existing certificate; cleanup holds the exact inserted
// context. This test-only helper is gated to GitHub-hosted runners.
func trustGHFixture(t *testing.T, der []byte) {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Skip("Windows TLS trust fixture is limited to disposable GitHub-hosted runners")
	}
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		t.Fatal(err)
	}
	store, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM_W, 0, 0, windows.CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if err != nil {
		t.Fatal("open CurrentUser fixture certificate store:", err)
	}
	t.Cleanup(func() {
		if err := windows.CertCloseStore(store, 0); err != nil {
			t.Error("close fixture certificate store:", err)
		}
	})
	if len(der) == 0 {
		t.Fatal("empty fixture certificate")
	}
	certificate, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		t.Fatal("parse fixture certificate:", err)
	}
	defer windows.CertFreeCertificateContext(certificate)
	var inserted *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(store, certificate, windows.CERT_STORE_ADD_NEW, &inserted); err != nil {
		t.Fatal("trust generated fixture certificate:", err)
	}
	t.Cleanup(func() {
		if err := windows.CertDeleteCertificateFromStore(inserted); err != nil {
			t.Error("remove exact fixture certificate:", err)
		}
	})
}
