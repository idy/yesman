// github-app is a local setup and identity-verification helper. Credentials stay
// in its Go process and dedicated private directory; it never writes gh config.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/idy/yesman/internal/githubapp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "github-app:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("use register, test, or create-pr")
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	dir := flags.String("directory", "", "Dedicated private registration directory outside workspaces")
	repo := flags.String("repo", "idy/yesman", "Single repository authorized for this operation")
	owner := flags.String("owner", "idy", "Personal account that will own the App")
	name := flags.String("name", "yesman by idy", "App display name, editable on GitHub before registration")
	input := flags.String("input", "", "JSON PullInput file for create-pr")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dir == "" || !filepath.IsAbs(*dir) {
		return errors.New("provide an absolute --directory path and no positional arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	switch os.Args[1] {
	case "register":
		// The standalone helper relies on Unix mode enforcement. The desktop
		// integration must use the credential store on Windows before enabling
		// this workflow there; inherited NTFS ACLs are not a 0700 equivalent.
		if runtime.GOOS == "windows" {
			return errors.New("standalone registration is available on macOS and Linux; Windows requires credential-store integration")
		}
		if _, err := os.Lstat(*dir); !errors.Is(err, os.ErrNotExist) {
			return errors.New("registration directory already exists or is inaccessible; it will not be replaced")
		}
		if err := os.MkdirAll(filepath.Dir(*dir), 0700); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		s := githubapp.NewRegistrationServer(*owner, *name, *dir)
		entry, stop, err := s.Start(ctx)
		if err != nil {
			return err
		}
		defer stop()
		fmt.Println("Open this local page within 30 minutes:", entry)
		fmt.Println("GitHub login and any confirmation are completed in the browser. No credential appears in this command's output.")
		select {
		case <-ctx.Done():
			return errors.New("registration stopped or expired")
		case result := <-s.Results():
			if result.Err != nil {
				return result.Err
			}
			if err := json.NewEncoder(os.Stdout).Encode(result.App); err != nil {
				return err
			}
			fmt.Println("Registration saved. Install at", result.App.URL+"/installations/new", "using Only select repositories: idy/yesman.")
			// Keep the success page reachable long enough to follow its install
			// link. Expiry or Ctrl+C stops only this local helper.
			<-ctx.Done()
			return nil
		}
	case "test", "create-pr":
		client, err := githubapp.NewClient(*dir, []string{*repo})
		if err != nil {
			return err
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if os.Args[1] == "test" {
			result, err := client.Test(ctx, *repo)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		if *input == "" {
			return errors.New("create-pr requires --input with a JSON PR request")
		}
		f, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer f.Close()
		var in githubapp.PullInput
		decoder := json.NewDecoder(io.LimitReader(f, 70000))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			return errors.New("invalid PR input JSON")
		}
		if in.Repository != *repo {
			return errors.New("PR input repository must equal --repo")
		}
		result, err := client.CreatePull(ctx, in)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	default:
		return errors.New("use register, test, or create-pr")
	}
}
