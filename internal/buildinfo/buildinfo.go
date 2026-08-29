// Package buildinfo exposes version metadata injected by release builds.
package buildinfo

import "fmt"

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// String returns stable, human-readable build metadata.
func String(name string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", name, version, commit, date)
}
