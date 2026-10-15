// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package netport

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

// Close calls f.
func (f closerFunc) Close() error { return f() }

// probeCall is one probe a recorded network was asked to bind.
type probeCall struct {
	network string
	host    string
	port    string
}

// probeLog records every probe the real TCP and UDP entries are asked to bind,
// so a test observes the probe's own sockets instead of re-binding the number
// afterwards, which another process on the host may have taken in between.
type probeLog struct {
	mu    sync.Mutex
	calls []probeCall
}

// recordProbes wraps the real TCP and UDP probes for the rest of the test.
func recordProbes(t *testing.T) *probeLog {
	t.Helper()
	log := &probeLog{}
	for _, network := range []string{NetworkTCP, NetworkUDP} {
		inner := probes[network]
		probes[network] = func(addr string) (net.Addr, io.Closer, error) {
			bound, closer, err := inner(addr)
			if err != nil {
				return nil, nil, err
			}
			host, _, splitErr := net.SplitHostPort(addr)
			_, port, portErr := net.SplitHostPort(bound.String())
			if err := errors.Join(splitErr, portErr); err != nil {
				_ = closer.Close()
				return nil, nil, err
			}
			log.mu.Lock()
			log.calls = append(log.calls, probeCall{network: network, host: host, port: port})
			log.mu.Unlock()
			return bound, closer, nil
		}
		t.Cleanup(func() { probes[network] = inner })
	}
	return log
}

// recorded returns a copy of the calls logged so far.
func (l *probeLog) recorded() []probeCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]probeCall(nil), l.calls...)
}

// fakeNetwork registers a probe network backed by p for the rest of the test
// and returns its name. The bound address it reports is the requested one.
func fakeNetwork(t *testing.T, p func(addr string) (io.Closer, error)) string {
	t.Helper()
	name := "netport-test-" + strconv.Itoa(len(probes))
	probes[name] = func(addr string) (net.Addr, io.Closer, error) {
		c, err := p(addr)
		if err != nil {
			return nil, nil, err
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, nil, err
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			return nil, nil, err
		}
		return &net.TCPAddr{IP: net.ParseIP(host), Port: n}, c, nil
	}
	t.Cleanup(func() { delete(probes, name) })
	return name
}

// binds is a fake probe that always succeeds and holds nothing.
func binds(string) (io.Closer, error) { return closerFunc(func() error { return nil }), nil }

// writeEphemeralRange writes a fixture ip_local_port_range file.
func writeEphemeralRange(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ip_local_port_range")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestFreePortOn_RejectsAPortHeldOnUDPOnly(t *testing.T) {
	const host = "127.0.0.1"
	pc, err := net.ListenPacket(NetworkUDP, net.JoinHostPort(host, kernelDrawsPort))
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer func() { _ = pc.Close() }()
	_, candidate, err := net.SplitHostPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	if _, err := freePortOn(host, 1, candidate, NetworkTCP); err != nil {
		t.Fatalf("TCP-only probe rejected candidate: %v", err)
	}
	if _, err := freePortOn(host, 1, candidate, NetworkTCP, NetworkUDP); err == nil {
		t.Fatalf("TCP+UDP probe accepted port %s, which is held on UDP", candidate)
	}
}

func TestFreePortOn_ProbesAtTheHostItIsAskedAbout(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			log := recordProbes(t)
			port, err := FreePortOn(host, NetworkTCP, NetworkUDP)
			if err != nil {
				t.Fatalf("FreePortOn(%q): %v", host, err)
			}
			calls := log.recorded()
			if len(calls) != 2 {
				t.Fatalf("probes = %+v, want one TCP and one UDP", calls)
			}
			want := []probeCall{
				{network: NetworkTCP, host: host, port: strconv.Itoa(port)},
				{network: NetworkUDP, host: host, port: strconv.Itoa(port)},
			}
			for i := range want {
				if calls[i] != want[i] {
					t.Errorf("probe %d = %+v, want %+v", i, calls[i], want[i])
				}
			}
		})
	}
}

