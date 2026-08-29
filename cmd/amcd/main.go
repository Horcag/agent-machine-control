package main

import (
	"os"

	"github.com/Horcag/agent-machine-control/internal/entrypoint"
)

func main() {
	os.Exit(entrypoint.Run(entrypoint.Config{
		Name:               "amcd",
		UnavailableMessage: "amcd: daemon runtime is not implemented yet",
	}, os.Args[1:], os.Stdout, os.Stderr))
}
