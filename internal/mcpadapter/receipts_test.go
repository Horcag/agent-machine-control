package mcpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
)

func TestReceiptListExactFilterAndInconclusiveEmpty(t *testing.T) {
	for _, test := range []struct {
		name, key, body string
		want            int
	}{{"ambiguous exact", "exact", `[{"idempotency_key":"exact","receipt_id":"first","actor":"agent:first","target":"synthetic:first"},{"idempotency_key":"exact","receipt_id":"second","actor":"agent:second","target":"synthetic:second"}]`, 2},
		{"all", "", `[{"idempotency_key":"exact","receipt_id":"own-aborted"},{"idempotency_key":"exact-prefix","receipt_id":"other"}]`, 2}, {"exact", "exact", `[{"idempotency_key":"exact","receipt_id":"own-aborted"},{"idempotency_key":"exact-prefix","receipt_id":"other"}]`, 1}, {"missing", "missing", `[{"idempotency_key":"exact","receipt_id":"own-aborted"}]`, 0}, {"empty", "exact", `[]`, 0}, {"null", "", `null`, 0}} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/receipts" || r.URL.Query().Get("limit") != "50" || r.Header.Get("Authorization") != "Bearer synthetic-agent" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				_, _ = w.Write([]byte(`{"schema_version":"1","receipts":` + test.body + `}`))
			}))
			defer server.Close()
			a := &Adapter{client: client.New(server.URL, "synthetic-agent")}
			tool, out, err := a.ReceiptList(t.Context(), nil, ReceiptListInput{IdempotencyKey: test.key})
			if err != nil || tool != nil || out.SchemaVersion != SchemaVersion || len(out.Receipts) != test.want || out.Receipts == nil || calls != 1 {
				t.Fatalf("tool=%+v out=%+v err=%v calls=%d", tool, out, err, calls)
			}
			if test.key == "exact" && test.want == 1 && out.Receipts[0].ReceiptID != "own-aborted" {
				t.Fatal("wrong exact receipt")
			}
		})
	}
}

func TestReceiptListBoundsAndReadFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "1000" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		http.Error(w, "failed", 500)
	}))
	defer server.Close()
	a := &Adapter{client: client.New(server.URL, "synthetic-agent")}
	for _, in := range []ReceiptListInput{{Limit: -1}, {Limit: 1001}, {IdempotencyKey: strings.Repeat("x", 257)}, {IdempotencyKey: "invalid\nkey"}} {
		tool, _, err := a.ReceiptList(t.Context(), nil, in)
		if err != nil || tool == nil || !tool.IsError || calls != 0 {
			t.Fatalf("input=%+v tool=%+v err=%v calls=%d", in, tool, err, calls)
		}
	}
	tool, _, err := a.ReceiptList(t.Context(), nil, ReceiptListInput{Limit: 1000})
	if err != nil || tool == nil || !tool.IsError || calls != 1 {
		t.Fatalf("read failure tool=%+v err=%v calls=%d", tool, err, calls)
	}
	a = &Adapter{stateDir: t.TempDir()}
	tool, _, err = a.ReceiptList(t.Context(), nil, ReceiptListInput{})
	if err != nil || tool == nil || !tool.IsError {
		t.Fatalf("missing client tool=%+v err=%v", tool, err)
	}
}
