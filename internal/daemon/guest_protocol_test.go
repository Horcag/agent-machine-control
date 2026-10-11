package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
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

func TestGuestHTTPRejectsUnsupportedConnectionAndMethods(t *testing.T) {
	server := &Server{}
	for _, tc := range []struct {
		method string
		status int
	}{{http.MethodGet, 405}, {http.MethodPost, 400}} {
		request := httptest.NewRequest(tc.method, "/v1/guest/operations", strings.NewReader("untrusted"))
		response := httptest.NewRecorder()
		server.handleGuestOperation(response, request)
		if response.Code != tc.status {
			t.Fatalf("method=%s status=%d", tc.method, response.Code)
		}
	}
	response := httptest.NewRecorder()
	server.handleGuestApproval(response, httptest.NewRequest(http.MethodGet, "/v1/guest/approvals", nil))
	if response.Code != 405 {
		t.Fatal("approval method admitted")
	}
}
func TestGuestApprovalRejectsUntrustedAuthorityAndEnvelope(t *testing.T) {
	server := &Server{}
	operator := domain.ActorContext{AuthenticatedCaller: "operator:synthetic", EffectiveActor: "operator:synthetic", CallerPermissions: domain.NewScopeSet(domain.ScopeSessionAdmin), EffectivePermissions: domain.NewScopeSet(domain.ScopeSessionAdmin)}
	delegated := operator.Clone()
	delegated.EffectiveActor = auth.AgentMCPPrincipal
	for _, tc := range []struct {
		name, body string
		caller     *domain.ActorContext
		status     int
	}{
		{"anonymous", `{}`, nil, 403}, {"delegated", `{}`, &delegated, 403}, {"unknown", `{"valid_for_millis":1000,"path":"untrusted"}`, &operator, 400}, {"duplicate", `{"valid_for_millis":1000,"valid_for_millis":300000}`, &operator, 400}, {"overflow", `{"valid_for_millis":9223372036854775807}`, &operator, 400}, {"too-short", `{"valid_for_millis":999}`, &operator, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/guest/approvals", strings.NewReader(tc.body))
			if tc.caller != nil {
				r = r.WithContext(context.WithValue(r.Context(), callerContextKey, *tc.caller))
			}
			w := httptest.NewRecorder()
			server.handleGuestApproval(w, r)
			if w.Code != tc.status {
				t.Fatalf("authority/envelope status=%d", w.Code)
			}
		})
	}
}
func TestGuestHTTPFramingRejectsWholeBodyMismatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		length      int64
		deadline    time.Time
	}{
		{"wrong-type", "application/json", 0, time.Now().Add(time.Minute)}, {"unknown-length", GuestContentType, -1, time.Now().Add(time.Minute)}, {"unbound-payload", GuestContentType, 0, time.Now().Add(time.Minute)}, {"missing-deadline", GuestContentType, 0, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := app.GuestOperationParams{Operation: "put", Size: 3, Deadline: tc.deadline}
			if tc.name == "missing-deadline" {
				p.Size = 0
			}
			data, _ := json.Marshal(p)
			var body bytes.Buffer
			// #nosec G115 -- bounded synthetic metadata from this fixed operation is below 64KiB.
			_ = binary.Write(&body, binary.BigEndian, uint32(len(data)))
			body.Write(data)
			r := httptest.NewRequest(http.MethodPost, "/", &body)
			r.Header.Set("Content-Type", tc.contentType)
			if tc.length < 0 {
				r.ContentLength = tc.length
			}
			if _, err := readGuestHTTPRequest(r); err == nil {
				t.Fatal("whole-body framing admitted")
			}
		})
	}
}
func TestGuestHTTPServiceFailureHasBoundedTrailer(t *testing.T) {
	server, token, fixture := newGuestHTTPServiceFixture(t)
	p, _ := guestHTTPCase("prepare", "")
	p.Operation = "adopt-arbitrary-root"
	final, body := invokeGuestHTTPFixture(t, server.URL, token, p, nil)
	if final.Error == "" || len(body) != 0 || len(fixture.operations) != 0 {
		t.Fatalf("invalid operation escaped shared admission: %+v %q %v", final, body, fixture.operations)
	}
}
