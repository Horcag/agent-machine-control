package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/Horcag/agent-machine-control/internal/app"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fragmentedGuestReader struct{ io.Reader }

func (r fragmentedGuestReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}
func TestGuestMetadataStrictFraming(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		valid      bool
	}{
		{"valid", `{"operation":"put","size":3}`, true},
		{"duplicate", `{"size":1,"size":2}`, false},
		{"unknown", `{"operation":"put","path":"C:\\secret","size":3}`, false},
		{"extra", `{"size":0} {}`, false},
		{"negative", `{"size":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			// #nosec G115 -- fixed synthetic test metadata is less than 64KiB.
			_ = binary.Write(&b, binary.BigEndian, uint32(len(tc.json)))
			b.WriteString(tc.json)
			b.WriteString("raw")
			p, n, err := DecodeGuestMetadata(fragmentedGuestReader{&b})
			if (err == nil) != tc.valid {
				t.Fatalf("result=%+v prefix=%d error=%v", p, n, err)
			}
			if tc.valid && b.String() != "raw" {
				t.Fatal("metadata consumed payload")
			}
		})
	}
	for _, data := range [][]byte{{}, {0, 0, 0, 0}, {0, 1, 0, 1}, {0, 0, 0, 3, '{', '}'}} {
		if _, _, err := DecodeGuestMetadata(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted truncated or oversized frame")
		}
	}
}
func TestGuestMetadataCannotDeserializeCaller(t *testing.T) {
	var p app.GuestOperationParams
	if err := json.Unmarshal([]byte(`{"caller":{"EffectiveActor":"operator:other"}}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Caller.EffectiveActor != "" {
		t.Fatal("public caller was trusted")
	}
}

func TestGuestInboundCancellationUnblocksRead(t *testing.T) {
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
		defer cancel()
		stop, err := guestIOContext(ctx, w, r.Body, time.Now().Add(time.Second))
		if err != nil {
			finished <- err
			return
		}
		defer stop()
		_, err = io.Copy(io.Discard, r.Body)
		finished <- err
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	connection, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = fmt.Fprintf(connection, "POST / HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\n\r\nx", host)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("incomplete body accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("inbound cancellation did not interrupt body read")
	}
}
