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

package mcp

import (
	"errors"
	"net"
	"testing"
	"time"
)

// TestServer_WithMaxConnections_SetsField mirrors the other WithX option
// tests: the option lands on the field Start reads.
func TestServer_WithMaxConnections_SetsField(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0", WithMaxConnections(7))
	if s.maxConnections != 7 {
		t.Errorf("maxConnections = %d, want 7", s.maxConnections)
	}
}

// TestServer_DefaultMaxConnections_Unset verifies that omitting the option
// leaves the field at zero, so Start falls back to DefaultMaxConnections
// rather than silently admitting zero connections.
func TestServer_DefaultMaxConnections_Unset(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0")
	if s.maxConnections != 0 {
		t.Errorf("maxConnections = %d, want 0 (falls back to DefaultMaxConnections in Start)", s.maxConnections)
	}
}

// TestServer_MaxConnections_BoundsListener proves the connection cap is
// enforced at the listener (go-qrdb#576): with WithMaxConnections(1), a
// second connection is never handed to the HTTP server while the first is
// still open, so it receives nothing before a short deadline elapses. Before
// the fix the MCP listener admitted an unbounded number of connections, each
// of which could hold open a request read ahead of authentication.
func TestServer_MaxConnections_BoundsListener(t *testing.T) {
	tlsCfg := testTLSConfig(t)
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithTLS(tlsCfg), WithMaxConnections(1))

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	addr := s.Addr()

	// Take the only slot and hold it open: a bare TCP connection is
	// sufficient, since the limiter counts Accept, not completed TLS
	// handshakes or HTTP requests.
	held, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer held.Close()

	// A second connection either sits unaccepted in the kernel backlog or is
	// accepted by the OS but never handed to net/http's Serve loop by
	// netutil.LimitListener, so it must never produce a byte of response.
	second, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer second.Close()

	if err := second.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1)
	_, err = second.Read(buf)
	if err == nil {
		t.Fatal("second connection was served past the configured limit of 1")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("second connection read = %v, want a read-deadline timeout (never served)", err)
	}
}

// TestServer_MaxConnsPerPeer_RefusesOverCapPeer proves WithMaxConnsPerPeer is
// enforced on the listener Start builds (go-qrdb#576): with a per-peer cap of
// 1, a second connection from the same IP is closed outright while the first
// is held, whereas without the option the same second connection is accepted
// and left waiting for its TLS ClientHello.
func TestServer_MaxConnsPerPeer_RefusesOverCapPeer(t *testing.T) {
	secondConnRead := func(t *testing.T, opts ...ServerOption) error {
		t.Helper()
		s := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
			append([]ServerOption{WithTLS(testTLSConfig(t))}, opts...)...)
		if err := s.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Stop()

		held, err := net.DialTimeout("tcp", s.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("first dial: %v", err)
		}
		defer held.Close()

		second, err := net.DialTimeout("tcp", s.Addr(), 2*time.Second)
		if err != nil {
			t.Fatalf("second dial: %v", err)
		}
		defer second.Close()
		if err := second.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		_, err = second.Read(make([]byte, 1))
		return err
	}

	err := secondConnRead(t, WithMaxConnsPerPeer(1))
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("capped: second read = %v, want the connection closed by the server", err)
	}

	err = secondConnRead(t)
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("uncapped: second read = %v, want a read-deadline timeout (accepted, awaiting TLS)", err)
	}
}
