package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func runMachine(
	ctx context.Context,
	discoverySvc *app.DiscoveryService,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	directMode bool,
	args []string,
	stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc machine: missing subcommand (expected 'list', 'inspect', 'start', or 'stop')")
		return ExitUsage
	}

	switch args[0] {
	case "list":
		return runMachineList(ctx, discoverySvc, args[1:], stdout, stderr)
	case "inspect":
		return runMachineInspect(ctx, discoverySvc, args[1:], stdout, stderr)
	case "start":
		return runMachineStart(ctx, recoverySvc, actor, prompter, nowFn, directMode, args[1:], stdout, stderr)
	case "stop":
		return runMachineStop(ctx, recoverySvc, actor, prompter, nowFn, directMode, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage: amc machine <list|inspect|start|stop> [flags] [args]")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amc machine: unknown subcommand %q (expected 'list', 'inspect', 'start', or 'stop')\n", args[0])
		return ExitUsage
	}
}

func runMachineList(ctx context.Context, service *app.DiscoveryService, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("machine list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")

	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "amc machine list: unexpected argument %q\n", flags.Arg(0))
		return ExitUsage
	}

	machines, err := service.List(ctx)
	if err != nil {
		return mapCLIError(err, stderr, "machine list")
	}

	sort.Slice(machines, func(i, j int) bool {
		if machines[i].Name == machines[j].Name {
			return machines[i].ID < machines[j].ID
		}
		return machines[i].Name < machines[j].Name
	})

	if *jsonOutput {
		dtos := make([]MachineOutputDTO, len(machines))
		for i, m := range machines {
			dtos[i] = ConvertToMachineDTO(m)
		}
		envelope := MachineListOutputEnvelope{
			SchemaVersion:   SchemaVersion,
			ObservationType: domain.ObservationObserved,
			Machines:        dtos,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc machine list: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	if len(machines) == 0 {
		fmt.Fprintln(stdout, "No virtual machines found.")
		return ExitSuccess
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATE\tCPU%\tMEMORY\tUPTIME")
	for _, m := range machines {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d%%\t%s\t%s\n",
			m.ID,
			m.Name,
			m.State,
			m.CPUUsagePercent,
			formatMemoryMB(m.MemoryAssignedBytes),
			formatUptime(m.UptimeMs),
		)
	}
	_ = w.Flush()

	return ExitSuccess
}

func runMachineInspect(ctx context.Context, service *app.DiscoveryService, args []string, stdout, stderr io.Writer) int {
	var jsonOutput bool
	var positional []string

	for _, arg := range args {
		switch {
		case arg == "--json" || arg == "-json":
			jsonOutput = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "amc machine inspect: unknown flag %q\n", arg)
			return ExitUsage
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) == 0 {
		fmt.Fprintln(stderr, "amc machine inspect: missing required machine GUID")
		return ExitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "amc machine inspect: unexpected argument %q\n", positional[1])
		return ExitUsage
	}

	targetID := positional[0]
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		fmt.Fprintf(stderr, "amc machine inspect: invalid machine GUID %q\n", targetID)
		return ExitUsage
	}

	m, err := service.Inspect(ctx, targetID)
	if err != nil {
		return mapCLIError(err, stderr, "machine inspect")
	}

	if jsonOutput {
		envelope := MachineInspectOutputEnvelope{
			SchemaVersion:   SchemaVersion,
			ObservationType: domain.ObservationObserved,
			Machine:         ConvertToMachineDTO(m),
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc machine inspect: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	printHumanInspect(stdout, m)
	return ExitSuccess
}

func printHumanInspect(w io.Writer, m domain.MachineObservation) {
	fmt.Fprintf(w, "ID:                    %s\n", m.ID)
	fmt.Fprintf(w, "Name:                  %s\n", m.Name)
	if m.RawStatus != "" {
		fmt.Fprintf(w, "State:                 %s (raw: %s, status: %s)\n", m.State, m.RawState, m.RawStatus)
	} else {
		fmt.Fprintf(w, "State:                 %s (raw: %s)\n", m.State, m.RawState)
	}
	fmt.Fprintf(w, "Generation:            %d\n", m.Generation)
	if m.Version != "" {
		fmt.Fprintf(w, "Version:               %s\n", m.Version)
	}
	fmt.Fprintf(w, "Uptime:                %s\n", formatUptime(m.UptimeMs))
	fmt.Fprintf(w, "CPU Usage:             %d%%\n", m.CPUUsagePercent)
	fmt.Fprintf(w, "Assigned Memory:       %s (%d bytes)\n", formatMemoryMB(m.MemoryAssignedBytes), m.MemoryAssignedBytes)
	printNetworkAdapters(w, m.NetworkAdapters)
	fmt.Fprintf(w, "Capabilities:          %s\n", formatCapabilities(m.Capabilities))
	fmt.Fprintf(w, "Observed At:           %s\n", m.ObservedAt.UTC().Format(time.RFC3339))
}

func printNetworkAdapters(w io.Writer, adapters []domain.NetworkAdapterSummary) {
	if len(adapters) == 0 {
		fmt.Fprintf(w, "Network Adapters:      none\n")
		return
	}
	fmt.Fprintf(w, "Network Adapters:\n")
	for _, na := range adapters {
		fmt.Fprintf(w, "  - Name:              %s\n", na.Name)
		if na.SwitchName != "" {
			fmt.Fprintf(w, "    Switch:            %s\n", na.SwitchName)
		}
		if na.MACAddress != "" {
			fmt.Fprintf(w, "    MAC:               %s\n", na.MACAddress)
		}
		if len(na.IPAddresses) > 0 {
			fmt.Fprintf(w, "    IP Addresses:      %s\n", strings.Join(na.IPAddresses, ", "))
		}
		if na.Status != "" {
			fmt.Fprintf(w, "    Status:            %s\n", na.Status)
		}
	}
}

func mapCLIError(err error, stderr io.Writer, opName string) int {
	if errors.Is(err, hyperv.ErrMachineNotFound) {
		fmt.Fprintf(stderr, "amc %s: machine not found\n", opName)
		return ExitNotFound
	}
	if errors.Is(err, hyperv.ErrInvalidState) {
		fmt.Fprintf(stderr, "amc %s: invalid machine state\n", opName)
		return ExitConflict
	}
	if errors.Is(err, hyperv.ErrCommandTimeout) {
		fmt.Fprintf(stderr, "amc %s: command timed out\n", opName)
		return ExitTimeout
	}
	if errors.Is(err, hyperv.ErrMalformedResponse) ||
		errors.Is(err, hyperv.ErrUnexpectedSchemaVersion) ||
		errors.Is(err, hyperv.ErrTrailingData) ||
		errors.Is(err, hyperv.ErrDuplicateMachineID) ||
		errors.Is(err, hyperv.ErrOutputExceededLimit) {
		fmt.Fprintf(stderr, "amc %s: malformed provider response\n", opName)
		return ExitMalformedProvider
	}
	if errors.Is(err, hyperv.ErrAccessDenied) {
		fmt.Fprintf(stderr, "amc %s: access denied to Hyper-V host\n", opName)
		return ExitBackendUnavailable
	}
	if errors.Is(err, hyperv.ErrModuleMissing) {
		fmt.Fprintf(stderr, "amc %s: Hyper-V PowerShell module is unavailable\n", opName)
		return ExitBackendUnavailable
	}
	if errors.Is(err, hyperv.ErrExecutableNotFound) {
		fmt.Fprintf(stderr, "amc %s: PowerShell executable (powershell.exe) was not found\n", opName)
		return ExitBackendUnavailable
	}
	if errors.Is(err, hyperv.ErrHostUnavailable) || errors.Is(err, hyperv.ErrBackendUnavailable) {
		fmt.Fprintf(stderr, "amc %s: Hyper-V host management is unavailable\n", opName)
		return ExitBackendUnavailable
	}
	fmt.Fprintf(stderr, "amc %s: Hyper-V host management is unavailable\n", opName)
	return ExitBackendUnavailable
}
