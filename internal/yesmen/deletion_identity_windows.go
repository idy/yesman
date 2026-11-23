package yesmen

import (
	"io/fs"
	"os"
	"unsafe"

	"github.com/idy/yesman/internal/safefs"
	"golang.org/x/sys/windows"
)

func deletionEntryID(dir *os.File, name string, st fs.FileInfo, volume safefs.Identity) (safefs.Identity, error) {
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return safefs.Identity{}, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: ntName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	// The only component here is the leaf. Open its reparse point, if any,
	// without following it, so a symbolic link's referent is never touched.
	err = windows.NtCreateFile(&h, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, &attrs, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return safefs.Identity{}, err
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameDeletionEntry(st, opened) {
		return safefs.Identity{}, errCheckoutPreviewChanged
	}
	id, err := safefs.FileIdentity(f)
	if err != nil || !id.Usable() || !sameMount(id, volume) {
		return safefs.Identity{}, errCheckoutPreviewChanged
	}
	return id, nil
}
