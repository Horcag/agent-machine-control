// Package entrypoint provides the shared bootstrap contract for project binaries.
package entrypoint

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Horcag/agent-machine-control/internal/buildinfo"
)

// Config describes one executable while keeping its bootstrap behavior shared.
type Config struct {
	Name               string
	UnavailableMessage string
}

// Run parses common flags, writes process output, and returns the desired exit code.
func Run(config Config, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(config.Name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.Bool("version", false, "print version information")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *version {
		fmt.Fprintln(stdout, buildinfo.String(config.Name))
		return 0
	}

	fmt.Fprintln(stderr, config.UnavailableMessage)
	return 2
}
