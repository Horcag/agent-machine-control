package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func formatUptime(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	d := time.Duration(ms) * time.Millisecond
	d = d.Round(time.Second)
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func formatMemoryMB(bytes uint64) string {
	mb := bytes / (1024 * 1024)
	return fmt.Sprintf("%d MB", mb)
}

func formatCapabilities(caps domain.CapabilitySet) string {
	if len(caps) == 0 {
		return "none"
	}
	slice := caps.Slice()
	if len(slice) == 0 {
		return "none"
	}
	return strings.Join(slice, ", ")
}

func writeJSON(w io.Writer, val any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(val)
}
