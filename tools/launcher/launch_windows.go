package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

var attachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// cliStreams recognizes real inherited console, pipe, or file handles. NUL
// handles are intentionally not CLI evidence: Explorer/GUI launches and MyGo's
// Windows relaunch path have no console and supply null standard streams.
func cliStreams() (bool, [3]*os.File) {
	ids := [...]uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE}
	names := [...]string{"stdin", "stdout", "stderr"}
	inspect := func() (cli, anyHandle bool, streams [3]*os.File) {
		for i, id := range ids {
			handle, err := windows.GetStdHandle(id)
			if err != nil || handle == 0 || handle == windows.InvalidHandle {
				continue
			}
			kind, err := windows.GetFileType(handle)
			if err != nil {
				continue
			}
			anyHandle = true
			var mode uint32
			if kind == windows.FILE_TYPE_PIPE || kind == windows.FILE_TYPE_DISK || windows.GetConsoleMode(handle, &mode) == nil {
				cli = true
			}
			streams[i] = os.NewFile(uintptr(handle), names[i])
		}
		return
	}
	cli, anyHandle, streams := inspect()
	if !cli && !anyHandle {
		// /SUBSYSTEM:WINDOWS may start without handles even when invoked
		// from a terminal. Attach only to an existing parent console; this
		// public API never allocates a console for an Explorer launch.
		if ok, _, _ := attachConsole.Call(uintptr(0xffffffff)); ok != 0 {
			cli, _, streams = inspect()
		}
	}
	return cli, streams
}

func launch(target string, args []string) error {
	cmd := exec.Command(target, args...) // Go implements Windows argument quoting.
	cli, streams := cliStreams()
	if cli {
		if streams[0] != nil {
			cmd.Stdin = streams[0]
		}
		if streams[1] != nil {
			cmd.Stdout = streams[1]
		}
		if streams[2] != nil {
			cmd.Stderr = streams[2]
		}
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
