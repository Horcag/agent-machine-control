package ssh

import "sync"

// StartupProgressPrefix identifies observational markers, never worker authority.
const StartupProgressPrefix = "AMC_STARTUP_V1:"

var startupStages = [...]string{"before_header_read", "before_source_read", "before_compile", "before_worker_run"}
var startupMarkers = [...]string{
	StartupProgressPrefix + "1:before_header_read",
	StartupProgressPrefix + "2:before_source_read",
	StartupProgressPrefix + "3:before_compile",
	StartupProgressPrefix + "4:before_worker_run",
}

// ValidStartupStage accepts only the closed remote observation vocabulary.
func ValidStartupStage(stage string) bool {
	for _, known := range startupStages {
		if stage == known {
			return true
		}
	}
	return false
}

// startupProgress drains every byte without retaining remote text. Position is
// bounded by the next fixed marker; noise remains invalid until its newline.
type startupProgress struct {
	mu                 sync.RWMutex
	sequence, position int
	invalid            bool
}

func (p *startupProgress) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range data {
		switch {
		case b == '\n':
			if !p.invalid && p.sequence < len(startupMarkers) {
				n := len(startupMarkers[p.sequence])
				if p.position == n || p.position == n+1 {
					p.sequence++
				}
			}
			p.position, p.invalid = 0, false
		case p.invalid:
			// Drain the remainder of a rejected line without retaining it.
		case p.sequence == len(startupMarkers):
			p.invalid = true
		default:
			next := startupMarkers[p.sequence]
			if p.position < len(next) && b == next[p.position] || p.position == len(next) && b == '\r' {
				p.position++
			} else {
				p.invalid = true
			}
		}
	}
	return len(data), nil
}

func (p *startupProgress) Stage() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.sequence == 0 {
		return ""
	}
	return startupStages[p.sequence-1]
}
