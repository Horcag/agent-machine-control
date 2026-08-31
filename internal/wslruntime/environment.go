package wslruntime

import (
	"slices"
	"strings"
)

// ForwardNamesViaWSLEnv adds names to WSLENV while preserving existing entries
// and their flags. The first occurrence of each name wins so callers do not
// forward a variable more than once.
func ForwardNamesViaWSLEnv(env, names []string) []string {
	entries := make([]string, 0, len(names)+1)
	seen := make(map[string]struct{}, len(names)+1)
	for entry := range strings.SplitSeq(environmentValue(env, "WSLENV"), ":") {
		if entry == "" {
			continue
		}
		name, _, _ := strings.Cut(entry, "/")
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, entry)
	}
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, name)
	}
	return setEnvironmentValue(env, "WSLENV", strings.Join(entries, ":"))
}

func environmentValue(env []string, name string) string {
	prefix := name + "="
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value
		}
	}
	return ""
}

func setEnvironmentValue(env []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