func TestFreePortOn_NamedErrors(t *testing.T) {
	t.Run("unknown network", func(t *testing.T) {
		_, err := FreePortOn("127.0.0.1", "sctp")
		var unknown *UnknownProbeNetworkError
		if !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want *UnknownProbeNetworkError", err)
		}
		if unknown.Network != "sctp" {
			t.Fatalf("Network = %q, want sctp", unknown.Network)
		}
	})
	t.Run("empty network list", func(t *testing.T) {
		_, err := FreePortOn("127.0.0.1")
		var empty *NoProbeNetworkError
		if !errors.As(err, &empty) {
			t.Fatalf("error = %v, want *NoProbeNetworkError", err)
		}
	})
	t.Run("unusable host", func(t *testing.T) {
		port, err := FreePortOn("203.0.113.1", NetworkTCP)
		var exhausted *NoFreePortError
		if !errors.As(err, &exhausted) {
			t.Fatalf("FreePortOn(unbindable host) = (%d, %v), want *NoFreePortError", port, err)
		}
		if exhausted.Attempts != freePortAttempts || exhausted.Unwrap() == nil {
			t.Fatalf("NoFreePortError = %+v, want %d attempts and an underlying bind error", exhausted, freePortAttempts)
		}
	})
}

func TestFreePortOn_ReleasesItsProbes(t *testing.T) {
	open := 0
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		open++
		return closerFunc(func() error { open--; return nil }), nil
	})
	if _, err := freePortOn("127.0.0.1", 1, "4242", network, network); err != nil {
		t.Fatalf("freePortOn: %v", err)
	}
	if open != 0 {
		t.Errorf("%d probe sockets left open after the port was returned", open)
	}
}

func TestHoldPort_HoldsEverySocketUntilClosed(t *testing.T) {
	open := 0
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		open++
		return closerFunc(func() error { open--; return nil }), nil
	})
	resolved, err := resolveProbes("127.0.0.1", []string{network, network})
	if err != nil {
		t.Fatalf("resolveProbes: %v", err)
	}

	port, held, err := holdPort("127.0.0.1", "4242", resolved)
	if err != nil {
		t.Fatalf("holdPort: %v", err)
	}
	if port != 4242 || open != 2 {
		t.Fatalf("holdPort = %d with %d sockets open, want 4242 with 2", port, open)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if open != 0 {
		t.Errorf("%d sockets left open after Close", open)
	}
}

func TestHoldPort_ReleasesWhatItHeldOnFailure(t *testing.T) {
	open := 0
	holds := fakeNetwork(t, func(string) (io.Closer, error) {
		open++
		return closerFunc(func() error { open--; return nil }), nil
	})
	bindErr := errors.New("address already in use")
	refuses := fakeNetwork(t, func(string) (io.Closer, error) { return nil, bindErr })
	resolved, err := resolveProbes("127.0.0.1", []string{holds, refuses})
	if err != nil {
		t.Fatalf("resolveProbes: %v", err)
	}

	if _, _, err := holdPort("127.0.0.1", "4242", resolved); !errors.Is(err, bindErr) {
		t.Fatalf("err = %v, want the second probe's bind failure", err)
	}
	if open != 0 {
		t.Errorf("%d sockets left open after a failed batch", open)
	}
}

func TestHoldPort_UnreadableAddress(t *testing.T) {
	released := false
	probes["netport-test-unix"] = func(string) (net.Addr, io.Closer, error) {
		return &net.UnixAddr{Name: "no-port", Net: "unix"}, closerFunc(func() error { released = true; return nil }), nil
	}
	t.Cleanup(func() { delete(probes, "netport-test-unix") })
	resolved, err := resolveProbes("127.0.0.1", []string{"netport-test-unix"})
	if err != nil {
		t.Fatalf("resolveProbes: %v", err)
	}

	_, _, err = holdPort("127.0.0.1", kernelDrawsPort, resolved)
	var addrErr *ProbeAddrError
	if !errors.As(err, &addrErr) {
		t.Fatalf("err = %v, want *ProbeAddrError", err)
	}
	if addrErr.Addr != "no-port" || !released {
		t.Errorf("ProbeAddrError.Addr = %q, released = %v; want no-port, true", addrErr.Addr, released)
	}
}

func TestHoldPort_NonNumericPort(t *testing.T) {
	network := fakeNetwork(t, binds)
	probes[network] = func(string) (net.Addr, io.Closer, error) {
		return &net.UnixAddr{Name: "host:http", Net: "unix"}, closerFunc(func() error { return nil }), nil
	}
	resolved, err := resolveProbes("127.0.0.1", []string{network})
	if err != nil {
		t.Fatalf("resolveProbes: %v", err)
	}

	_, _, err = holdPort("127.0.0.1", kernelDrawsPort, resolved)
	var addrErr *ProbeAddrError
	if !errors.As(err, &addrErr) {
		t.Fatalf("err = %v, want *ProbeAddrError", err)
	}
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Errorf("err = %v, want it to unwrap to strconv.ErrSyntax", err)
	}
}
