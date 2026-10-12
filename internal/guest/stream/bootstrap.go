package stream

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"io"
	"strings"
)

// The small fixed command reads source as bytes before any operation frames.
// Payloads never enter PowerShell command-line encoding or a terminal buffer.
var StartupCommand = startupCommand()

//go:embed startup_pipe.ps1
var startupPipe string

func startupCommand() string {
	var lines []string
	for line := range strings.SplitSeq(startupPipe, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	script := strings.Join(lines, " ")
	command := `powershell.exe -NoLogo -NoProfile -NonInteractive -Command "` + script + `"`
	// This fixed source must fit the Windows command envelope without escaping.
	if strings.ContainsAny(script, "\"\r") || len(command) > 8191 {
		panic("invalid fixed startup command")
	}
	return command
}

//go:embed worker.cs
var workerSource []byte

//go:embed native.cs
var nativeSource []byte

//go:embed security.cs
var securitySource []byte

//go:embed lifetime.cs
var lifetimeSource []byte

func writeSource(w io.Writer) error {
	source := sourceBytes()
	if len(source) > MaxControl {
		return ErrProtocol
	}
	var h [4]byte
	// #nosec G115 -- length is checked against the 256 KiB control bound above.
	binary.BigEndian.PutUint32(h[:], uint32(len(source)))
	if err := writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, source)
}

func sourceBytes() []byte {
	return bytes.Join([][]byte{workerSource, nativeSource, securitySource, lifetimeSource}, nil)
}
