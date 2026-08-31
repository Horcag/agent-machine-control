package wslruntime

import (
	"strings"
	"testing"
)

func TestForwardNamesViaWSLEnv(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		env        []string
		names      []string
		wantWSLEnv string
	}{
		{
			name:       "empty WSLENV",
			env:        []string{"PATH=/usr/bin"},
			names:      []string{"AMC_BOOTSTRAP_ACTION", "AMC_BOOTSTRAP_SPEC_B64"},
			wantWSLEnv: "AMC_BOOTSTRAP_ACTION:AMC_BOOTSTRAP_SPEC_B64",
		},
		{
			name:       "preserves flags and deduplicates names",
			env:        []string{"WSLENV=EXISTING/u:AMC_BOOTSTRAP_SPEC_B64/p:EXISTING/w"},
			names:      []string{"AMC_BOOTSTRAP_ACTION", "AMC_BOOTSTRAP_SPEC_B64", "AMC_BOOTSTRAP_METADATA_B64", "AMC_BOOTSTRAP_METADATA_B64"},
			wantWSLEnv: "EXISTING/u:AMC_BOOTSTRAP_SPEC_B64/p:AMC_BOOTSTRAP_ACTION:AMC_BOOTSTRAP_METADATA_B64",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ForwardNamesViaWSLEnv(tc.env, tc.names)
			if value := environmentValue(got, "WSLENV"); value != tc.wantWSLEnv {
				t.Fatalf("WSLENV = %q, want %q", value, tc.wantWSLEnv)
			}
			if strings.Count(strings.Join(got, ":"), "WSLENV=") != 1 {
				t.Fatalf("environment contains duplicate WSLENV entries: %q", got)
			}
		})
	}
}
