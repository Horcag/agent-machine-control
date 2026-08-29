package cli

import (
	"errors"
	"strings"
)

// NormalizedCLI holds the extracted global flags and remaining command arguments.
type NormalizedCLI struct {
	Direct      bool
	StateDir    string
	JSON        bool
	CommandArgs []string
}

// NormalizeGlobalFlags extracts documented global flags (--direct, --state-dir, --json)
// from raw command line arguments. It preserves command/subcommand tokens and handler-level
// flags while preventing global flag values from being mistaken for command tokens.
func NormalizeGlobalFlags(rawArgs []string) (NormalizedCLI, error) {
	var norm NormalizedCLI
	var remaining []string

	for i := 0; i < len(rawArgs); i++ {
		arg := rawArgs[i]
		switch {
		case arg == "--direct" || arg == "-direct":
			norm.Direct = true
		case arg == "--json" || arg == "-json":
			norm.JSON = true
		case arg == "--state-dir" || arg == "-state-dir":
			if i+1 >= len(rawArgs) {
				return norm, errors.New("missing value for --state-dir flag")
			}
			norm.StateDir = rawArgs[i+1]
			i++
		case strings.HasPrefix(arg, "--state-dir="):
			val := strings.TrimPrefix(arg, "--state-dir=")
			if val == "" {
				return norm, errors.New("missing value for --state-dir flag")
			}
			norm.StateDir = val
		case strings.HasPrefix(arg, "-state-dir="):
			val := strings.TrimPrefix(arg, "-state-dir=")
			if val == "" {
				return norm, errors.New("missing value for --state-dir flag")
			}
			norm.StateDir = val
		default:
			remaining = append(remaining, arg)
		}
	}
	norm.CommandArgs = remaining
	return norm, nil
}
