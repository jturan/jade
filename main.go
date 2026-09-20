// Command jade orchestrates AI coding agents across the discovery → plan → build
// pipeline, using GitHub as the source of truth and herdr as the execution surface.
package main

import (
	"context"
	"os"

	"github.com/jturan/jade/internal/cli"
)

func main() {
	if err := cli.Run(context.Background(), os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
