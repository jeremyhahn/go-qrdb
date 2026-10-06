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
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

// mcpHandshake dials addr and completes a TLS handshake within timeout,
// returning the open connection or the handshake error.
func mcpHandshake(t *testing.T, addr string, timeout time.Duration) (*tls.Conn, error) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn := tls.Client(raw, &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = raw.Close()
		t.Fatalf("SetDeadline: %v", err)
	}
	if err := conn.Handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

// TestWithMCPMaxConnections_BoundsOpenConnections: the embedder's cap on the
// MCP listener's simultaneously open connections is enforced before TLS --
// with a cap of one, a second client cannot even complete its handshake while
// the first holds the only slot, and is served once that slot is released.
// Without the option the listener admits the second client at once.
func TestWithMCPMaxConnections_BoundsOpenConnections(t *testing.T) {
	const held = 500 * time.Millisecond // long enough for an admitted handshake on loopback
	start := func(t *testing.T, opts ...ServerOption) string {
		t.Helper()
		cfg := testConfigFull(t, protocolFlags{mcp: true}, "")
		srv := New(cfg, newMockKVService(), &mockAdminService{}, append([]ServerOption{WithCeremonyCA(testServerCA)}, opts...)...)
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		return srv.MCPAddr()
	}

	t.Run("default admits a second connection", func(t *testing.T) {
		addr := start(t)
		first, err := mcpHandshake(t, addr, 10*time.Second)
		if err != nil {
			t.Fatalf("first handshake: %v", err)
		}
		defer func() { _ = first.Close() }()
		second, err := mcpHandshake(t, addr, 10*time.Second)
		if err != nil {
			t.Fatalf("second handshake under the default cap: %v", err)
		}
		_ = second.Close()
	})

	t.Run("cap of one holds the second until the first closes", func(t *testing.T) {
		addr := start(t, WithMCPMaxConnections(1))
		first, err := mcpHandshake(t, addr, 10*time.Second)
		if err != nil {
			t.Fatalf("first handshake: %v", err)
		}

		var netErr net.Error
		if second, err := mcpHandshake(t, addr, held); err == nil {
			_ = second.Close()
			t.Fatal("a second connection completed its handshake while the only slot was held")
		} else if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("second handshake error = %v, want a timeout (never accepted)", err)
		}

		_ = first.Close()
		third, err := mcpHandshake(t, addr, 10*time.Second)
		if err != nil {
			t.Fatalf("handshake after the slot was released: %v", err)
		}
		_ = third.Close()
	})
}
