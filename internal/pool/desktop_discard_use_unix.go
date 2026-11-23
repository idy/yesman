//go:build darwin || linux

package pool

import (
	"bufio"
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

func checkDesktopWorktreeUse(ctx context.Context, path string) error {
	root := realPath(path)
	matches := func(name string) bool {
		name = strings.TrimSuffix(name, " (deleted)")
		return filepath.IsAbs(name) && (name == root || within(name, root))
	}
	inUse := &RemovalHold{Reason: "A process is using this worktree.", Detail: path}
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
				if err == nil && matches(name) {
					return inUse
				}
			}
			fds, _ := os.ReadDir(filepath.Join(base, "fd")) // processes outside our permissions are not observable
			for _, fd := range fds {
				if err := ctx.Err(); err != nil {
					return err
				}
				name, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
				if err == nil && matches(name) {
					return inUse
				}
			}
		}
		return nil
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
	reader := bufio.NewScanner(io.LimitReader(out, 32<<20))
	reader.Split(splitOpenFiles)
	self, observed, held := "p"+strconv.Itoa(os.Getpid()), false, false
	pid := ""
	for reader.Scan() {
		field := strings.TrimLeft(reader.Text(), "\n")
		if strings.HasPrefix(field, "p") {
			pid = field
			observed = true
		}
		if name, ok := strings.CutPrefix(field, "n"); ok && pid != self && matches(name) {
			held = true
			break
		}
	}
	_ = out.Close()
	waitErr := cmd.Wait()
	if held {
		return inUse
	}
	if !observed || reader.Err() != nil || waitErr != nil || ctx.Err() != nil {
		return fmt.Errorf("could not verify open worktree files: %w", errors.Join(reader.Err(), waitErr, ctx.Err(), errors.New("incomplete process listing")))
	}
	return nil
}

func splitOpenFiles(data []byte, atEOF bool) (int, []byte, error) {
	if _, field, ok := strings.Cut(string(data), "\x00"); ok {
		length := len(data) - len(field) - 1
		return length + 1, data[:length], nil
	}
	if atEOF && strings.TrimSpace(string(data)) != "" {
		return 0, nil, errors.New("incomplete process entry")
	}
	return 0, nil, nil
}
