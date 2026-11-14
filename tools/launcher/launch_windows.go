package main

import "os/exec"

func launch(target string, args []string) error {
	// os/exec implements Windows command-line quoting (including quotes and
	// trailing backslashes); no shell or manually joined command is involved.
	cmd := exec.Command(target, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
