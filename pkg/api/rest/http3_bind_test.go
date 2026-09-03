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

// Tests for the HTTP/3 bind address (go-qrdb#165).
//
// These assert against the address the kernel actually assigned each socket,
// not against the string the code computed, because the defect was precisely
// that the two transports ended up on different sockets while the logs
// reported both as started. The measurement on the reporting node was
// `ss -ltnup`: `udp UNCONN *:18443` beside `tcp LISTEN 127.0.0.1:18443`.
package rest

import (
	"errors"
	"net"
	"testing"
)

// startTLSServerOn starts a TLS REST server bound to addr and returns it,
// already registered for shutdown.
func startTLSServerOn(t *testing.T, addr string) *Server {
	t.Helper()
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, addr, WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start(%q) error: %v", addr, err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// TestDualStackAddrOn_ProbesAtTheCallersScope pins the property the helper
// exists for: the address it returns must be bindable, on BOTH protocols, AT
// THE SCOPE THE CALLER ASKED FOR.
//
// A helper that probed loopback and handed back a port for a wildcard bind
// would pass every assertion in this file and still let the wildcard test fail
// during setup, because a port free on 127.0.0.1 can be held on another local
// address. That is a check that cannot answer being reported as an answer.
func TestDualStackAddrOn_ProbesAtTheCallersScope(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			addr := dualStackAddrOn(t, host)

			// Same SCOPE, not the same string. Go normalises a "0.0.0.0:0"
			// bind to "[::]:port" on a dual-stack host, and both name every
			// interface. sameBindHost is the production rule for exactly this
			// equivalence, so the test asserts by the same rule the server
			// enforces rather than inventing a second one.
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatalf("SplitHostPort(%q): %v", addr, err)
			}
			want := net.JoinHostPort(host, port)
			if mismatch := sameBindHost(stubAddr(want), stubAddr(addr)); mismatch != nil {
				t.Fatalf("returned %q for a %q request: %v", addr, host, mismatch)
			}

			// Both protocols must actually bind there, right now.
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("TCP bind at %q failed: %v", addr, err)
			}
			defer func() { _ = ln.Close() }()
			pc, err := net.ListenPacket("udp", addr)
			if err != nil {
				t.Fatalf("UDP bind at %q failed: %v", addr, err)
			}
			_ = pc.Close()
		})
	}
}

// TestHTTP3_BindsSameHostAsTCPListener is the #165 assertion: with the REST
// server contained to loopback, the QUIC socket must be on loopback too. A
// wildcard UDP socket here is the reported exposure -- the same handler with
// the same TLS config, reachable on every interface the operator bound away
// from.
//
// It reads the bound socket rather than reproducing the reporter's `ss` probe
// from a second local address. The probe form needs an address such as
// 127.0.0.2 to be bindable, which is not true on every host, and a regression
// test for an exposure must not be the kind that can decline to run.
func TestHTTP3_BindsSameHostAsTCPListener(t *testing.T) {
	srv := startTLSServerOn(t, dualStackAddr(t))

	if srv.http3Conn == nil {
		t.Fatal("HTTP/3 socket was not opened; the QUIC listener is what this test is about")
	}
	tcpHost, _, err := net.SplitHostPort(srv.listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(TCP %q): %v", srv.listener.Addr(), err)
	}
	udpAddr := srv.http3Conn.LocalAddr()
	udpHost, udpPort, err := net.SplitHostPort(udpAddr.String())
	if err != nil {
		t.Fatalf("SplitHostPort(UDP %q): %v", udpAddr, err)
	}

	if udpHost != tcpHost {
		t.Fatalf("HTTP/3 bound host %q, TCP listener bound host %q: the bind address was honoured on one transport and discarded on the other (#165)", udpHost, tcpHost)
	}
	if ip := net.ParseIP(udpHost); ip == nil || ip.IsUnspecified() {
		t.Fatalf("HTTP/3 bound the wildcard host %q while the REST server was contained to %q", udpHost, tcpHost)
	}

	// The two transports must also share the port they advertise via Alt-Svc.
	_, tcpPort, err := net.SplitHostPort(srv.listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(TCP %q): %v", srv.listener.Addr(), err)
	}
	if udpPort != tcpPort {
		t.Fatalf("HTTP/3 port %q != REST port %q", udpPort, tcpPort)
	}
}

