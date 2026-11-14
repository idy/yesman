package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// spec describes how to start one pi process.
type spec struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// response is pi's answer to one RPC command.
type response struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// waiter is a command waiting for its response.
type waiter struct {
	ch chan response
	// at runs on the reader when the response arrives, before any later
	// record is handled.
	at func(response)
}

// errExited is returned for commands to a pi process that has exited.
var errExited = errors.New("the agent's pi process has exited")

// proc is a pi process in RPC mode: JSON records, one per line, on its
// standard input and output.
type proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex

	mu        sync.Mutex
	pending   map[string]waiter
	seq       atomic.Int64
	session   string       // opaque process incarnation; never an authentication token
	piSession atomic.Value // string, from get_state

	done   chan struct{}
	err    error // why it exited, set before done closes
	stderr tail
}

// startProc starts pi. Records that are not answers to commands, the
// session events and extension requests, go to onRecord in order.
func startProc(s spec, onRecord func(p *proc, kind string, raw json.RawMessage)) (*proc, error) {
	cmd := exec.Command(s.Path, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	setProcessGroup(cmd)
	p := &proc{cmd: cmd, pending: map[string]waiter{}, done: make(chan struct{}), session: newToken()}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pi: %w", err)
	}
	p.stdin = stdin
	p.piSession.Store("")
	go p.read(stdout, onRecord)
	return p, nil
}

func (p *proc) read(stdout io.Reader, onRecord func(*proc, string, json.RawMessage)) {
	// RPC records end with LF only; a JSON string may contain U+2028, so
	// split on bytes rather than with a line scanner of text.
	br := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := br.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			p.handle(line, onRecord)
		}
		if err != nil {
			break
		}
	}
	waitErr := p.cmd.Wait()
	p.mu.Lock()
	if waitErr == nil {
		waitErr = errExited
	}
	p.err = waitErr
	for id, w := range p.pending {
		close(w.ch)
		delete(p.pending, id)
	}
	p.mu.Unlock()
	close(p.done)
}

func (p *proc) handle(line []byte, onRecord func(*proc, string, json.RawMessage)) {
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return // not a record: ignore, as stdout carries only records
	}
	if head.Type == "response" && head.ID != "" {
		var r response
		if json.Unmarshal(line, &r) == nil {
			p.mu.Lock()
			w, ok := p.pending[head.ID]
			delete(p.pending, head.ID)
			p.mu.Unlock()
			if ok {
				if w.at != nil {
					w.at(r)
				}
				w.ch <- r
				return
			}
		}
	}
	onRecord(p, head.Type, json.RawMessage(line))
}

func (p *proc) deliverySession() string { return p.session + ":" + p.piSession.Load().(string) }

// call sends a command and waits for its response.
func (p *proc) call(ctx context.Context, cmd map[string]any) (response, error) {
	return p.callAt(ctx, cmd, nil)
}

// callAt is call with at run on the reader as the response arrives, to
// capture state that later records would change.
func (p *proc) callAt(ctx context.Context, cmd map[string]any, at func()) (response, error) {
	return p.callObserved(ctx, cmd, func(response) {
		if at != nil {
			at()
		}
	})
}

// callObserved observes the response on the reader before subsequent events.
func (p *proc) callObserved(ctx context.Context, cmd map[string]any, at func(response)) (response, error) {
	id := "y" + strconv.FormatInt(p.seq.Add(1), 10)
	cmd["id"] = id
	ch := make(chan response, 1)
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return response{}, errExited
	}
	p.pending[id] = waiter{ch: ch, at: at}
	p.mu.Unlock()
	if err := p.send(cmd); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return response{}, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return response{}, errExited
		}
		if !r.Success {
			return r, fmt.Errorf("pi %s: %s", r.Command, r.Error)
		}
		return r, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return response{}, ctx.Err()
	}
}

// send writes one record without waiting for an answer.
func (p *proc) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	select {
	case <-p.done:
		return errExited
	default:
	}
	_, err = p.stdin.Write(append(data, '\n'))
	return err
}

// stop asks pi to shut down by closing its input, and kills it and its
// children when it has not exited after grace.
func (p *proc) stop(grace time.Duration) {
	// Close without wmu and without waiting: a send blocked on a pipe pi no
	// longer reads holds wmu, and the grace and kill below must still run.
	// The kill breaks the pipe, which ends that write and lets Close finish.
	go p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(grace):
		killProcessGroup(p.cmd)
		<-p.done
	}
}

// tail keeps the last bytes written to it, for pi's diagnostics.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 8 << 10

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.buf))
}
