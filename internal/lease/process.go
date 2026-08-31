package lease

import (
	"fmt"
	"os"
	"runtime"
)

// DefaultIdentityProvider provides process identity using the host OS runtime.
type DefaultIdentityProvider struct{}

// CurrentIdentity returns the current runtime ID, process PID, and native start identity.
func (*DefaultIdentityProvider) CurrentIdentity() (string, int, string) {
	pid := os.Getpid()
	return detectRuntimeID(), pid, platformProcessStartTime(pid)
}

// DefaultLivenessChecker checks process existence and native start identity.
type DefaultLivenessChecker struct{}

// IsAlive checks whether PID still names the recorded native process instance.
func (*DefaultLivenessChecker) IsAlive(pid int, recordedStartTime string) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	return platformProcessAlive(pid, recordedStartTime)
}

func detectRuntimeID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	if native := platformRuntimeIdentity(); native != "" {
		return fmt.Sprintf("%s:%s:%s", runtime.GOOS, hostname, native)
	}
	return fmt.Sprintf("%s:%s", runtime.GOOS, hostname)
}
