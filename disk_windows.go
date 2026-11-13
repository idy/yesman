package main

import "golang.org/x/sys/windows"

// diskSpace is the size of the disk that holds path and the space free to
// use there, as this user may.
func diskSpace(path string) (DiskSpace, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskSpace{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return DiskSpace{}, err
	}
	return DiskSpace{Total: int64(total), Free: int64(free)}, nil
}
