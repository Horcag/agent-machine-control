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
		fmt.Println(buildinfo.String("amc"))
		return
	}

	fmt.Fprintln(os.Stderr, "amc: no command selected; use --version while the CLI is being bootstrapped")
	os.Exit(2)
}
