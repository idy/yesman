// Prepare exactly the same managed dependency as the application, in a chosen
// isolated cache. This command never modifies a global pi installation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/idy/yesman/internal/piruntime"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/pi-runtime <cache-directory>")
		os.Exit(2)
	}
	runtime, err := piruntime.New(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	node, args, err := runtime.Command(ctx)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"root": runtime.Directory(), "node": node, "args": args, "version": piruntime.Version})
}
