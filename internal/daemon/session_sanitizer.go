package daemon

import (
	"github.com/Horcag/agent-machine-control/internal/auth"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func daemonSessionSanitizerConfig(authStore *auth.Store, config guestssh.SanitizerConfig) guestssh.SanitizerConfig {
	activeBearerSecrets := authStore.ActiveBearerSecrets()
	merged := mergeSessionSanitizerConfig(config, activeBearerSecrets)
	for _, secret := range activeBearerSecrets {
		clear(secret)
	}
	return merged
}

func mergeSessionSanitizerConfig(config guestssh.SanitizerConfig, mandatorySecrets [][]byte) guestssh.SanitizerConfig {
	merged := guestssh.SanitizerConfig{
		Patterns: append([]guestssh.RedactionPattern(nil), config.Patterns...),
	}
	for _, secret := range config.ExactSecrets {
		if len(secret) > 0 {
			merged.ExactSecrets = append(merged.ExactSecrets, append([]byte(nil), secret...))
		}
	}
	for _, secret := range mandatorySecrets {
		if len(secret) > 0 {
			merged.ExactSecrets = append(merged.ExactSecrets, append([]byte(nil), secret...))
		}
	}
	return merged
}
