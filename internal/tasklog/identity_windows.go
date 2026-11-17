package tasklog

import (
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func fileIdentity(f *os.File) (Identity, error) {
	var basic windows.ByHandleFileInformation
	handle := windows.Handle(f.Fd())
	if err := windows.GetFileInformationByHandle(handle, &basic); err != nil {
		return Identity{}, err
	}
	// FileIdInfo is 128-bit (also on ReFS); a 64-bit FileIndex is insufficient
	// on filesystems with wider IDs. Never substitute bytes/mtime for failure.
	var info struct {
		Volume uint64
		ID     [16]byte
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return Identity{}, fmt.Errorf("stable Windows FileID unavailable: %w", err)
	}
	if info.ID == [16]byte{} || basic.CreationTime == (windows.Filetime{}) {
		return Identity{}, errors.New("filesystem cannot prove a stable source FileID/creation time")
	}
	return Identity{Platform: "windows", Volume: fmt.Sprintf("%016x", info.Volume), ID: hex.EncodeToString(info.ID[:]), Birth: fmt.Sprintf("%08x%08x", basic.CreationTime.HighDateTime, basic.CreationTime.LowDateTime)}, nil
}
