package daemon

import (
	"errors"
	"fmt"
	"net"
)

// Shared Windows/WSL state retains IPv4 reachability across both environments.
// Only positively identified Linux-local state opts into the faster local path.
func preferIPv6Loopback(platform, statePath string, inspect func(string) (bool, error)) bool {
	if platform != "linux" {
		return false
	}
	shared, err := inspect(statePath)
	return err == nil && !shared
}

// Explicit addresses remain exact and never fall back to another endpoint.
func listenLoopback(addr string, preferIPv6 bool, bind func(string, string) (net.Listener, error)) (net.Listener, error) {
	if addr == "" {
		var ipv6Err error
		if preferIPv6 {
			var listener net.Listener
			listener, ipv6Err = bind("tcp6", "[::1]:0")
			if ipv6Err == nil {
				return listener, nil
			}
		}
		listener, ipv4Err := bind("tcp4", "127.0.0.1:0")
		if ipv4Err != nil {
			return nil, fmt.Errorf("daemon: failed to listen on loopback: %w", errors.Join(ipv6Err, ipv4Err))
		}
		return listener, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("daemon: invalid listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("daemon: listen address host %q must be a loopback IP literal", host)
	}
	listener, err := bind("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("daemon: failed to listen on %s: %w", addr, err)
	}
	return listener, nil
}
