package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestConsoleOperationBindsPayloadWithoutRetainingText(t *testing.T) {
	input := ConsoleInput{Kind: "type", Text: "synthetic private input"}
	params := ConsoleInputParameters(input)
	if err := ValidateOperationParameters("console.input", params); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Text += " changed"
	if reflect.DeepEqual(params, ConsoleInputParameters(changed)) || len(params) != 2 {
		t.Fatal("approval fingerprint did not bind payload")
	}
	for _, bad := range []map[string]any{
		{"kind": "type", "input_sha256": params["input_sha256"], "text": input.Text},
		{"kind": "scroll", "input_sha256": params["input_sha256"]},
		{"kind": "type", "input_sha256": 3},
		{"kind": "type", "input_sha256": "short"},
		{"kind": "type", "input_sha256": strings.Repeat("A", 64)},
	} {
		if err := ValidateOperationParameters("console.input", bad); err == nil {
			t.Fatalf("noncanonical parameters accepted: %v", bad)
		}
	}
}
