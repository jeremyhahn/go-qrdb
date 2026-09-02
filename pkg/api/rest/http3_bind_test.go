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
	srv := startTLSServerOn(t, "127.0.0.1:0")

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
	srv := startTLSServerOn(t, "0.0.0.0:0")

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

// TestHTTP3_BindFailureIsAStartError: the UDP socket is opened synchronously,
// so a port already taken by another process surfaces to the caller of Start
// instead of one slog line inside a goroutine. Under the old code the REST
// server reported success with no QUIC listener at all.
func TestHTTP3_BindFailureIsAStartError(t *testing.T) {
	// Hold the UDP side of a port while leaving its TCP side free, so the
	// TCP listener succeeds and only the QUIC bind can fail.
	blocker, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve UDP port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	_, port, err := net.SplitHostPort(blocker.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", blocker.LocalAddr(), err)
	}

	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, net.JoinHostPort("127.0.0.1", port), WithTLS(tlsCfg))
	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("Start() returned nil with the HTTP/3 port already held")
	}
	var h3Err *HTTP3StartError
	if !errors.As(startErr, &h3Err) {
		t.Fatalf("Start() error = %v (%T), want *HTTP3StartError", startErr, startErr)
	}
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
