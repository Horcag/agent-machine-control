package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Horcag/agent-machine-control/internal/buildinfo"
	"github.com/Horcag/agent-machine-control/internal/mcpadapter"
)

func main() {
	flags := flag.NewFlagSet("amc-mcp", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	listen := flags.String("listen", "", "Run in streamable HTTP mode, listening on this loopback address (e.g. 127.0.0.1:8080)")
	stateDir := flags.String("state-dir", "", "Path to the state directory")
	versionFlag := flags.Bool("version", false, "Print version information and exit")

	var helpFlag bool
	flags.BoolVar(&helpFlag, "help", false, "Print usage instructions")
	flags.BoolVar(&helpFlag, "h", false, "Print usage instructions")

	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if helpFlag {
		printUsage()
		os.Exit(0)
	}

	if *versionFlag {
		fmt.Fprintln(os.Stdout, buildinfo.String("amc-mcp"))
		os.Exit(0)
	}

	code := mcpadapter.Run(*stateDir, *listen, os.Stdout, os.Stderr)
	os.Exit(code)
}

func printUsage() {
	fmt.Fprintf(os.Stdout, "Usage of amc-mcp:\n")
	fmt.Fprintf(os.Stdout, "  --listen string\n")
	fmt.Fprintf(os.Stdout, "    \tRun in streamable HTTP mode, listening on this loopback address (e.g. 127.0.0.1:8080)\n")
	fmt.Fprintf(os.Stdout, "  --state-dir string\n")
	fmt.Fprintf(os.Stdout, "    \tPath to the state directory\n")
	fmt.Fprintf(os.Stdout, "  --version\n")
	fmt.Fprintf(os.Stdout, "    \tPrint version information and exit\n")
	fmt.Fprintf(os.Stdout, "  --help\n")
	fmt.Fprintf(os.Stdout, "    \tPrint usage instructions\n")
}
