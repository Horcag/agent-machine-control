package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func runDoctor(ctx context.Context, service *app.DiscoveryService, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")

	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}

	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "amc doctor: unexpected argument %q\n", flags.Arg(0))
		return ExitUsage
	}

	report, err := service.Doctor(ctx)
	if err != nil {
		return mapCLIError(err, stderr, "doctor")
	}

	if *jsonOutput {
		caps := report.Capabilities.Slice()
		if caps == nil {
			caps = []string{}
		}
		envelope := DoctorOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Status:        report.Status,
			Ready:         report.Ready,
			Reason:        report.Reason,
			Message:       report.Message,
			Capabilities:  caps,
			ObservedAt:    report.ObservedAt.UTC().Format(time.RFC3339),
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc doctor: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	if report.Ready {
		fmt.Fprintf(stdout, "Status: ready\n")
		fmt.Fprintf(stdout, "Hyper-V Module: available\n")
		fmt.Fprintf(stdout, "Capabilities: %s\n", formatCapabilities(report.Capabilities))
	} else {
		fmt.Fprintf(stdout, "Status: unavailable\n")
		if report.Reason != "" {
			fmt.Fprintf(stdout, "Reason: %s\n", report.Reason)
		}
		if report.Message != "" {
			fmt.Fprintf(stdout, "Message: %s\n", report.Message)
		}
		fmt.Fprintf(stdout, "Capabilities: none\n")
	}

	return ExitSuccess
}
