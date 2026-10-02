//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/Horcag/agent-machine-control/internal/winlauncher"
)

func main() {
	code, err := winlauncher.Run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
