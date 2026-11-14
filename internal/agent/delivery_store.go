package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Only bounded metadata is retained, never original image/text payloads. New
// IDs start with a 12-hex millisecond timestamp. A persisted eviction watermark
// makes an unknown old ID fail closed after compaction instead of resending it.
const (
	maxDeliveryRecords    = 256
	maxPendingDeliveries  = 64
	maxDeliveryPreview    = 512
	maxDeliveryError      = 512
	maxDeliveryFile       = 2 << 20
	maxLegacyDeliveryFile = 32 << 20
	deliveryRetention     = 24 * time.Hour
	deliveryClockSkew     = 5 * time.Minute
)

type storedDelivery struct {
	Delivery
	Fingerprint string `json:"fingerprint"`
	Created     int64  `json:"created"`
	Expires     int64  `json:"expires"`
}
type deliveryDisk struct {
	Version int              `json:"version"`
	Floor   int64            `json:"floor"`
	Records []storedDelivery `json:"records"`
}
type deliveryWrite struct {
	revision uint64
	state    deliveryDisk
}

// One worker per agent. The mailbox holds only the latest metadata snapshot;
// neither a slow disk nor serialization can block the manager mutex/pi reader.
// A submitter waits outside those paths for initial registration durability.
type deliveryWriter struct {
	mu                   sync.Mutex
	path                 string
	write                func(string, []byte) error
	latest               *deliveryWrite
	requested, completed uint64
	durable              uint64
	err                  error
	changed              chan struct{}
	wake                 chan struct{}
	closed               bool // sealed: no new snapshots; the worker drains latest before done
	done                 chan struct{}
}

