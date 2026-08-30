package cli

import (
	"strings"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func sanitizeSessionChunks(chunks []daemon.SessionChunkDTO, sanitizer *guestssh.StreamSanitizer, flush bool) string {
	var clean strings.Builder
	for i := range chunks {
		chunks[i].Data = sanitizer.Push([]byte(chunks[i].Data))
		clean.WriteString(chunks[i].Data)
	}
	if flush {
		tail := sanitizer.Flush()
		clean.WriteString(tail)
		if len(chunks) > 0 {
			chunks[len(chunks)-1].Data += tail
		}
	}
	return clean.String()
}
