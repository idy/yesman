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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-w", "-nP", "-a", "-u", strconv.Itoa(os.Getuid()), "-F0pn")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	limit := &io.LimitedReader{R: out, N: 32<<20 + 1}
	reader := bufio.NewScanner(limit)
	reader.Split(splitOpenFiles)
	self, observed, held := "p"+strconv.Itoa(os.Getpid()), false, false
	pid := ""
	for reader.Scan() {
		field := strings.TrimLeft(reader.Text(), "\n")
		if strings.HasPrefix(field, "p") {
			pid = field
			observed = true
		}
		if name, ok := strings.CutPrefix(field, "n"); ok && pid != "" && pid != self && visit(name) {
			held = true
			break
		}
	}
	_ = out.Close()
	waitErr := cmd.Wait()
	if held {
		return nil
	}
	if !observed || limit.N == 0 || reader.Err() != nil || waitErr != nil || ctx.Err() != nil {
		return fmt.Errorf("could not verify open worktree files: %w", errors.Join(reader.Err(), waitErr, ctx.Err(), errors.New("incomplete process listing")))
	}
	return nil
}

func splitOpenFiles(data []byte, atEOF bool) (int, []byte, error) {
	if end := bytes.IndexByte(data, 0); end >= 0 {
		return end + 1, data[:end], nil
	}
	if atEOF && len(bytes.TrimSpace(data)) != 0 {
		return 0, nil, errors.New("incomplete process entry")
	}
	return 0, nil, nil
}
