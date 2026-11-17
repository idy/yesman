package safefs

import "testing"

// macOS reports {0, 0} where the file system keeps no creation time
// (stat(2): "set to 0 (i.e. epoch)"); that is no creation time, and a real
// epoch-zero one cannot be told apart from it. Supplied values only: no file
// system without creation times is needed.
func TestBirthFromTimespec(t *testing.T) {
	for _, c := range []struct {
		sec, nsec int64
		want      int64
		ok        bool
	}{
		{0, 0, 0, false},
		{0, 1, 1, true},
		{1, 0, 1e9, true},
		{1763400000, 123456789, 1763400000123456789, true},
		{-1, 0, -1e9, true},
	} {
		got, ok := birthFromTimespec(c.sec, c.nsec)
		if got != c.want || ok != c.ok {
			t.Errorf("{%d, %d}: %d %v, want %d %v", c.sec, c.nsec, got, ok, c.want, c.ok)
		}
	}
}

func TestIdentityUsableNeedsANonzeroCreationTime(t *testing.T) {
	for _, c := range []struct {
		id   Identity
		want bool
	}{
		{Identity{Ino: 7}, false},
		{Identity{Ino: 7, HasBirth: true}, false},
		{Identity{Ino: 7, Birth: 5, HasBirth: false}, false},
		{Identity{Ino: 7, Birth: 1763400000123456789, HasBirth: true}, true},
		{Identity{Ino: 7, Birth: -1e9, HasBirth: true}, true},
	} {
		if got := c.id.Usable(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.id, got, c.want)
		}
	}
}

// A real directory here keeps reporting a usable identity.
func TestIdentityOfAnOrdinaryDirectoryIsUsable(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if id.Ino == 0 {
		t.Fatalf("no inode number: %+v", id)
	}
	if id.HasBirth && !id.Usable() {
		t.Fatalf("an ordinary directory's creation time is zero: %+v", id)
	}
}

// Raw Windows FILETIME values (100-nanosecond ticks since 1601) as
// GetFileInformationByHandle reports ftCreationTime. It is zero where the
// file system keeps no creation time: that is none, not the year 1601.
// Supplied values only: no volume without creation times is needed.
func TestSafeFSFiletimeCreationTime(t *testing.T) {
	const epoch = 116444736000000000 // 1970-01-01 in ticks since 1601
	split := func(ticks uint64) (uint32, uint32) { return uint32(ticks), uint32(ticks >> 32) }
	for _, c := range []struct {
		name   string
		ticks  uint64
		want   int64
		ok     bool
		usable bool
	}{
		{"no creation time", 0, 0, false, false},
		{"2026-11-17T00:00:00Z", epoch + 1794873600*1e7, 1794873600 * 1e9, true, true},
		{"1900-01-01T00:00:00Z", epoch - 2208988800*1e7, -2208988800 * 1e9, true, true},
		{"one tick after the epoch", epoch + 1, 100, true, true},
		// The Unix epoch itself converts to 0, which Usable refuses like
		// every other zero creation time.
		{"the Unix epoch", epoch, 0, true, false},
	} {
		low, high := split(c.ticks)
		got, ok := birthFromFiletime(low, high)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %d %v, want %d %v", c.name, got, ok, c.want, c.ok)
		}
		if u := (Identity{Ino: 7, Birth: got, HasBirth: ok}).Usable(); u != c.usable {
			t.Errorf("%s: usable %v, want %v", c.name, u, c.usable)
		}
	}
}
