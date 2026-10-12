package stream

import (
	"errors"
	"io"
	"testing"
)

func TestRemoteExitPreservesEOFAndOnlyChannelNumericStatus(t *testing.T) {
	for _, code := range []int{0, 1, 215, 255, 0xc0000005} {
		channel := &exitChannel{code: code, known: true}
		err := observeLifetimeExit(t.Context(), channel, io.EOF)
		exit, ok := errors.AsType[*RemoteExit](err)
		if !ok || !exit.Valid() || exit.Code != code || !errors.Is(err, io.EOF) {
			t.Fatalf("numeric status %d lost or EOF changed: %v", code, err)
		}
	}
	for _, code := range []int{-1, 0x100000000} {
		if (&RemoteExit{Code: code}).Valid() {
			t.Fatal("invalid remote status accepted")
		}
		err := observeLifetimeExit(t.Context(), &exitChannel{code: code, known: true}, io.EOF)
		if _, ok := errors.AsType[*RemoteExit](err); ok || err != io.EOF {
			t.Fatal("invalid remote status recorded", err)
		}
	}
	for _, channel := range []*exitChannel{{code: 1, known: false}, {code: 211, known: true}} {
		err := observeLifetimeExit(t.Context(), channel, io.EOF)
		if _, ok := errors.AsType[*RemoteExit](err); ok {
			t.Fatal("missing status or known lifetime marker replaced", err)
		}
	}
}
