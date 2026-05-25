// Package main is the Lumos CLI entrypoint.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dsetiawan230294/lumos/cmd/lumos/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	err := cli.Execute(version)
	if err == nil {
		return
	}
	// Exit codes: 0 = pass, 1 = regression, 2 = any other error.
	if errors.Is(err, cli.ErrRegression) {
		fmt.Fprintln(os.Stderr, "lumos:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "lumos:", err)
	os.Exit(2)
}
