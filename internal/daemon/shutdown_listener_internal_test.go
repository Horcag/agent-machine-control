package daemon

import (
	"net"
	"testing"
)

func TestCloseAdmissionListenerHandlesAbsentAndAlreadyClosedListener(t *testing.T) {
	if err := (&Server{}).closeAdmissionListener(); err != nil {
		t.Fatalf("nil listener: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{listener: listener}
	if err := server.closeAdmissionListener(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := server.closeAdmissionListener(); err != nil {
		t.Fatalf("already closed listener: %v", err)
	}
}
