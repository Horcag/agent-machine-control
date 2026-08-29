package main

import (
	"os"

	"github.com/Horcag/agent-machine-control/internal/daemoncli"
)

func main() {
	os.Exit(daemoncli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
