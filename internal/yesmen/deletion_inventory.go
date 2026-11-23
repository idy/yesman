package yesmen

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/idy/yesman/internal/safefs"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const managedDeletingPrefix = deletingPrefix + "v1-"
const deletionReceiptPrefix = ".approved-deletion-v1-"
const deletionReceiptLimit = 512 << 20
const deletionHeaderLimit = 1 << 20
const deletionPathLimit = 32 << 10

var errDeletionReceipt = deletionIssue("The interrupted deletion's approval record is missing or changed. Preserve the remaining files and Git data.")

type deletionApproval struct {
	Stage string
	// Both ends of an absorbed module are recorded. Admins can outlive
	// gitfiles, and gitfiles can outlive their admins during an interruption.
	Git      map[string]string   // checkout observation path -> Git administration path
	Configs  map[string][32]byte // exact configs checked for external dependencies
	Pointers map[string][32]byte // original absorbed-module gitfiles
}

func (a *deletionApproval) admins() map[string]bool {
	out := make(map[string]bool, len(a.Git))
	for _, admin := range a.Git {
		out[admin] = true
	}
	return out
}

func receiptName(stage string) string {
	return deletionReceiptPrefix + strings.TrimPrefix(stage, managedDeletingPrefix)
}

// The manifest is a sorted, preorder stream of path/fingerprint pairs. Only
// one record and the walk's current directory batches are needed to compare
// it, even when the interrupted purge left millions of paths. Absent records
// in the current tree represent completed work. New or changed records do not.
type deletionInventory struct {
	io.Writer // optional buffered, private spool
	sum       hash.Hash
	buf       []byte
	size      int64
}

func newDeletionInventory(w io.Writer) *deletionInventory {
	return &deletionInventory{Writer: w, sum: sha256.New()}
}

func (i *deletionInventory) add(rel string, fingerprint [32]byte) error {
	if len(rel) > deletionPathLimit {
		return errCheckoutPreviewChanged
	}
	i.buf = binary.LittleEndian.AppendUint16(i.buf[:0], uint16(len(rel)))
	i.buf = append(i.buf, rel...)
	i.buf = append(i.buf, fingerprint[:]...)
	i.size += int64(len(i.buf))
	if i.size > deletionReceiptLimit {
		return deletionIssue("The workspace deletion inventory is too large. Move some files out before trying again.")
	}
	_, _ = i.sum.Write(i.buf)
	if i.Writer != nil {
		_, err := i.Writer.Write(i.buf)
		return err
	}
	return nil
}

func (i *deletionInventory) digest() [32]byte { return [32]byte(i.sum.Sum(nil)) }

type deletionCursor struct {
	r           *bufio.Reader
	path        string
	fingerprint [32]byte
	buf         []byte
}

func newDeletionCursor(r io.Reader) *deletionCursor {
	return &deletionCursor{r: bufio.NewReaderSize(r, 64<<10)}
}

func (c *deletionCursor) next() error {
	var length [2]byte
	if _, err := io.ReadFull(c.r, length[:]); err != nil {
		return err
	}
	n := int(binary.LittleEndian.Uint16(length[:]))
	if n == 0 || n > deletionPathLimit {
		return errDeletionReceipt
	}
	if cap(c.buf) < n {
		c.buf = make([]byte, n)
	}
	if _, err := io.ReadFull(c.r, c.buf[:n]); err != nil {
		return err
	}
	if _, err := io.ReadFull(c.r, c.fingerprint[:]); err != nil {
		return err
	}
	c.path = string(c.buf[:n])
	return nil
}

func (c *deletionCursor) match(ctx context.Context, rel string, fingerprint [32]byte, subset bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.next(); err != nil {
			if errors.Is(err, io.EOF) {
				return errCheckoutPreviewChanged
			}
			return err
		}
		if c.path == rel && c.fingerprint == fingerprint {
			return nil
		}
		if !subset || compareDeletionPath(c.path, rel) >= 0 {
			return errCheckoutPreviewChanged
		}
	}
}

func compareDeletionPath(a, b string) int {
	for {
		an, at, _ := strings.Cut(a, "/")
		bn, bt, _ := strings.Cut(b, "/")
		if cmp := compareDeletionName(an, bn); cmp != 0 {
			return cmp
		}
		if at == "" || bt == "" {
			return strings.Compare(at, bt)
		}
		a, b = at, bt
	}
}

