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

package server

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

// preboundBound bounds each wait in these tests. A failure bound only: every
// wait returns the moment its event happens.
const preboundBound = 10 * time.Second

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// TestLifetimeListener_EndsOneLifetimeOnly pins the property the wrapper
// exists for: closing one lifetime releases its blocked Accept and refuses
// further accepts, while the shared listener stays open and a second lifetime
// accepts on it.
func TestLifetimeListener_EndsOneLifetimeOnly(t *testing.T) {
	shared := listenTCP(t)

	first, err := newLifetimeListener("rest", shared)
	if err != nil {
		t.Fatalf("newLifetimeListener: %v", err)
	}
	accepted := make(chan error, 1)
	go func() {
		_, err := first.Accept()
		accepted <- err
	}()
	if err := first.Close(); err != nil {
		t.Fatalf("lifetime Close: %v", err)
	}
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("blocked Accept after lifetime Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(preboundBound):
		t.Fatal("lifetime Close did not release the blocked Accept")
	}
	if _, err := first.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept on a closed lifetime = %v, want net.ErrClosed", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second lifetime Close: %v", err)
	}

	second, err := newLifetimeListener("rest", shared)
	if err != nil {
		t.Fatalf("newLifetimeListener: %v", err)
	}
	if second.Addr().String() != shared.Addr().String() {
		t.Fatalf("Addr() = %s, want the shared %s", second.Addr(), shared.Addr())
	}
	got := make(chan error, 1)
	go func() {
		conn, err := second.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		got <- err
	}()
	conn, err := net.Dial("tcp", shared.Addr().String())
	if err != nil {
		t.Fatalf("dial the shared listener after a lifetime ended: %v", err)
	}
	_ = conn.Close()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("second lifetime Accept: %v", err)
		}
	case <-time.After(preboundBound):
		t.Fatal("the second lifetime never accepted: the first lifetime left the listener unusable")
	}
	_ = second.Close()
}

// noDeadlineListener is a listener without SetDeadline.
type noDeadlineListener struct{ net.Listener }

func TestLifetimeListener_RefusesAListenerWithoutDeadlines(t *testing.T) {
	_, err := newLifetimeListener("grpc", noDeadlineListener{listenTCP(t)})
	var want *PreboundSocketError
	if !errors.As(err, &want) || want.Protocol != "grpc" {
		t.Fatalf("err = %v, want *PreboundSocketError for grpc", err)
	}
}

func TestLifetimePacketConn_CloseLeavesTheSocketOpen(t *testing.T) {
	udp := listenUDP(t)

	wrapped := newLifetimePacketConn(udp)
	if _, ok := wrapped.(*lifetimeUDPConn); !ok {
		t.Fatalf("a *net.UDPConn must keep its concrete type visible, got %T", wrapped)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("lifetime Close: %v", err)
	}
	if _, err := udp.WriteTo([]byte{1}, udp.LocalAddr()); err != nil {
		t.Fatalf("the shared socket was closed by a lifetime Close: %v", err)
	}

	generic := newLifetimePacketConn(noUDP{udp})
	if _, ok := generic.(*lifetimePacketConn); !ok {
		t.Fatalf("a non-UDPConn must be wrapped generically, got %T", generic)
	}
	if err := generic.Close(); err != nil {
		t.Fatalf("generic lifetime Close: %v", err)
	}
}

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc.(*net.UDPConn)
}

// TestLifetimePacketConn_HandsTheSocketToTheNextLifetime pins what quic-go
// needs from a pre-bound UDP socket across lifetimes: it ends a server on a
// socket it did not create by moving the read deadline into the past and
// never moving it back. Closing the lifetime must release the blocked read,
// refuse further reads, and leave the socket with no deadline, so the next
// lifetime's read waits for — and receives — the next datagram.
func TestLifetimePacketConn_HandsTheSocketToTheNextLifetime(t *testing.T) {
	readBatch := func(pc net.PacketConn) error {
		ms := []ipv4.Message{{Buffers: [][]byte{make([]byte, 64)}}}
		_, err := pc.(*lifetimeUDPConn).ReadBatch(ms, 0)
		return err
	}
	readMsg := func(pc net.PacketConn) error {
		_, _, _, _, err := pc.(*lifetimeUDPConn).ReadMsgUDP(make([]byte, 64), make([]byte, 64))
		return err
	}
	readFrom := func(pc net.PacketConn) error {
		_, _, err := pc.ReadFrom(make([]byte, 64))
		return err
	}
	for _, tc := range []struct {
		name    string
		generic bool
		read    func(net.PacketConn) error
	}{
		{name: "udp ReadBatch", read: readBatch},
		{name: "udp ReadMsgUDP", read: readMsg},
		{name: "udp ReadFrom", read: readFrom},
		{name: "generic ReadFrom", generic: true, read: readFrom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			udp := listenUDP(t)
			wrap := func() net.PacketConn {
				if tc.generic {
					return newLifetimePacketConn(noUDP{udp})
				}
				return newLifetimePacketConn(udp)
			}

			first := wrap()
			read := make(chan error, 1)
			go func() { read <- tc.read(first) }()
			if err := first.Close(); err != nil {
				t.Fatalf("lifetime Close: %v", err)
			}
			select {
			case err := <-read:
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("blocked read after lifetime Close = %v, want net.ErrClosed", err)
				}
			case <-time.After(preboundBound):
				t.Fatal("lifetime Close did not release the blocked read")
			}
			if err := tc.read(first); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("read on a closed lifetime = %v, want net.ErrClosed", err)
			}
			// What quic-go leaves behind when its server on this socket
			// closes; the lifetime Close that follows must clear it.
			second := wrap()
			if err := udp.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatalf("SetReadDeadline: %v", err)
			}
			if err := second.Close(); err != nil {
				t.Fatalf("lifetime Close: %v", err)
			}

			third := wrap()
			got := make(chan error, 1)
			go func() { got <- tc.read(third) }()
			sender, err := net.Dial("udp", udp.LocalAddr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = sender.Close() }()
			if _, err := sender.Write([]byte("next")); err != nil {
				t.Fatalf("send: %v", err)
			}
			select {
			case err := <-got:
				if err != nil {
					t.Fatalf("the next lifetime's read failed: %v (an ended lifetime left a past deadline behind)", err)
				}
			case <-time.After(preboundBound):
				t.Fatal("the next lifetime never received the datagram")
			}
			_ = third.Close()
		})
	}
}

