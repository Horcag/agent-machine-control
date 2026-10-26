package daemon

import (
	"errors"
	"net"
	"reflect"
	"testing"
)

type loopbackSelectionCase struct {
	name, address string
	preferIPv6    bool
	failures      []error
	want          []string
}

func TestLoopbackListenerSelection(t *testing.T) {
	available, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer available.Close()
	v6, v4 := errors.New("IPv6 unavailable"), errors.New("IPv4 unavailable")
	cases := []loopbackSelectionCase{
		{"automatic_ipv6", "", true, []error{nil}, []string{"tcp6 [::1]:0"}},
		{"shared_ipv4", "", false, []error{nil}, []string{"tcp4 127.0.0.1:0"}},
		{"automatic_ipv4_fallback", "", true, []error{v6, nil}, []string{"tcp6 [::1]:0", "tcp4 127.0.0.1:0"}},
		{"automatic_both_fail", "", true, []error{v6, v4}, []string{"tcp6 [::1]:0", "tcp4 127.0.0.1:0"}},
		{"explicit_ipv4", "127.0.0.1:42", true, []error{nil}, []string{"tcp 127.0.0.1:42"}},
		{"explicit_ipv6", "[::1]:42", false, []error{nil}, []string{"tcp [::1]:42"}},
		{"explicit_failure_never_falls_back", "127.0.0.1:42", true, []error{v4}, []string{"tcp 127.0.0.1:42"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkLoopbackSelection(t, tc, available) })
	}
}

func checkLoopbackSelection(t *testing.T, tc loopbackSelectionCase, available net.Listener) {
	t.Helper()
	var calls []string
	listener, gotErr := listenLoopback(tc.address, tc.preferIPv6, func(network, address string) (net.Listener, error) {
		calls = append(calls, network+" "+address)
		failure := tc.failures[len(calls)-1]
		if failure != nil {
			return nil, failure
		}
		return available, nil
	})
	if !reflect.DeepEqual(calls, tc.want) {
		t.Fatalf("bound %v; want %v", calls, tc.want)
	}
	if tc.failures[len(tc.failures)-1] == nil {
		if gotErr != nil || listener != available {
			t.Fatalf("successful bind lost: %v", gotErr)
		}
		return
	}
	if listener != nil {
		t.Fatal("failed bind returned listener")
	}
	for _, wantErr := range tc.failures {
		if wantErr != nil && !errors.Is(gotErr, wantErr) {
			t.Fatalf("bind failure %v lost: %v", wantErr, gotErr)
		}
	}
}

func TestIPv6PreferenceRequiresLinuxLocalState(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		shared         bool
		err            error
		want           bool
	}{
		{"windows", "windows", false, nil, false},
		{"other_platform", "darwin", false, nil, false},
		{"linux_local", "linux", false, nil, true},
		{"shared_windows_mount", "linux", true, nil, false},
		{"unknown_mount", "linux", false, errors.New("mount inspection failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := preferIPv6Loopback(tc.platform, "/synthetic/state", func(path string) (bool, error) {
				called = true
				if path != "/synthetic/state" {
					t.Fatal("wrong state inspected")
				}
				return tc.shared, tc.err
			})
			if got != tc.want || called != (tc.platform == "linux") {
				t.Fatalf("preference=%v inspection=%v", got, called)
			}
		})
	}
}

func TestLoopbackListenerRejectsUnsafeAddressesBeforeBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:0", "localhost:0", "malformed"} {
		t.Run(address, func(t *testing.T) {
			_, err := listenLoopback(address, true, func(string, string) (net.Listener, error) {
				t.Fatal("unsafe address reached bind")
				return nil, nil
			})
			if err == nil {
				t.Fatal("unsafe address accepted")
			}
		})
	}
}
