package stream

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

type scriptedChannel struct {
	read  *bytes.Reader
	write bytes.Buffer
}

func (s *scriptedChannel) Read(p []byte) (int, error)  { return s.read.Read(p) }
func (s *scriptedChannel) Write(p []byte) (int, error) { return s.write.Write(p) }
func response(req Request, payload []byte, result Result) *scriptedChannel {
	var wire bytes.Buffer
	_ = writeControl(&wire, Result{ID: req.ID, Size: int64(len(payload))})
	_, _ = wire.Write(payload)
	_ = writeControl(&wire, result)
	return &scriptedChannel{read: bytes.NewReader(wire.Bytes())}
}
func TestDownloadResultRequiresObservedDigestAndCompleteReceipt(t *testing.T) {
	req := Request{ID: "get", Operation: "get", MaxOutput: 100}
	data := []byte{0, 255, 13, 10}
	result := Result{ID: req.ID, Status: "ok", Size: 4, SHA256: hash(data)}
	ch := response(req, data, result)
	var out bytes.Buffer
	got, err := exchange(ch, req, nil, &out)
	if err != nil || got.SHA256 != hash(data) || !bytes.Equal(out.Bytes(), data) {
		t.Fatal(got, err)
	}
	for _, bad := range []Result{{ID: "other", Size: 4, SHA256: hash(data)}, {ID: req.ID, Size: 3, SHA256: hash(data)}, {ID: req.ID, Size: 4, SHA256: hash(nil)}} {
		if _, err = exchange(response(req, data, bad), req, nil, io.Discard); !errors.Is(err, ErrIntegrity) {
			t.Fatal("accepted wrong receipt", err)
		}
	}
	wire := response(req, data, result)
	raw := make([]byte, wire.read.Len())
	_, _ = wire.read.Read(raw)
	wire.read = bytes.NewReader(raw[:len(raw)-1])
	if _, err = exchange(wire, req, nil, io.Discard); err == nil {
		t.Fatal("accepted incomplete completion")
	}
	req.MaxOutput = 3
	if _, err = exchange(response(req, data, result), req, nil, io.Discard); !errors.Is(err, ErrProtocol) {
		t.Fatal("unbounded output", err)
	}
}
func TestWholeRequestValidation(t *testing.T) {
	valid := request()
	cases := []Request{valid, valid, valid, valid, valid, valid, valid, valid}
	cases[0].ID = ""
	cases[1].DeadlineUTC = time.Now().Add(-time.Second)
	cases[2].Size = -1
	cases[3].SHA256 = "no"
	cases[4].Operation = "unknown"
	cases[5].Operation = "exec"
	cases[6].Operation = "get"
	cases[7].Operation = "remove-owned"
	for i, req := range cases {
		if err := validate(req); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid case %d: %v", i, err)
		}
	}
	if err := validate(valid); err != nil {
		t.Fatal(err)
	}
}

func TestObserveRequiresExactPathAndUploadPreflight(t *testing.T) {
	observe := Request{ID: "observe", Operation: "observe", Path: `C:\synthetic\workspace`, DeadlineUTC: time.Now().Add(time.Minute)}
	if err := validate(observe); err != nil {
		t.Fatal(err)
	}
	observe.Path = ""
	if !errors.Is(validate(observe), ErrRequest) {
		t.Fatal("missing preflight target")
	}
	put := request()
	put.ExpectedRoot = nil
	if !errors.Is(validate(put), ErrRequest) {
		t.Fatal("upload without original root")
	}
}

func TestDownloadRequiresOneOriginalReferenceForExactPath(t *testing.T) {
	req := Request{ID: "owned-download", Operation: "get", Path: `C:\synthetic\workspace\payload`, DeadlineUTC: time.Now().Add(time.Minute), MaxOutput: 64}
	for _, refs := range [][]FileIdentity{nil, {{Path: `C:\synthetic\foreign\payload`}}, {{Path: req.Path}, {Path: req.Path}}} {
		req.Owned = refs
		if !errors.Is(validate(req), ErrRequest) {
			t.Fatal("accepted missing, foreign or ambiguous ownership reference")
		}
	}
	req.Owned = []FileIdentity{{Path: req.Path}}
	if err := validate(req); err != nil {
		t.Fatal(err)
	}
}
