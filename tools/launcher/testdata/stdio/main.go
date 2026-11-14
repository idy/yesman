package main

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	os.Stdout.Write(input)
	os.Stderr.Write([]byte("stderr: Unicode 中文; quotes \"; slash \\\n"))
	cwd, _ := os.Getwd()
	b, _ := json.Marshal(struct {
		Args     []string
		Cwd, Env string
		PID      int
	}{os.Args[1:], cwd, os.Getenv("YESMAN_CLI_SENTINEL"), os.Getpid()})
	if path := os.Getenv("YESMAN_CLI_RECORD"); path != "" {
		if err := os.WriteFile(path, b, 0600); err != nil {
			panic(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	os.Exit(37)
}
