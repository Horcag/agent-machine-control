package main

import (
	"os"

	"github.com/Horcag/agent-machine-control/internal/entrypoint"
)

func main() {
	os.Exit(entrypoint.Run(entrypoint.Config{
		Name:               "amc",
		UnavailableMessage: "amc: no command selected; use --version while the CLI is being bootstrapped",
	}, os.Args[1:], os.Stdout, os.Stderr))
}
