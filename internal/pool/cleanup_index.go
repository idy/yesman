package pool

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/idy/yesman/internal/safefs"
)

// These holds let the desktop explain why the original index must stay.
var (
	ErrWorktreeResolveUndo = errors.New("the Git index contains saved conflict stages (resolve-undo)")
	ErrWorktreeIndex       = errors.New("could not verify the worktree's Git index")
)

// Git can ignore a bad trailing checksum on ordinary read-only commands.
// Check the regular index through the private registration's handle before
// asking Git to interpret its stages. Git's index.skipHash uses a zero sum.
func checkIndexFile(d *safefs.Dir, oidWidth int) error {
	const maxIndex = 64 << 20
	var digest hash.Hash
	switch oidWidth {
	case sha1.Size * 2:
		digest = sha1.New()
	case sha256.Size * 2:
		digest = sha256.New()
	default:
		return ErrWorktreeIndex
	}
	f, err := d.OpenFile("index", os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorktreeIndex, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < int64(12+digest.Size()) || info.Size() > maxIndex {
		return ErrWorktreeIndex
	}
	if _, err := io.CopyN(digest, f, info.Size()-int64(digest.Size())); err != nil {
		return fmt.Errorf("%w: %w", ErrWorktreeIndex, err)
	}
	// Reading one additional byte detects a growing file without consuming it.
	sum, err := io.ReadAll(io.LimitReader(f, int64(digest.Size()+1)))
	if err != nil || len(sum) != digest.Size() || !bytes.Equal(sum, digest.Sum(nil)) && !bytes.Equal(sum, make([]byte, digest.Size())) {
		return ErrWorktreeIndex
	}
	return nil
}

// Git decodes the index format and exposes resolved stages separately from
// ordinary clean entries. Treat diagnostics, including a skipped extension,
// as an unreadable index. Keep output memory constant for both streams.
func checkIndexRecovery(ctx context.Context, path, private string) error {
	var stages, diagnostics outputPresent
	cmd := gitCommand(ctx, path, "--git-dir="+private, "--work-tree="+path, "--no-optional-locks", "-c", "core.fsmonitor=false", "ls-files", "--resolve-undo", "-z")
	cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+filepath.Join(private, "index"))
	cmd.Stdout, cmd.Stderr = &stages, &diagnostics
	if err := cmd.Run(); err != nil || diagnostics {
		return ErrWorktreeIndex
	}
	if stages {
		return ErrWorktreeResolveUndo
	}
	return nil
}

type outputPresent bool

func (p *outputPresent) Write(data []byte) (int, error) {
	if len(data) != 0 {
		*p = true
	}
	return len(data), nil
}
