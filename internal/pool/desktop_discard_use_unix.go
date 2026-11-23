//go:build darwin || linux

package pool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func checkDiscardSharing(*os.File, string) error { return nil }

func visitDesktopOpenFiles(ctx context.Context, visit func(string) bool) error {
	if runtime.GOOS == "linux" {
		processes, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		for _, process := range processes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := strconv.Atoi(process.Name()); err != nil || process.Name() == strconv.Itoa(os.Getpid()) {
				continue
			}
			base := filepath.Join("/proc", process.Name())
			for _, field := range []string{"cwd", "exe", "root"} {
				name, err := os.Readlink(filepath.Join(base, field))
				if err == nil && visit(name) {
					return nil
				}
			}
			fds, _ := os.ReadDir(filepath.Join(base, "fd")) // processes outside our permissions are not observable
			for _, fd := range fds {
				if err := ctx.Err(); err != nil {
					return err
				}
				name, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
				if err == nil && visit(name) {
					return nil
				}
			}
		}
		return ctx.Err()
	}
	// Use the macOS system lsof. Query open files for the current user,
	// not +D, which would recursively enumerate the worktree contents.
	return visitLsof(ctx, "/usr/sbin/lsof", visit)
}

func visitLsof(ctx context.Context, executable string, visit func(string) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-nP", "-a", "-u", strconv.Itoa(os.Getuid()), "-F0pfn")
	var diagnostics lsofDiagnostics
	cmd.Stderr = &diagnostics
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	limit := &io.LimitedReader{R: out, N: 32<<20 + 1}
	parseErr := scanLsof(limit, os.Getpid(), visit)
	_ = out.Close()
	waitErr := cmd.Wait()
	if diagnostics.present || limit.N == 0 || parseErr != nil || waitErr != nil || ctx.Err() != nil {
		return fmt.Errorf("could not verify open worktree files: %w", errors.Join(parseErr, waitErr, ctx.Err(), errLsofIncomplete))
	}
	return nil
}

// Only the presence of stderr matters. Do not retain unrelated paths from
// the process listing or buffer unlimited warnings.
type lsofDiagnostics struct{ present bool }

func (d *lsofDiagnostics) Write(p []byte) (int, error) {
	d.present = d.present || len(p) != 0
	return len(p), nil
}

var errLsofIncomplete = errors.New("incomplete process listing")

// -F0pfn emits a process set (p), then file sets (f, n). Each field
// ends in NUL, and each set ends in a newline. Names can contain newlines.
func scanLsof(input io.Reader, self int, visit func(string) bool) error {
	r := bufio.NewScanner(input)
	r.Split(splitLsofField)
	pid := 0
	pendingFD := ""
	for r.Scan() {
		field := r.Text()
		if field == "\n" && pid > 0 && pendingFD == "" {
			return nil
		}
		boundary := strings.HasPrefix(field, "\n")
		if boundary {
			field = field[1:]
		}
		if field == "" {
			return errLsofIncomplete
		}
		switch field[0] {
		case 'p':
			if boundary != (pid != 0) || pendingFD != "" {
				return errLsofIncomplete
			}
			n, err := strconv.Atoi(field[1:])
			if err != nil || n <= 0 || field[1] < '0' || field[1] > '9' {
				return errLsofIncomplete
			}
			pid = n
		case 'f':
			fd := strings.TrimSpace(field[1:])
			if pid == 0 || !boundary || pendingFD != "" || !validLsofFD(fd) {
				return errLsofIncomplete
			}
			pendingFD = fd
		case 'n':
			if boundary || pendingFD == "" {
				return errLsofIncomplete
			}
			// macOS emits an empty n field for some resources, such as
			// unnamed pipes. A missing field or an unnamed cwd is invalid.
			if len(field) == 1 && (pendingFD == "cwd" || pendingFD == "twd" || pendingFD == "rtd" || pendingFD == "txt") {
				return errLsofIncomplete
			}
			pendingFD = ""
			if pid != self && len(field) > 1 {
				visit(field[1:])
			}
		default:
			return errLsofIncomplete
		}
	}
	if err := r.Err(); err != nil {
		return err
	}
	return errLsofIncomplete
}

func validLsofFD(fd string) bool {
	// The macOS dialect uses these labels for nonnumeric file records.
	// Error records (err and NOFD) do not verify that files are unused.
	switch fd {
	case "cwd", "twd", "rtd", "txt", "mem", "mmap":
		return true
	}
	if fd == "" {
		return false
	}
	for _, digit := range fd {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func splitLsofField(data []byte, atEOF bool) (int, []byte, error) {
	if end := bytes.IndexByte(data, 0); end >= 0 {
		if end == 0 || end == 1 && data[0] == '\n' {
			return 0, nil, errLsofIncomplete
		}
		return end + 1, data[:end], nil
	}
	if atEOF {
		if bytes.Equal(data, []byte{'\n'}) {
			return 1, data, bufio.ErrFinalToken
		}
		return 0, nil, errLsofIncomplete
	}
	return 0, nil, nil
}
