package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Horcag/agent-machine-control/internal/buildinfo"
)

func main() {
	version := flag.Bool("version", false, "print version information")
	flag.Parse()

	if *version {
		fmt.Println(buildinfo.String("amcd"))
		return
	}

	fmt.Fprintln(os.Stderr, "amcd: daemon runtime is not implemented yet")
	os.Exit(2)
}
