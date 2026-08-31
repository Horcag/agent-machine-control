package hyperv

import (
	"slices"
	"strings"
	"testing"
)

func TestCommandEnvironmentBridgesExplicitVariablesThroughWSL(t *testing.T) {
	base := []string{"PATH=/usr/bin", "WSLENV=EXISTING/u:AMC_TARGET_VM_ID/p"}
	explicit := []string{
		"AMC_TARGET_VM_ID=vm-1",
		"AMC_STOP_MODE=shutdown",
		"AMC_STOP_MODE=shutdown",
	}

	got := commandEnvironment(base, explicit, true)
	if value := environmentValue(got, "AMC_TARGET_VM_ID"); value != "vm-1" {
		t.Fatalf("target value = %q, want vm-1", value)
	}
	if value := environmentValue(got, "AMC_STOP_MODE"); value != "shutdown" {
		t.Fatalf("stop mode = %q, want shutdown", value)
	}

	entries := strings.Split(environmentValue(got, "WSLENV"), ":")
	want := []string{"EXISTING/u", "AMC_TARGET_VM_ID/p", "AMC_STOP_MODE"}
	if strings.Join(entries, ":") != strings.Join(want, ":") {
		t.Fatalf("WSLENV entries = %q, want %q", entries, want)
	}
}

func TestCommandEnvironmentCreatesWSLEnvAndIgnoresMalformedNames(t *testing.T) {
	got := commandEnvironment(
		[]string{"PATH=/usr/bin"},
		[]string{
			"AMC_TARGET_VM_ID=vm-1",
			"9INVALID=value",
			"BAD-NAME=value",
			"=empty",
			"missing-equals",
		},
		true,
	)

	if value := environmentValue(got, "WSLENV"); value != "AMC_TARGET_VM_ID" {
		t.Fatalf("WSLENV = %q, want AMC_TARGET_VM_ID", value)
	}
}

func TestCommandEnvironmentDoesNotCreateWSLEnvOutsideInterop(t *testing.T) {
	got := commandEnvironment(
		[]string{"PATH=/usr/bin"},
		[]string{"AMC_TARGET_VM_ID=vm-1"},
		false,
	)

	if value := environmentValue(got, "WSLENV"); value != "" {
		t.Fatalf("WSLENV = %q, want empty outside WSL", value)
	}
	if value := environmentValue(got, "AMC_TARGET_VM_ID"); value != "vm-1" {
		t.Fatalf("target value = %q, want vm-1", value)
	}
}

func TestCommandEnvironmentUsesLastExplicitValue(t *testing.T) {
	got := commandEnvironment(
		[]string{"AMC_TARGET_VM_ID=old", "WSLENV=OTHER"},
		[]string{"AMC_TARGET_VM_ID=new"},
		true,
	)

	if value := environmentValue(got, "AMC_TARGET_VM_ID"); value != "new" {
		t.Fatalf("target value = %q, want new", value)
	}
	if count := environmentKeyCount(got, "AMC_TARGET_VM_ID"); count != 1 {
		t.Fatalf("target entry count = %d, want 1", count)
	}
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

func environmentKeyCount(env []string, name string) int {
	prefix := name + "="
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}