func compareDeletionName(a, b string) int {
	// Keep Git's most recognizable markers until after neighboring data.
	late := func(name string) bool {
		switch strings.ToLower(name) {
		case ".git", "head", "objects":
			return true
		}
		return false
	}
	x, y := late(a), late(b)
	if x != y {
		if x {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// Created only while committing an explicitly confirmed deletion. The file
// is outside the workspace and survives both its rename and a partial purge.
// The format binds the complete inventory to the exact staged name, which in
// turn binds the original name, parent and workspace birth/inode identities.
func openDeletionSpool(parent *os.Root, instance, kind string) (*os.File, string, error) {
	// One spool of each kind per workspace. The deletion lifecycle lock
	// serializes their use, so repeated interrupted attempts cannot pile up
	// unbounded temporary inventories. No spool grants recovery authority.
	name := ".writing-deletion-v1-" + instance + "-" + kind
	st, err := parent.Lstat(name)
	if err == nil {
		if !st.Mode().IsRegular() {
			return nil, "", errDeletionReceipt
		}
		if err := parent.Remove(name); err != nil {
			return nil, "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	f, err := parent.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	return f, name, err
}

func writeDeletionHeader(w io.Writer, a deletionApproval) error {
	data, err := json.Marshal(a)
	if err != nil || len(data) > deletionHeaderLimit {
		return errDeletionReceipt
	}
	if _, err := w.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(data)))); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

const deletionKeyName = ".workspace-deletion-key-v1"

// The key is in the Store parent, outside agent workspaces. The receipt
// authenticates the persisted scope against corruption and fabricated workspace
// metadata. As elsewhere in the Store, programs with direct access to the host
// profile are within its trust boundary.
func deletionReceiptMAC(parent *os.Root, create bool) (hash.Hash, error) {
	key, _, err := readRegularEntry(parent, deletionKeyName, 32, nil, true)
	if create && errors.Is(err, fs.ErrNotExist) {
		var seed [32]byte
		rand.Read(seed[:])
		if err := publishRelocationReceipt(parent, deletionKeyName, seed[:]); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		key, _, err = readRegularEntry(parent, deletionKeyName, 32, nil, true)
	}
	if err != nil || len(key) != 32 {
		return nil, errDeletionReceipt
	}
	return hmac.New(sha256.New, key), nil
}

func readDeletionApproval(ctx context.Context, parent *os.Root, stage string) (*os.File, *deletionApproval, [32]byte, int64, error) {
	var digest [32]byte
	name := receiptName(stage)
	st, err := parent.Lstat(name)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	f, err := parent.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	opened, err := f.Stat()
	if err != nil || !sameDeletionEntry(st, opened) || st.Size() > deletionReceiptLimit+deletionHeaderLimit+36 {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	n, err := linkCount(f, opened)
	if err != nil || n != 1 {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	var size [4]byte
	if _, err := io.ReadFull(f, size[:]); err != nil || binary.LittleEndian.Uint32(size[:]) > deletionHeaderLimit {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	header := make([]byte, binary.LittleEndian.Uint32(size[:]))
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	var approval deletionApproval
	if err := json.Unmarshal(header, &approval); err != nil || approval.Stage != stage || len(approval.Git) == 0 || len(approval.Git) > checkoutResultLimit {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	if len(approval.Configs) != len(approval.admins()) {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	for rel, admin := range approval.Git {
		if !validDeletionPath(rel) || !validDeletionPath(admin) || approval.Configs[admin] == ([32]byte{}) {
			return nil, nil, digest, 0, errDeletionReceipt
		}
	}
	for rel, pointer := range approval.Pointers {
		if admin := approval.Git[rel]; admin == "" || admin == rel || pointer == ([32]byte{}) {
			return nil, nil, digest, 0, errDeletionReceipt
		}
	}
	offset := int64(len(header) + len(size))
	bodySize := st.Size() - offset - sha256.Size
	if bodySize < 0 || bodySize > deletionReceiptLimit {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	h, err := deletionReceiptMAC(parent, false)
	if err != nil {
		return nil, nil, digest, 0, err
	}
	h.Write(size[:])
	h.Write(header)
	buf := make([]byte, 64<<10)
	left := bodySize
	for left > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, digest, 0, err
		}
		n, err := io.ReadFull(f, buf[:min(int64(len(buf)), left)])
		if err != nil {
			return nil, nil, digest, 0, errDeletionReceipt
		}
		h.Write(buf[:n])
		left -= int64(n)
	}
	if _, err := io.ReadFull(f, digest[:]); err != nil || !hmac.Equal(h.Sum(nil), digest[:]) {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	now, err := parent.Lstat(name)
	if err != nil || !sameDeletionEntry(st, now) {
		return nil, nil, digest, 0, errDeletionReceipt
	}
	keep = true
	return f, &approval, digest, bodySize, nil
}

func (f *ForceDeletion) openApproval(ctx context.Context, parent *os.Root, stage string) (*os.File, *deletionCursor, error) {
	receipt, _, digest, size, err := readDeletionApproval(ctx, parent, stage)
	if err != nil {
		return nil, nil, err
	}
	if digest != f.approvalDigest {
		receipt.Close()
		return nil, nil, errDeletionReceipt
	}
	cursor, err := approvalCursor(receipt, size)
	if err != nil {
		receipt.Close()
		return nil, nil, err
	}
	return receipt, cursor, nil
}

func validDeletionPath(rel string) bool {
	if rel == "." {
		return true
	}
	return fs.ValidPath(rel) && filepath.IsLocal(filepath.FromSlash(rel))
}

func approvalCursor(f *os.File, bodySize int64) (*deletionCursor, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := st.Size() - bodySize - sha256.Size
	if offset < 4 || offset > deletionHeaderLimit+4 {
		return nil, errDeletionReceipt
	}
	return newDeletionCursor(io.NewSectionReader(f, offset, bodySize)), nil
}

func deletionFingerprint(dir *os.Root, handle *os.File, name string, st fs.FileInfo, volume safefs.Identity, gitData bool) ([32]byte, error) {
	id, err := deletionEntryID(handle, name, st, volume)
	if err != nil {
		return [32]byte{}, err
	}
	bytes := binary.LittleEndian.AppendUint64(nil, id.Ino)
	bytes = binary.LittleEndian.AppendUint64(bytes, uint64(id.Birth))
	bytes = binary.LittleEndian.AppendUint32(bytes, uint32(st.Mode().Type()))
	if !st.IsDir() {
		bytes = binary.LittleEndian.AppendUint64(bytes, uint64(st.Size()))
		bytes = binary.LittleEndian.AppendUint64(bytes, uint64(st.ModTime().UnixNano()))
	}
	if st.Mode().Type() == fs.ModeSymlink {
		link, err := dir.Readlink(name)
		if err != nil {
			return [32]byte{}, err
		}
		bytes = append(bytes, link...)
	} else if gitData && st.Mode().IsRegular() {
		limit := int64(gitDeletionFileLimit)
		if strings.EqualFold(name, "config") {
			limit = 64 << 10
		}
		data, now, err := readRegularEntry(dir, name, limit, nil, false)
		if err != nil || !sameDeletionEntry(st, now) {
			return [32]byte{}, fmt.Errorf("cannot verify Git entry %s: %w", name, errCheckoutPreviewChanged)
		}
		bytes = append(bytes, data...)
	}
	return sha256.Sum256(bytes), nil
}

func recoverableDeletionName(entry string) bool {
	return strings.HasPrefix(entry, managedDeletingPrefix)
}

// Seal a receipt while the live workspace and every approved Git relationship
// are still present. A temp file never grants recovery authority.
func (s *Store) buildDeletionApproval(ctx context.Context, parent, ws *os.Root, stage string, force *ForceDeletion) (string, [32]byte, error) {
	var digest [32]byte
	f, name, err := openDeletionSpool(parent, force.target.Instance, "approval")
	if err != nil {
		return "", digest, err
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
			parent.Remove(name)
		}
	}()
	h, err := deletionReceiptMAC(parent, true)
	if err != nil {
		return "", digest, err
	}
	out := bufio.NewWriterSize(io.MultiWriter(f, h), 64<<10)
	approval := deletionApproval{Stage: stage, Git: force.approvedGit(), Configs: force.approvedGitConfigs(), Pointers: force.approvedModulePointers()}
	if err = writeDeletionHeader(out, approval); err != nil {
		return "", digest, err
	}
	inventory := newDeletionInventory(out)
	if err = walkDeletion(ctx, ws, inspectDeletion, force, deletionCoverage{output: inventory}); err != nil {
		return "", digest, err
	}
	if err = out.Flush(); err != nil {
		return "", digest, err
	}
	digest = [32]byte(h.Sum(nil))
	if _, err = f.Write(digest[:]); err != nil {
		return "", digest, err
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return "", digest, err
	}
	keep = true
	return name, digest, nil
}