// TestHTTP3_WildcardBindStaysWildcard: containment is symmetric. A server that
// asked for every interface must get QUIC on every interface too, or traffic
// the operator asked to serve is silently dropped on one transport.
func TestHTTP3_WildcardBindStaysWildcard(t *testing.T) {
	// Probed AT WILDCARD SCOPE, because that is where this test binds. Taking
	// a port proven free on 127.0.0.1 and re-joining it to 0.0.0.0 would
	// prove the wrong thing: the port can be held on another local address
	// while loopback is free, and the wildcard bind would then fail during
	// setup and be reported as this test failing.
	srv := startTLSServerOn(t, dualStackAddrOn(t, "0.0.0.0"))

	if srv.http3Conn == nil {
		t.Fatal("HTTP/3 socket was not opened")
	}
	udpHost, _, err := net.SplitHostPort(srv.http3Conn.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(UDP %q): %v", srv.http3Conn.LocalAddr(), err)
	}
	if ip := net.ParseIP(udpHost); ip == nil || !ip.IsUnspecified() {
		t.Fatalf("HTTP/3 bound %q, want a wildcard host to match the REST listener", udpHost)
	}
}

// portAttempts bounds every port search in this file.
const portAttempts = 20

// errNoDualStackPort reports that no candidate was free on both protocols.
var errNoDualStackPort = errors.New("rest: no port free on both TCP and UDP")

// dualStackAddrOn returns an address on host whose port is free on BOTH TCP
// and UDP, for a server whose REST listener takes the port on TCP and whose
// HTTP/3 sibling takes the same number on UDP.
//
// THE PROBE BINDS AT THE HOST THE CALLER WILL BIND AT, and that is the point.
// A port proven free on 127.0.0.1 says nothing about the same port at
// wildcard scope: another process can hold it on a non-loopback local address
// while loopback stays free, so a wildcard bind then fails on a port the probe
// called free. That is go-qrdb#248's own thesis one axis over -- a free port
// in one protocol says nothing about another, and a free port at one bind
// SCOPE says nothing about a wider one (go-qrdb#253 review).
//
// The failure it prevents is not a wrong assertion; it is a setup failure
// that reads as a test failure -- a check that could not answer, reported as
// an answer.
func dualStackAddrOn(t *testing.T, host string) string {
	t.Helper()
	for i := 0; i < portAttempts; i++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			continue
		}
		addr := ln.Addr().String()
		pc, uerr := net.ListenPacket("udp", addr)
		_ = ln.Close()
		if uerr != nil {
			continue // UDP side taken at this scope; the number is unusable
		}
		_ = pc.Close()
		return addr
	}
	t.Fatalf("dualStackAddrOn(%q): %v after %d attempts", host, errNoDualStackPort, portAttempts)
	return ""
}

// dualStackAddr is dualStackAddrOn for the loopback scope, which is what most
// tests in this file bind.
func dualStackAddr(t *testing.T) string {
	t.Helper()
	return dualStackAddrOn(t, "127.0.0.1")
}

// reserveUDPLeavingTCPFree holds the UDP side of a port whose TCP side it has
// just proven free, and returns the held socket and the address.
//
// The proof is the point. The previous version bound UDP on :0 and commented
// that this left "its TCP side free" -- an assumption it never measured, and
// on a loaded host the TCP side of that number can already be held by another
// process (go-qrdb#253). Here the TCP side is bound first, which establishes
// it was free, and only then is UDP taken on the same number.
func reserveUDPLeavingTCPFree(t *testing.T) (net.PacketConn, string) {
	t.Helper()
	for i := 0; i < portAttempts; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		addr := ln.Addr().String()
		pc, uerr := net.ListenPacket("udp", addr)
		_ = ln.Close() // release TCP; the server under test will take it
		if uerr != nil {
			continue
		}
		return pc, addr
	}
	t.Fatalf("reserveUDPLeavingTCPFree: %v after %d attempts", errNoDualStackPort, portAttempts)
	return nil, ""
}

