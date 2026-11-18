// Command mygo preserves the repository's existing go tool mygo interface.
// Distribution builds use the naming adapter; other commands use pinned MyGo.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	args := []string{"run", "github.com/egoist/mygo/cmd/mygo"}
	if len(os.Args) > 1 && os.Args[1] == "build" {
		args = append([]string{"run", "./tools/package"}, os.Args[2:]...)
	} else {
		args = append(args, os.Args[1:]...)
	}
	cmd := exec.Command("go", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "mygo:", err)
		os.Exit(1)
	}
}