func newDeliveryWriter(path string, write func(string, []byte) error) *deliveryWriter {
	if write == nil {
		write = writeDeliveryFile
	}
	w := &deliveryWriter{path: path, write: write, changed: make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}
	go w.run()
	return w
}
func (w *deliveryWriter) request(revision uint64, state deliveryDisk) {
	w.mu.Lock()
	if !w.closed {
		w.latest = &deliveryWrite{revision, state}
		w.requested = revision
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *deliveryWriter) run() {
	defer close(w.done)
	for {
		<-w.wake
		for {
			w.mu.Lock()
			next := w.latest
			w.latest = nil
			closed := w.closed
			w.mu.Unlock()
			if next == nil {
				if closed {
					return
				}
				break
			}
			data, err := json.Marshal(next.state)
			if err == nil && len(data) > maxDeliveryFile {
				err = errors.New("delivery metadata exceeds its byte limit")
			}
			if err == nil {
				err = w.write(w.path, data)
			}
			w.mu.Lock()
			w.completed, w.err = next.revision, err
			if err == nil {
				w.durable = next.revision
			}
			close(w.changed)
			w.changed = make(chan struct{})
			w.mu.Unlock()
		}
	}
}
func (w *deliveryWriter) wait(ctx context.Context, revision uint64, quit <-chan struct{}) error {
	for {
		w.mu.Lock()
		durable, done, err, closed, changed := w.durable >= revision, w.completed >= revision, w.err, w.closed, w.changed
		w.mu.Unlock()
		if durable {
			return nil
		}
		if done {
			return err
		}
		if closed {
			select {
			case <-w.done:
				if err != nil {
					return err
				}
				return errStopped
			default:
			}
		}
		select {
		case <-changed:
		case <-w.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-quit:
			return errStopped
		}
	}
}
func (w *deliveryWriter) close(ctx context.Context) error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
func (w *deliveryWriter) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}
func writeDeliveryFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func deliveryFingerprint(text string, images []Image) string {
	h := sha256.New()
	var size [8]byte
	field := func(s string) {
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		h.Write(size[:])
		io.WriteString(h, s)
	}
	field(text)
	binary.BigEndian.PutUint64(size[:], uint64(len(images)))
	h.Write(size[:])
	for _, image := range images {
		field(image.Type)
		field(image.MimeType)
		field(image.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func deliveryClip(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return strings.Clone(text), false
	}
	for !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.Clone(text[:limit]), true
}
func deliveryTimestamp(id string) int64 {
	if !deliveryID.MatchString(id) {
		return 0
	}
	stamp, _ := strconv.ParseInt(id[:12], 16, 64)
	return stamp
}

func (m *Manager) ensureDeliveries(a *agent, path string) error {
	a.deliveryOnce.Do(func() {
		read := m.cfg.deliveryRead
		if read == nil {
			read = readDeliveries
		}
		records, floor, err := read(path, time.Now())
		m.mu.Lock()
		defer m.mu.Unlock()
		a.deliveryErr = err
		if err != nil {
			return
		}
		a.deliveries, a.deliveryFloor = records, floor
		for _, d := range records {
			if d.Order > a.deliveryOrder {
				a.deliveryOrder = d.Order
			}
		}
		a.deliveryWriter = newDeliveryWriter(path, m.cfg.deliveryWrite)
		if len(records) > 0 || floor > 0 {
			m.persistDeliveriesLocked(a)
		}
	})
	return a.deliveryErr
}
func readDeliveries(path string, now time.Time) (map[string]Delivery, int64, error) {
	records := map[string]Delivery{}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return records, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLegacyDeliveryFile+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxLegacyDeliveryFile {
		return nil, 0, errors.New("receipt history exceeds the bounded migration limit; it was left unchanged")
	}
	var disk deliveryDisk
	if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		var legacy []Delivery
		if err = json.Unmarshal(data, &legacy); err != nil {
			return nil, 0, err
		}
		for _, d := range legacy {
			d.fingerprint = deliveryFingerprint(d.Text, d.Images)
			d.created = now.UnixMilli()
			d.ImageCount = len(d.Images)
			d.Images = nil
			disk.Records = append(disk.Records, storedDelivery{d, d.fingerprint, d.created, now.Add(deliveryRetention).UnixMilli()})
		}
	} else {
		if len(data) > maxDeliveryFile {
			return nil, 0, errors.New("delivery metadata exceeds its byte limit")
		}
		if err = json.Unmarshal(data, &disk); err != nil {
			return nil, 0, err
		}
		if disk.Version != 2 {
			return nil, 0, errors.New("unsupported delivery metadata version")
		}
	}
	for _, stored := range disk.Records {
		d := stored.Delivery
		if !deliveryID.MatchString(d.ID) || len(stored.Fingerprint) != 64 || d.ImageCount < 0 || d.ImageCount > 4 {
			return nil, 0, errors.New("invalid delivery metadata")
		}
		switch d.State {
		case "sending", "accepted", "steering", "queued", "unconfirmed", "failed", "interrupted", "handled":
		default:
			return nil, 0, errors.New("invalid delivery state")
		}
		if _, err := hex.DecodeString(stored.Fingerprint); err != nil {
			return nil, 0, errors.New("invalid delivery fingerprint")
		}
		d.fingerprint, d.created, d.expires = stored.Fingerprint, stored.Created, stored.Expires
		if d.expires == 0 {
			d.expires = d.created + deliveryRetention.Milliseconds()
		}
		preview, truncated := deliveryClip(d.Text, maxDeliveryPreview)
		d.Text, d.Preview = preview, d.Preview || truncated
		d.Error, _ = deliveryClip(d.Error, maxDeliveryError)
		d.Session, _ = deliveryClip(d.Session, 160)
		d.Images = nil
		if pendingDelivery(d.State) {
			d.State, d.Error = "interrupted", ""
			d.expires = now.Add(deliveryRetention).UnixMilli()
			d.Revision++
		}
		records[d.ID] = d
	}
	floor := disk.Floor
	pruneDeliveryRecords(records, &floor, now)
	return records, floor, nil
}
func pruneDeliveryRecords(records map[string]Delivery, floor *int64, now time.Time) []string {
	var terminal []Delivery
	for _, d := range records {
		if !pendingDelivery(d.State) {
			terminal = append(terminal, d)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].Order < terminal[j].Order })
	var removed []string
	for _, d := range terminal {
		if len(records) <= maxDeliveryRecords && d.expires >= now.UnixMilli() {
			continue
		}
		delete(records, d.ID)
		removed = append(removed, d.ID)
		// A legacy ID may not contain a valid timestamp. Its unknown retries are
		// still rejected once its record disappears, using at least migration time.
		stamp := deliveryTimestamp(d.ID)
		if stamp > now.Add(deliveryClockSkew).UnixMilli() {
			stamp = d.created
		} // old random IDs have no issued-at timestamp
		if d.created > stamp {
			stamp = d.created
		}
		if stamp > *floor {
			*floor = stamp
		}
	}
	return removed
}
func (m *Manager) pruneDeliveryEventLocked(a *agent) *Event {
	if a.retiring {
		return nil
	}
	removed := m.pruneDeliveriesLocked(a)
	if len(removed) == 0 {
		return nil
	}
	m.persistDeliveriesLocked(a)
	a.seq++
	retained := make([]string, 0, len(a.deliveries))
	for id := range a.deliveries {
		retained = append(retained, id)
	}
	raw, _ := json.Marshal(struct {
		Type     string   `json:"type"`
		Retained []string `json:"retained"`
		Removed  []string `json:"removed"`
	}{"delivery_retained", retained, removed})
	return &Event{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}
}

func (m *Manager) persistDeliveriesLocked(a *agent) uint64 {
	if a.deliverySealed {
		return a.deliveryVersion
	}
	state := deliveryDisk{Version: 2, Floor: a.deliveryFloor, Records: make([]storedDelivery, 0, len(a.deliveries))}
	for _, d := range a.deliveries {
		state.Records = append(state.Records, storedDelivery{d, d.fingerprint, d.created, d.expires})
	}
	a.deliveryVersion++
	a.deliveryWriter.request(a.deliveryVersion, state)
	return a.deliveryVersion
}
func newDeliveryIDAllowed(id string, floor int64, now time.Time) error {
	stamp := deliveryTimestamp(id)
	if stamp <= floor || stamp < now.Add(-deliveryRetention).UnixMilli() {
		return errors.New("this submission ID is no longer retained; it will not be resent")
	}
	if stamp > now.Add(deliveryClockSkew).UnixMilli() {
		return errors.New("invalid submission ID timestamp")
	}
	return nil
}
func (m *Manager) pruneDeliveriesLocked(a *agent) []string {
	return pruneDeliveryRecords(a.deliveries, &a.deliveryFloor, time.Now())
}
