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
