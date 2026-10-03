package desktop

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestClipboardSnapshotMetadataOnlyProtocol(t *testing.T) {
	req := request("clipboard.snapshot")
	base := `{"request_id":"` + req.RequestID + `","success":true,"session_id":1,"elevated":true,"clipboard":{"sequence":4294967295,"formats":[2,13,49321],"inventory_complete":true,"empty":false}}`
	for _, test := range []struct {
		name, output string
		want         domain.DesktopClipboard
	}{
		{"mixed inventory", base, domain.DesktopClipboard{Sequence: ^uint32(0), Formats: []uint32{2, 13, 49321}, InventoryComplete: true}},
		{"complete empty zero", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(base, "4294967295", "0"), "[2,13,49321]", "[]"), `"empty":false`, `"empty":true`), domain.DesktopClipboard{Formats: []uint32{}, InventoryComplete: true, Empty: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{output: []byte(test.output)}
			response, err := New(runner).Execute(t.Context(), "synthetic", req)
			if err != nil || !reflect.DeepEqual(response.Clipboard, &test.want) || response.Text != "" || runner.calls != 1 {
				t.Fatalf("response=%+v error=%v calls=%d", response, err, runner.calls)
			}
			var wire Request
			if json.Unmarshal(runner.input, &wire) != nil || wire.Action != req.Action {
				t.Fatal("snapshot downgraded or malformed dispatch")
			}
			encoded, _ := json.Marshal(response)
			if strings.Contains(string(encoded), `"text"`) {
				t.Fatal("snapshot serialized text")
			}
		})
	}
}

func TestClipboardSnapshotRejectsUnsafeProtocol(t *testing.T) {
	req := request("clipboard.snapshot")
	base := `{"request_id":"` + req.RequestID + `","success":true,"session_id":1,"elevated":true,"clipboard":{"sequence":4294967295,"formats":[2,13,49321],"inventory_complete":true,"empty":false}}`
	oversized := make([]uint32, 257)
	for i := range oversized {
		oversized[i] = uint32(i + 1)
	}
	formats, _ := json.Marshal(oversized)
	for _, test := range []struct{ name, old, replacement string }{
		{"legacy missing metadata", `,"clipboard":{"sequence":4294967295,"formats":[2,13,49321],"inventory_complete":true,"empty":false}`, ""},
		{"null metadata", `{"sequence":4294967295,"formats":[2,13,49321],"inventory_complete":true,"empty":false}`, "null"},
		{"missing sequence", `"sequence":4294967295,`, ""},
		{"null sequence", "4294967295", "null"},
		{"overflow sequence", "4294967295", "4294967296"},
		{"negative sequence", "4294967295", "-1"},
		{"fraction sequence", "4294967295", "1.5"},
		{"string sequence", "4294967295", `"0"`},
		{"missing formats", `"formats":[2,13,49321],`, ""},
		{"null formats", "[2,13,49321]", "null"},
		{"duplicate formats", "[2,13,49321]", "[13,13]"},
		{"unsorted formats", "[2,13,49321]", "[13,2]"},
		{"zero format", "[2,13,49321]", "[0]"},
		{"overflow format", "[2,13,49321]", "[4294967296]"},
		{"oversize formats", "[2,13,49321]", string(formats)},
		{"missing complete", `"inventory_complete":true,`, ""},
		{"null complete", `"inventory_complete":true`, `"inventory_complete":null`},
		{"incomplete", `"inventory_complete":true`, `"inventory_complete":false`},
		{"missing empty", `,"empty":false`, ""},
		{"null empty", `"empty":false`, `"empty":null`},
		{"false empty", `"empty":false`, `"empty":true`},
		{"nonempty flag for empty", "[2,13,49321]", "[]"},
		{"text payload", `"clipboard":`, `"text":"synthetic-private-text","clipboard":`},
		{"empty text field", `"clipboard":`, `"text":"","clipboard":`},
		{"null text field", `"clipboard":`, `"text":null,"clipboard":`},
		{"windows payload", `"clipboard":`, `"windows":[],"clipboard":`},
		{"cursor payload", `"clipboard":`, `"cursor":null,"clipboard":`},
		{"elements payload", `"clipboard":`, `"elements":[],"clipboard":`},
		{"image payload", `"clipboard":`, `"image":"synthetic","clipboard":`},
		{"nested payload", `"empty":false`, `"empty":false,"text":"synthetic-private-text"`},
		{"unsupported helper", `"success":true`, `"success":false,"error":"unsupported_action"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{output: []byte(strings.Replace(base, test.old, test.replacement, 1))}
			response, err := New(runner).Execute(t.Context(), "synthetic", req)
			if !errors.Is(err, ErrUnavailable) || runner.calls != 1 || !reflect.DeepEqual(response, Response{}) || strings.Contains(err.Error(), "synthetic-private-text") {
				t.Fatalf("unsafe response=%+v error=%v calls=%d", response, err, runner.calls)
			}
		})
	}
}
