package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func retirementLoaderPayload(identity, source []byte) []byte {
	var buffer bytes.Buffer
	for _, block := range [][]byte{identity, source} {
		// #nosec G115 -- synthetic test payloads are bounded below 512 KiB.
		_ = binary.Write(&buffer, binary.BigEndian, uint32(len(block)))
		buffer.Write(block)
	}
	return buffer.Bytes()
}

func runRetirementLoader(t *testing.T, source string, input []byte) ([]byte, error) {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell unavailable for local loader semantics")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := guestRetirementCommand(source)
	if len(utf16.Encode([]rune(command))) > 7900 {
		t.Fatal("loader exceeds normal Windows command budget")
	}
	token := strings.TrimPrefix(command, "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ")
	process := exec.CommandContext(ctx, pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", token)
	stagePath := filepath.Join(t.TempDir(), "retirement-stage.txt")
	process.Env = append(os.Environ(), "AMC_RETIREMENT_TEST_STAGE="+stagePath)
	process.Stdin = bytes.NewReader(input)
	started := time.Now()
	output, err := process.CombinedOutput()
	t.Logf("retirement fixture: stage=%s elapsed=%s context=%v", retirementLoaderStage(stagePath), time.Since(started).Round(time.Millisecond), ctx.Err())
	return output, err
}

func retirementLoaderStage(path string) string {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "not-entered"
		}
		return "unavailable"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32))
	if err != nil {
		return "unavailable"
	}
	switch stage := string(data); stage {
	case "entered", "add-type-start", "add-type-done", "inventory-done":
		return stage
	default:
		return "invalid"
	}
}

func TestGuestRetirementLoaderPreservesFullInteger(t *testing.T) {
	source := "$original | ConvertTo-Json -Compress"
	identity := []byte(`{"pid":1234,"birthTicks":639273243660826407,"image":"synthetic","sid":"synthetic"}`)
	output, err := runRetirementLoader(t, source, retirementLoaderPayload(identity, []byte(source)))
	if err != nil {
		t.Fatalf("loader: %v %s", err, output)
	}
	var observed map[string]json.RawMessage
	if err := json.Unmarshal(output, &observed); err != nil {
		t.Fatal(err)
	}
	if string(observed["birthTicks"]) != "639273243660826407" {
		t.Fatalf("native birth changed: %s", output)
	}
}

func TestGuestRetirementLoaderRefusesInvalidFrames(t *testing.T) {
	source := "throw 'synthetic_source_was_executed'"
	original := retirementLoaderPayload([]byte(`{"pid":1234}`), []byte(source))
	cases := map[string][]byte{
		"truncated":    original[:len(original)-1],
		"trailing":     append(bytes.Clone(original), 1),
		"zero":         make([]byte, 4),
		"oversize":     {0, 1, 0, 0},
		"source_hash":  retirementLoaderPayload([]byte(`{"pid":1234}`), []byte("throw 'changed_source_was_executed'")),
		"invalid_utf8": retirementLoaderPayload([]byte{255}, []byte(source)),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			output, err := runRetirementLoader(t, source, input)
			if err == nil || bytes.Contains(output, []byte("source_was_executed")) {
				t.Fatalf("invalid frame reached execution: %v %s", err, output)
			}
		})
	}
}

func TestGuestRetirementNativeInventoryFailsClosed(t *testing.T) {
	parts := strings.Split(guestRetirementScript, "@'")
	if len(parts) != 2 {
		t.Fatal("native probe source unavailable")
	}
	native, _, ok := strings.Cut(parts[1], "'@")
	if !ok {
		t.Fatal("native probe terminator missing")
	}
	// Compile the actual embedded class, then invoke only its pure census decision.
	source := "[IO.File]::WriteAllText($env:AMC_RETIREMENT_TEST_STAGE,'entered')\n" +
		"[IO.File]::WriteAllText($env:AMC_RETIREMENT_TEST_STAGE,'add-type-start')\n" +
		"Add-Type -TypeDefinition @'\n" + strings.TrimSpace(native) + "\n'@\n" +
		"[IO.File]::WriteAllText($env:AMC_RETIREMENT_TEST_STAGE,'add-type-done')\n" +
		"$ids=[uint32[]]@(1,2,0);if([AMCOriginalProcessProbe]::InventoryMissing(1,$ids,8)){throw 'present_pid_absent'};if(-not[AMCOriginalProcessProbe]::InventoryMissing(3,$ids,8)){throw 'missing_pid_not_observed'};$refused=0;foreach($size in @(0,1,12,16)){try{$null=[AMCOriginalProcessProbe]::InventoryMissing(3,$ids,[uint32]$size);throw 'invalid_census_accepted'}catch{if($_.Exception.Message-eq'invalid_census_accepted'){throw};$refused++}}\n" +
		"[IO.File]::WriteAllText($env:AMC_RETIREMENT_TEST_STAGE,'inventory-done')\n" +
		"@{refused=$refused}|ConvertTo-Json -Compress"
	output, err := runRetirementLoader(t, source, retirementLoaderPayload([]byte(`{}`), []byte(source)))
	if err != nil {
		t.Fatalf("native inventory decision: %v %s", err, output)
	}
	var observed struct {
		Refused int `json:"refused"`
	}
	if json.Unmarshal(output, &observed) != nil || observed.Refused != 4 {
		t.Fatalf("census decisions missing: %s", output)
	}
}
