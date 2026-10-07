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

// Tests for pre-bound REST sockets (go-qrdb#374): an embedder that reserved a
// kernel-assigned port hands the bound socket over instead of its number, so
// nothing can take the port between reservation and serving.
package rest

import (
	"errors"
	"net"
	"testing"
)

// listenLoopbackTCP binds a kernel-assigned loopback TCP port.
func listenLoopbackTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	return ln
}

// bindUDPOnDifferentPort binds a loopback UDP socket on an ephemeral port
// and retries until that port is not avoidPort. TCP and UDP have separate
// kernel port spaces, so the ephemeral allocator can hand UDP the same
// number already held by a TCP listener -- that happened under full-suite
// load and made this collision case un-exercisable (go-qrdb#610). Retrying
// is deterministic: the ephemeral range has thousands of other ports, so a
// repeat collision within a few attempts would itself indicate a bug.
func bindUDPOnDifferentPort(t *testing.T, avoidPort string) net.PacketConn {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("bind udp: %v", err)
		}
		if portOfAddr(pc.LocalAddr()) != avoidPort {
			return pc
		}
		_ = pc.Close()
	}
	t.Fatalf("could not obtain a udp port different from %s after 20 attempts", avoidPort)
	return nil
}

// assertClosed reports whether a socket was closed by trying to use it.
func assertListenerClosed(t *testing.T, ln net.Listener) {
	t.Helper()
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener still open after the server owned it (Accept err = %v)", err)
	}
}

func assertPacketConnClosed(t *testing.T, pc net.PacketConn) {
	t.Helper()
	if _, err := pc.WriteTo([]byte{0}, pc.LocalAddr()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("packet conn still open after the server owned it (WriteTo err = %v)", err)
	}
}

// TestWithListener_ServesOnTheHandedOverSocket pins that Start binds nothing
// for TCP: the server's address is the listener's, even though the configured
// address names a different (unbindable) port, and HTTP/3 lands beside it.
func TestWithListener_ServesOnTheHandedOverSocket(t *testing.T) {
	ln := listenLoopbackTCP(t)
	tlsCfg, _ := generateTestTLSConfig(t)
	// A configured address that would fail to bind proves it is not used.
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "256.0.0.1:1", WithTLS(tlsCfg), WithListener(ln))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got, want := srv.Addr(), ln.Addr().String(); got != want {
		t.Fatalf("Addr() = %q, want the handed-over listener %q", got, want)
	}
	if srv.http3Conn == nil || portOfAddr(srv.http3Conn.LocalAddr()) != portOfAddr(ln.Addr()) {
		t.Fatalf("HTTP/3 must be bound on the listener's port %s", portOfAddr(ln.Addr()))
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial the served listener: %v", err)
	}
	_ = conn.Close()
}

// TestWithPacketConn_ServesHTTP3OnTheHandedOverSocket pins the UDP half.
func TestWithPacketConn_ServesHTTP3OnTheHandedOverSocket(t *testing.T) {
	ln := listenLoopbackTCP(t)
	pc, err := net.ListenPacket("udp", ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("bind udp beside the listener: %v", err)
	}
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "256.0.0.1:1", WithTLS(tlsCfg), WithListener(ln), WithPacketConn(pc))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if srv.http3Conn != pc {
		t.Fatal("HTTP/3 must serve on the handed-over packet conn")
	}
	srv.Stop()
	assertPacketConnClosed(t, pc)
}

// TestWithPacketConn_Refusals pins the two ways a pre-bound HTTP/3 socket is
// refused, and that the server closes what it was handed on refusal.
func TestWithPacketConn_Refusals(t *testing.T) {
	t.Run("without a listener", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("bind udp: %v", err)
		}
		tlsCfg, _ := generateTestTLSConfig(t)
		srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(tlsCfg), WithPacketConn(pc))
		err = srv.Start()
		var want *PacketConnWithoutListenerError
		if !errors.As(err, &want) {
			t.Fatalf("Start = %v, want *PacketConnWithoutListenerError", err)
		}
		assertPacketConnClosed(t, pc)
	})
	t.Run("on another port", func(t *testing.T) {
		ln := listenLoopbackTCP(t)
		pc := bindUDPOnDifferentPort(t, portOfAddr(ln.Addr()))
		tlsCfg, _ := generateTestTLSConfig(t)
		srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(tlsCfg), WithListener(ln), WithPacketConn(pc))
		err := srv.Start()
		var want *ListenerPortMismatchError
		if !errors.As(err, &want) {
			t.Fatalf("Start = %v, want *ListenerPortMismatchError", err)
		}
		assertListenerClosed(t, ln)
		assertPacketConnClosed(t, pc)
	})
	t.Run("start refused before serving closes the handed-over sockets", func(t *testing.T) {
		ln := listenLoopbackTCP(t)
		srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithListener(ln))
		err := srv.Start()
		var want *TLSNotConfiguredError
		if !errors.As(err, &want) {
			t.Fatalf("Start = %v, want *TLSNotConfiguredError", err)
		}
		assertListenerClosed(t, ln)
	})
}

func TestPrebindErrors(t *testing.T) {
	if got, want := (&ListenerPortMismatchError{TCPPort: "1", UDPPort: "2"}).Error(),
		"rest: HTTP/3 socket port 2 does not match REST listener port 1; Alt-Svc advertises HTTP/3 on the listener's port"; got != want {
		t.Fatalf("ListenerPortMismatchError = %q, want %q", got, want)
	}
	if got, want := (&PacketConnWithoutListenerError{}).Error(), "rest: WithPacketConn requires WithListener"; got != want {
		t.Fatalf("PacketConnWithoutListenerError = %q, want %q", got, want)
	}
}
