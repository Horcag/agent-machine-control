package stream

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

type fragments struct {
	io.Reader
	size int
}

func (f fragments) Read(p []byte) (int, error) {
	if len(p) > f.size {
		p = p[:f.size]
	}
	return f.Reader.Read(p)
}

type shortWriter struct {
	bytes.Buffer
	size int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.size {
		p = p[:w.size]
	}
	return w.Buffer.Write(p)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
func hash(data []byte) string                { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func TestControlFragmentedAndShortWrites(t *testing.T) {
	w := &shortWriter{size: 1}
	want := Result{ID: "synthetic", Size: 123, SHA256: hash(nil)}
	if err := writeControl(w, want); err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := readControl(fragments{bytes.NewReader(w.Bytes()), 1}, &got); err != nil || got.ID != want.ID || got.Size != 123 {
		t.Fatalf("fragmented: %+v %v", got, err)
	}
	if err := writeControl(zeroWriter{}, want); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}
func TestControlRejectsTruncationAndBounds(t *testing.T) {
	var valid bytes.Buffer
	_ = writeControl(&valid, Result{ID: "x"})
	for n := 0; n < valid.Len(); n++ {
		var result Result
		if err := readControl(bytes.NewReader(valid.Bytes()[:n]), &result); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, size := range []uint32{0, MaxControl + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		var result Result
		if !errors.Is(readControl(bytes.NewReader(header[:]), &result), ErrProtocol) {
			t.Fatal(size)
		}
	}
	var result Result
	if err := readControl(bytes.NewReader([]byte{0, 0, 0, 1, '!'}), &result); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}
func TestPayloadBinaryIntegrityAndShortIO(t *testing.T) {
	payload := bytes.Repeat([]byte{0, 255, 1, 13, 10}, 30000)
	var output bytes.Buffer
	digest, err := copyPayload(&output, fragments{bytes.NewReader(payload), 7}, int64(len(payload)))
	if err != nil || digest != hash(payload) || !bytes.Equal(output.Bytes(), payload) {
		t.Fatal("binary corruption", err)
	}
	if _, err = copyPayload(io.Discard, bytes.NewReader(payload), int64(len(payload)+1)); !errors.Is(err, ErrIntegrity) {
		t.Fatal(err)
	}
	if _, err = copyPayload(&shortWriter{size: 1}, bytes.NewReader(payload), int64(len(payload))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}
func BenchmarkPayload(b *testing.B) {
	data := bytes.Repeat([]byte{1}, 4*1024*1024)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := copyPayload(io.Discard, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
	}
}
