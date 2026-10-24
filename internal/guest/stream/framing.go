package stream

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
)

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return ErrProtocol
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
func writeControl(w io.Writer, v any) error {
	p, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(p) > MaxControl {
		return ErrProtocol
	}
	var h [4]byte
	// #nosec G115 -- length is checked against the 256 KiB control bound above.
	binary.BigEndian.PutUint32(h[:], uint32(len(p)))
	if err = writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, p)
}
func readControl(r io.Reader, v any) error {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > MaxControl {
		return ErrProtocol
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return err
	}
	if err := json.Unmarshal(p, v); err != nil {
		return ErrProtocol
	}
	return nil
}
func copyPayload(w io.Writer, r io.Reader, size int64) (string, error) {
	h := sha256.New()
	buf := make([]byte, BufferSize)
	n, err := io.CopyBuffer(io.MultiWriter(w, h), io.LimitReader(r, size), buf)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", ErrIntegrity
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
