package stream

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"io"
)

// The small fixed command reads source as bytes before any operation frames.
// Payloads never enter PowerShell command-line encoding or a terminal buffer.
const StartupCommand = `powershell.exe -NoLogo -NoProfile -NonInteractive -Command "$i=[Console]::OpenStandardInput();$h=New-Object byte[] 4;$n=0;while($n -lt 4){$r=$i.Read($h,$n,4-$n);if($r -eq 0){exit 1};$n+=$r};[Array]::Reverse($h);$l=[BitConverter]::ToUInt32($h,0);if($l -gt 262144){exit 1};$b=New-Object byte[] $l;$n=0;while($n -lt $l){$r=$i.Read($b,$n,$l-$n);if($r -eq 0){exit 1};$n+=$r};Add-Type -TypeDefinition ([Text.Encoding]::UTF8.GetString($b)) -ReferencedAssemblies @('System.dll','System.Core.dll','System.Web.Extensions.dll',[Management.Automation.PowerShell].Assembly.Location);[AMCStream.Worker]::Run($i,[Console]::OpenStandardOutput())"`

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
