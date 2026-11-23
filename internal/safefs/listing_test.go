package safefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReadNamesAdvances(t *testing.T) {
	root := t.TempDir()
	const total = 260
	for i := range total {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("pack-%03d.keep", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	}()
	seen := map[string]bool{}
	for _, want := range []int{128, 128, 4, 0} {
		names, err := dir.ReadNames(128)
		if (want == 0 && !errors.Is(err, io.EOF)) || (want != 0 && err != nil) || len(names) != want {
			t.Fatalf("read %d names, want %d: %v", len(names), want, err)
		}
		for _, name := range names {
			if seen[name] {
				t.Fatalf("listing repeated %q", name)
			}
			seen[name] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("listed %d of %d entries", len(seen), total)
	}
}
