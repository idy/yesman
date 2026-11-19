package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsRelocationDirectory(t *testing.T) {
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
	other, err := windows.UTF16PtrFromString(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := flushSameReceiptDir(windows.Handle(f.Fd()), other); err == nil || !strings.Contains(err.Error(), "directory changed") {
		t.Fatalf("different directory identity: %v", err)
	}

	// A real outstanding reader can temporarily deny write access. Preserve
	// that refusal and its operation name, then retry when the reader closes.
	name, err := windows.UTF16PtrFromString(archive)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	flushErr := flushReceiptDir(f)
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	var op *os.SyscallError
	if !errors.Is(flushErr, windows.ERROR_SHARING_VIOLATION) || !errors.As(flushErr, &op) || op.Syscall != "CreateFileW(relocation directory)" {
		t.Fatalf("directory sharing refusal: %v", flushErr)
	}
	if err := syncRelocationDirectory(group); err != nil {
		t.Fatal(err)
	}
}
