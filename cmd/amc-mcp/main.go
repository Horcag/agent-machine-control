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
		fmt.Println(buildinfo.String("amc-mcp"))
		return
	}

	fmt.Fprintln(os.Stderr, "amc-mcp: MCP adapter is not implemented yet")
	os.Exit(2)
}