// TestHTTP3_BindFailureIsAStartError: the UDP socket is opened synchronously,
// so a port already taken by another process surfaces to the caller of Start
// instead of one slog line inside a goroutine. Under the old code the REST
// server reported success with no QUIC listener at all.
//
// The fixture has a race it cannot remove: between releasing the TCP side and
// the server binding it, another process can take it. That is not a reason to
// fail -- and crucially, the test does NOT have to guess whether it happened,
// because the error already says which socket lost:
//
//	*StartError      -> the TCP listener lost. The precondition did not hold,
//	                    so this run establishes nothing; retry.
//	*HTTP3StartError -> the TCP listener won and the QUIC bind failed, which
//	                    is exactly the property under test.
//
// go-qrdb#253 was this test reporting the first case as a failure of the
// second. The discriminator was sitting in the error the assertion already
// had.
func TestHTTP3_BindFailureIsAStartError(t *testing.T) {
	for attempt := 1; attempt <= portAttempts; attempt++ {
		blocker, addr := reserveUDPLeavingTCPFree(t)

		tlsCfg, _ := generateTestTLSConfig(t)
		srv := NewServer(&mockKVService{}, &mockAdminService{}, addr, WithTLS(tlsCfg))
		startErr := srv.Start()
		_ = blocker.Close()

		if startErr == nil {
			srv.Stop()
			t.Fatal("Start() returned nil with the HTTP/3 port already held; the QUIC bind failure was not reported")
		}

		var h3Err *HTTP3StartError
		if errors.As(startErr, &h3Err) {
			return // the property under test held
		}

		var tcpErr *StartError
		if errors.As(startErr, &tcpErr) {
			// The TCP side was taken between our release and the bind. The
			// precondition never held, so there was nothing to observe.
			t.Logf("attempt %d: TCP side of %s lost the race (%v); retrying", attempt, addr, startErr)
			continue
		}

		t.Fatalf("Start() error = %v (%T), want *HTTP3StartError or *StartError", startErr, startErr)
	}
	// Not a skip. AGENTS.md forbids skip guards, and a regression test for a
	// silent-failure path must not be able to decline to run -- reaching here
	// needs portAttempts independent losses of the TCP race, which means
	// something is wrong with the host, and saying so is more useful than
	// passing quietly.
	t.Fatalf("could not establish the precondition (TCP free, UDP held) in %d attempts: "+
		"every candidate port had its TCP side taken between release and bind, "+
		"which is a host problem rather than a failure of the behaviour under test", portAttempts)
}

// TestSameBindHost covers the invariant the runtime check enforces: the two
// sockets must be on the same host, and the only difference tolerated is two
// spellings of the wildcard, which Go itself produces.
func TestSameBindHost(t *testing.T) {
	tests := []struct {
		name     string
		tcp, udp string
		wantErr  bool
	}{
		{"identical loopback", "127.0.0.1:8443", "127.0.0.1:8443", false},
		{"identical wildcard", "0.0.0.0:8443", "0.0.0.0:8443", false},
		{"both wildcard, different families", "0.0.0.0:8443", "[::]:8443", false},
		// Two concrete hosts are a mismatch even with no wildcard in
		// sight: Alt-Svc points clients at the TCP authority, so a QUIC
		// socket on another address is simply unreachable to them. The
		// realistic producer is a name that resolves differently per
		// stack -- "localhost" as 127.0.0.1 for TCP and ::1 for UDP.
		{"both specific, different addresses", "127.0.0.1:8443", "10.0.0.1:8443", true},
		{"dual-stack loopback split", "127.0.0.1:8443", "[::1]:8443", true},
		{"specific TCP, wildcard UDP", "127.0.0.1:8443", "0.0.0.0:8443", true},
		{"wildcard TCP, specific UDP", "0.0.0.0:8443", "127.0.0.1:8443", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sameBindHost(stubAddr(tt.tcp), stubAddr(tt.udp))
			if tt.wantErr && err == nil {
				t.Fatalf("sameBindHost(%q, %q) = nil, want *ListenerHostMismatchError", tt.tcp, tt.udp)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("sameBindHost(%q, %q) = %v, want nil", tt.tcp, tt.udp, err)
			}
		})
	}
}

// stubAddr is a net.Addr whose String is fixed, for table-driven host checks
// that must not open sockets.
type stubAddr string

func (a stubAddr) Network() string { return "test" }
func (a stubAddr) String() string  { return string(a) }
