package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// forkCopyFault, when set by a test, is asked after each chunk copySession
// writes; an error it returns interrupts the copy there.
var forkCopyFault func(written int64) error

// copySession copies the session file src into a new session of the
// directory sessions, recorded under cwd, as pi's --fork does: a new
// header naming src as its parent, then src's entries. It returns the new
// file's path.
//
// pi's --fork writes its copy under the final name entry by entry, so an
// interrupted fork (a full disk, a crash, a kill) leaves a shorter copy
// that the next start would resume as the newest session, silently missing
// the rest of the conversation. copySession writes a staging file pi never
// reads, makes it durable, and only then links it to a session name, which
// never replaces an existing file. An error removes only its own staging
// file and leaves src and every session as they were; the next start
// copies again.
//
// The entries are copied as src holds them, byte for byte, after its
// header's line: pi skips, reading the copy, the lines it skips reading
// src. The header's version is src's, so pi reads the entries as it would
// read src.
func copySession(ctx context.Context, sessions, src, cwd string) (string, error) {
	looked, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if !looked.Mode().IsRegular() {
		return "", errors.New("the session to copy is not a regular file")
	}
	in, err := os.OpenFile(src, sessionOpenFlags, 0)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(looked, info) {
		if err == nil {
			err = errors.New("the session to copy changed while it was opened")
		}
		return "", err
	}
	header, ok := parseSessionHeader(in)
	if !ok {
		return "", errors.New("the session to copy has no header pi can read")
	}
	if _, err := in.Seek(header.end, io.SeekStart); err != nil {
		return "", err
	}
	// pi records the working directory as the process sees it.
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	id, err := uuidv7()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	line, err := json.Marshal(struct {
		Type          string          `json:"type"`
		Version       json.RawMessage `json:"version,omitempty"`
		ID            string          `json:"id"`
		Timestamp     string          `json:"timestamp"`
		Cwd           string          `json:"cwd"`
		ParentSession string          `json:"parentSession"`
	}{"session", header.version, id, now, filepath.Clean(cwd), src})
	if err != nil {
		return "", err
	}
	name := strings.NewReplacer(":", "-", ".", "-").Replace(now) + "_" + id + ".jsonl"
	final := filepath.Join(sessions, name)

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	staging := filepath.Join(sessions, ".yesman-fork-"+hex.EncodeToString(nonce[:])+".tmp")
	out, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o600)
	if err != nil {
		return "", err
	}
	linked := false
	defer func() {
		out.Close()
		if !linked {
			os.Remove(staging)
		}
	}()
	if _, err := out.Write(append(line, '\n')); err != nil {
		return "", err
	}
	last, err := copyEntries(ctx, out, in)
	if err != nil {
		return "", err
	}
	if last != '\n' && last != 0 {
		if _, err := out.Write([]byte{'\n'}); err != nil {
			return "", err
		}
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Link(staging, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", err
		}
		// A file system without hard links: rename, which is atomic, once
		// nothing has the name.
		if _, lerr := os.Lstat(final); !errors.Is(lerr, os.ErrNotExist) {
			return "", fmt.Errorf("cannot name the copy: %w", err)
		}
		if err := os.Rename(staging, final); err != nil {
			return "", err
		}
		linked = true
	} else {
		linked = true
		os.Remove(staging)
	}
	syncDir(sessions)
	return final, nil
}

// copyEntries copies in to out a chunk at a time, stopping when ctx ends,
// and returns the last byte copied.
func copyEntries(ctx context.Context, out io.Writer, in io.Reader) (byte, error) {
	buf := make([]byte, 1<<20)
	var last byte
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return 0, err
			}
			written += int64(n)
			last = buf[n-1]
			if forkCopyFault != nil {
				if err := forkCopyFault(written); err != nil {
					return 0, err
				}
			}
		}
		if rerr == io.EOF {
			return last, nil
		}
		if rerr != nil {
			return 0, rerr
		}
	}
}

// uuidv7 returns a version 7 UUID, as pi names sessions.
func uuidv7() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	ms := uint64(time.Now().UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
