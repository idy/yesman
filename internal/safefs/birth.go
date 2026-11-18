package safefs

// birthFromTimespec returns a creation time in nanoseconds from stat's
// st_birthtimespec. macOS sets it to 0 (the epoch) where the file system
// keeps no creation time; a real epoch-zero creation time cannot be told
// apart from that, so {0, 0} reports none.
func birthFromTimespec(sec, nsec int64) (int64, bool) {
	if sec == 0 && nsec == 0 {
		return 0, false
	}
	return sec*1e9 + nsec, true
}

// birthFromFiletime returns a creation time in Unix nanoseconds from a raw
// Windows FILETIME (100-nanosecond ticks since 1601), as GetFileInformation
// ByHandle's ftCreationTime reports it. That member is zero where the file
// system keeps no creation time, so the all-zero pair reports none; it has
// to be caught before conversion, which would turn it into a large nonzero
// value. Other values convert exactly as x/sys/windows Filetime.Nanoseconds,
// so recorded identities still compare equal.
func birthFromFiletime(low, high uint32) (int64, bool) {
	if low == 0 && high == 0 {
		return 0, false
	}
	nsec := int64(high)<<32 + int64(low)
	nsec -= 116444736000000000
	nsec *= 100
	return nsec, true
}

// Usable reports whether id can name a directory across time: the file
// system reported a creation time, and it is not zero, which is ambiguous
// with "none" on some systems. Without it an inode number alone, reusable
// after removal, would be the only discriminator.
func (id Identity) Usable() bool {
	return id.HasBirth && id.Birth != 0
}
