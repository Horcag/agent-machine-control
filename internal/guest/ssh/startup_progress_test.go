package ssh

import (
	"strings"
	"sync"
	"testing"
)

func TestStartupProgressFragmentedMarkersIgnoreNoiseAndSpoofs(t *testing.T) {
	var p startupProgress
	input := "private error/path\nspoof AMC_STARTUP_V1:1:before_header_read\nAMC_STARTUP_V1:4:before_worker_run\nAMC_STARTUP_V1:1:before_header_read extra\nAMC_STARTUP_V1:1:before_header_read\r\nAMC_STARTUP_V1:2:before_source_read\n"
	for _, b := range []byte(input) {
		if n, err := p.Write([]byte{b}); n != 1 || err != nil {
			t.Fatal(n, err)
		}
	}
	if got := p.Stage(); got != "before_source_read" {
		t.Fatal(got)
	}
	_, _ = p.Write([]byte("AMC_STARTUP_V1:1:before_header_read\nAMC_STARTUP_V1:4:before_worker_run\nAMC_STARTUP_V1:3:private\n"))
	if got := p.Stage(); got != "before_source_read" {
		t.Fatal("nonmonotonic/unknown accepted", got)
	}
	_, _ = p.Write([]byte("AMC_STARTUP_V1:3:before_compile\nAMC_STARTUP_V1:4:before_worker_run\n"))
	if got := p.Stage(); got != "before_worker_run" {
		t.Fatal(got)
	}
}

func TestStartupProgressOversizedAndUnterminatedLinesDoNotAdvance(t *testing.T) {
	var p startupProgress
	_, _ = p.Write([]byte("AMC_STARTUP_V1:1:before_header_read" + strings.Repeat("x", 65536) + "\n"))
	if p.Stage() != "" {
		t.Fatal("oversized line accepted")
	}
	_, _ = p.Write([]byte("AMC_STARTUP_V1:1:before_header_read"))
	if p.Stage() != "" {
		t.Fatal("unterminated marker accepted")
	}
	_, _ = p.Write([]byte("\n"))
	if p.Stage() != "before_header_read" {
		t.Fatal(p.Stage())
	}
}

func TestStartupProgressConcurrentObservation(t *testing.T) {
	var p startupProgress
	var wg sync.WaitGroup
	wg.Go(func() {
		for _, b := range []byte("AMC_STARTUP_V1:1:before_header_read\nAMC_STARTUP_V1:2:before_source_read\nAMC_STARTUP_V1:3:before_compile\nAMC_STARTUP_V1:4:before_worker_run\n") {
			_, _ = p.Write([]byte{b})
		}
	})
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				if stage := p.Stage(); stage != "" && !ValidStartupStage(stage) {
					t.Error(stage)
				}
			}
		})
	}
	wg.Wait()
	if p.Stage() != "before_worker_run" {
		t.Fatal(p.Stage())
	}
	if got := (&duplex{progress: &p}).StartupStage(); got != p.Stage() {
		t.Fatal(got)
	}
	if (&duplex{}).StartupStage() != "" {
		t.Fatal("missing observer invented a stage")
	}
}
