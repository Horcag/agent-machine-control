package daemon

import (
	"errors"
	"testing"
	"time"
)

func TestSessionTimeoutWirePrecisionAndValidation(t *testing.T) {
	seconds, millis, err := EncodeSessionTimeout(250 * time.Millisecond)
	if err != nil || seconds != 0 || millis != 250 {
		t.Fatalf("EncodeSessionTimeout(250ms) = (%d, %d, %v)", seconds, millis, err)
	}
	got, err := ResolveSessionTimeout(seconds, millis, time.Second)
	if err != nil || got != 250*time.Millisecond {
		t.Fatalf("ResolveSessionTimeout = (%v, %v), want 250ms", got, err)
	}
	seconds, millis, err = EncodeSessionTimeout(30 * time.Second)
	if err != nil || seconds != 30 || millis != 0 {
		t.Fatalf("whole-second compatibility = (%d, %d, %v)", seconds, millis, err)
	}

	invalid := []struct {
		seconds int64
		millis  int64
	}{
		{seconds: -1}, {millis: -1}, {seconds: 1, millis: 1},
		{seconds: int64((time.Duration(1<<63-1))/time.Second) + 1},
		{millis: int64((time.Duration(1<<63-1))/time.Millisecond) + 1},
	}
	for _, tc := range invalid {
		if _, err := ResolveSessionTimeout(tc.seconds, tc.millis, 0); !errors.Is(err, ErrInvalidSessionTimeout) {
			t.Errorf("ResolveSessionTimeout(%d, %d) error = %v", tc.seconds, tc.millis, err)
		}
	}
	if _, _, err := EncodeSessionTimeout(1500 * time.Microsecond); !errors.Is(err, ErrInvalidSessionTimeout) {
		t.Fatalf("sub-millisecond timeout error = %v", err)
	}
	if got, err := ResolveSessionTimeout(3600, 0, 0); err != nil || got != MaxSessionMutationTimeout {
		t.Fatalf("maximum timeout = %v, %v", got, err)
	}
	if _, err := ResolveSessionTimeout(0, 3_600_001, 0); !errors.Is(err, ErrInvalidSessionTimeout) {
		t.Fatalf("excessive timeout error = %v", err)
	}
}
