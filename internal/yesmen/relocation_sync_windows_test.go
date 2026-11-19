package yesmen

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsRelocationDirectory(t *testing.T) {
	// Use the same Root and directory handles used during deletion. Separate
	// the OS call results so the hosted log identifies the failed operation.
	base := t.TempDir()
	archive := filepath.Join(base, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(base, "yesmen"), archive)
	group, err := s.deletionParent(true)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	f, err := group.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Also report whether a direct Win32 directory handle accepts a flush.
	name, err := windows.UTF16PtrFromString(archive)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	t.Logf("CreateFileW(directory, GENERIC_WRITE): %v", err)
	if err == nil {
		t.Logf("FlushFileBuffers(direct directory): %v", windows.FlushFileBuffers(h))
		t.Logf("CloseHandle(direct directory): %v", windows.CloseHandle(h))
	}
	if err := flushReceiptDir(f); err != nil {
		t.Fatal(err)
	}
}