// noUDP hides the *net.UDPConn behind the interface.
type noUDP struct{ net.PacketConn }

// TestStart_RefusesPreboundSocketForDisabledProtocol pins the refusal and
// that the refused socket is closed rather than leaked.
func TestStart_RefusesPreboundSocketForDisabledProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flags    protocolFlags
		protocol string
	}{
		{name: "rest disabled", flags: protocolFlags{grpc: true, quic: true, mcp: true}, protocol: "rest"},
		{name: "grpc disabled", flags: protocolFlags{rest: true, quic: true, mcp: true}, protocol: "grpc"},
		{name: "quic disabled", flags: protocolFlags{rest: true, grpc: true, mcp: true}, protocol: "quic"},
		{name: "mcp disabled", flags: protocolFlags{rest: true, grpc: true, quic: true}, protocol: "mcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfigFull(t, tc.flags, "")
			cfg.Server.Host = "127.0.0.1"
			restLn, grpcLn, mcpLn, quicPC := listenTCP(t), listenTCP(t), listenTCP(t), listenUDP(t)
			srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA),
				WithRESTListener(restLn), WithGRPCListener(grpcLn),
				WithQUICPacketConn(quicPC), WithMCPListener(mcpLn))
			err := srv.Start()
			var want *PreboundSocketError
			if !errors.As(err, &want) || want.Protocol != tc.protocol {
				srv.Stop()
				t.Fatalf("Start = %v, want *PreboundSocketError for %s", err, tc.protocol)
			}
			for _, ln := range []net.Listener{restLn, grpcLn, mcpLn} {
				if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("a refused Start must close every pre-bound socket (Accept err = %v)", err)
				}
			}
			if err := quicPC.SetReadDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("a refused Start must close the pre-bound QUIC socket (SetReadDeadline err = %v)", err)
			}
		})
	}
}

// TestServer_StopKeepsPreboundSocketsForTheNextLifetime is B2 at the
// orchestrator: Stop ends a Server's use of its pre-bound listeners without
// closing them, so a second Server from the same options serves on them.
func TestServer_StopKeepsPreboundSocketsForTheNextLifetime(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, mcp: true}, "")
	cfg.Server.Host = "127.0.0.1"
	restLn, grpcLn, mcpLn, quicPC := listenTCP(t), listenTCP(t), listenTCP(t), listenUDP(t)
	opts := []ServerOption{WithCeremonyCA(testServerCA), WithRESTListener(restLn), WithGRPCListener(grpcLn),
		WithQUICPacketConn(quicPC), WithMCPListener(mcpLn)}

	for lifetime := 1; lifetime <= 2; lifetime++ {
		srv := New(cfg, newMockKVService(), &mockAdminService{}, opts...)
		if err := srv.Start(); err != nil {
			t.Fatalf("lifetime %d Start: %v", lifetime, err)
		}
		if srv.RESTAddr() != restLn.Addr().String() || srv.GRPCAddr() != grpcLn.Addr().String() ||
			srv.QUICAddr() != quicPC.LocalAddr().String() || srv.MCPAddr() != mcpLn.Addr().String() {
			t.Fatalf("lifetime %d did not serve on the pre-bound sockets", lifetime)
		}
		srv.Stop()
	}
	if got := len(PreboundSockets(opts...)); got != 4 {
		t.Fatalf("PreboundSockets = %d sockets, want 4", got)
	}
}

func TestPreboundSocketError_Error(t *testing.T) {
	err := &PreboundSocketError{Protocol: "rest", Reason: "the REST protocol is disabled"}
	if got, want := err.Error(), "server: pre-bound rest socket refused: the REST protocol is disabled"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
