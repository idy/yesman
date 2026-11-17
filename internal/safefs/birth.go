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

// Usable reports whether id can name a directory across time: the file
// system reported a creation time, and it is not zero, which is ambiguous
// with "none" on some systems. Without it an inode number alone, reusable
// after removal, would be the only discriminator.
func (id Identity) Usable() bool {
	return id.HasBirth && id.Birth != 0
}
