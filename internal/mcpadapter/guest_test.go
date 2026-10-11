package mcpadapter

import (
	"testing"
)

func TestGuestInvalidDeadlineNoDispatch(t *testing.T) {
	adapter := NewAdapter("missing")
	result, _, err := adapter.GuestPrepare(t.Context(), nil, GuestInput{Deadline: "invalid"})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
