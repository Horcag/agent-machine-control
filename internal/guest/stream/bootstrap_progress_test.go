package stream

import (
	"fmt"
	"strings"
	"testing"

	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func TestStartupMarkersUseStderrBeforeEachClosedPhase(t *testing.T) {
	previous := -1
	for i, row := range []struct{ stage, anchor string }{
		{"before_header_read", "$h=New-Object byte[] 4"},
		{"before_source_read", "$b=New-Object byte[] $l"},
		{"before_compile", "Add-Type -TypeDefinition"},
		{"before_worker_run", "[AMCStream.Worker]::Run"},
	} {
		marker := fmt.Sprintf("[Console]::Error.WriteLine('%s%d:%s');", guestssh.StartupProgressPrefix, i+1, row.stage)
		at := strings.Index(StartupCommand, marker)
		if !guestssh.ValidStartupStage(row.stage) || strings.Count(StartupCommand, marker) != 1 || at <= previous || at >= strings.Index(StartupCommand, row.anchor) {
			t.Fatal("missing/misordered marker", row.stage)
		}
		previous = at
	}
	if strings.Count(StartupCommand, "[Console]::Error.WriteLine(") != 4 || !strings.Contains(StartupCommand, "[Console]::OpenStandardOutput()") {
		t.Fatal("startup stream contract changed")
	}
}
