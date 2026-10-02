package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestConsoleApprovalHashesInputAndRejectsUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"valid", `{"kind":"type","text":"synthetic-test-input"}`, true},
		{"unknown", `{"kind":"type","text":"synthetic-test-input","actor":"operator:any"}`, false},
		{"trailing", `{"kind":"key","key":"enter"}{}`, false},
		{"invalid-key", `{"kind":"key","key":"arbitrary"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(tc.payload), 0600); err != nil {
				t.Fatal(err)
			}
			req := daemon.OperationApprovalIssueRequest{Kind: "console.input"}
			err := populateApprovalRequest(&req, []string{"console.input", "default"}, "", "", path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && (len(req.Parameters) != 2 || req.Parameters["kind"] != "type" || len(req.Parameters["input_sha256"].(string)) != 64) {
				t.Fatal("redacted exact input binding missing")
			}
		})
	}
}
