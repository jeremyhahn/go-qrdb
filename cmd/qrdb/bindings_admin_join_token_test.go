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

package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// activateTestConnection registers conn as the manager's active connection,
// standing in for the dial a real profile connect performs.
func activateTestConnection(m *ConnectionManagerBinding, conn *managedConnection) {
	m.connections.Store(conn.profile.ID, conn)
	m.activeID.Store(conn.profile.ID)
}

func TestAdminBinding_MintJoinToken_RemoteOnly(t *testing.T) {
	const wantToken = "join-token-1"
	expires := time.Unix(1_900_000_000, 0).UTC()
	var gotNode string
	var gotTTL time.Duration
	adminSvc := &fnAdminService{
		mintJoinTokenFn: func(_ context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
			gotNode, gotTTL = nodeID, ttl
			return wantToken, expires, nil
		},
	}
	conn := newTestConnectionBinding(t, newMockKVService(), adminSvc)
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}
	b := NewAdminBinding(testLogger(t), conn)

	t.Run("no connection manager", func(t *testing.T) {
		if b.JoinTokenMintable() {
			t.Fatal("JoinTokenMintable = true without a connection manager; want false")
		}
		if resp, err := b.MintJoinToken("node-4", 60); !errors.Is(err, ErrRemoteConnectionRequired) || resp != nil {
			t.Fatalf("MintJoinToken = %+v, %v; want ErrRemoteConnectionRequired", resp, err)
		}
	})

	t.Run("no active connection", func(t *testing.T) {
		m := NewConnectionManagerBinding(testLogger(t))
		conn.SetManager(m)
		if b.JoinTokenMintable() {
			t.Fatal("JoinTokenMintable = true with no active connection; want false")
		}
		var notConnected *NotConnectedError
		if _, err := b.MintJoinToken("node-4", 60); !errors.As(err, &notConnected) {
			t.Fatalf("MintJoinToken err = %v; want NotConnectedError", err)
		}
	})

	t.Run("embedded connection refused", func(t *testing.T) {
		m := NewConnectionManagerBinding(testLogger(t))
		activateTestConnection(m, &managedConnection{
			profile: ConnectionProfile{ID: "embedded"},
			client:  cl,
			server:  &EmbeddedServerBinding{},
		})
		conn.SetManager(m)
		if b.JoinTokenMintable() {
			t.Fatal("JoinTokenMintable = true on the embedded connection; want false")
		}
		if resp, err := b.MintJoinToken("node-4", 60); !errors.Is(err, ErrRemoteConnectionRequired) || resp != nil {
			t.Fatalf("MintJoinToken = %+v, %v; want ErrRemoteConnectionRequired", resp, err)
		}
		if gotNode != "" {
			t.Fatalf("embedded refusal reached the admin service (node %q)", gotNode)
		}
	})

	t.Run("profile without external config refused", func(t *testing.T) {
		m := NewConnectionManagerBinding(testLogger(t))
		activateTestConnection(m, &managedConnection{
			profile: ConnectionProfile{ID: "bare"},
			client:  cl,
		})
		conn.SetManager(m)
		if b.JoinTokenMintable() {
			t.Fatal("JoinTokenMintable = true without an external config; want false")
		}
		if _, err := b.MintJoinToken("node-4", 60); !errors.Is(err, ErrRemoteConnectionRequired) {
			t.Fatalf("MintJoinToken err = %v; want ErrRemoteConnectionRequired", err)
		}
	})

	t.Run("external connection allowed", func(t *testing.T) {
		m := NewConnectionManagerBinding(testLogger(t))
		activateTestConnection(m, &managedConnection{
			profile: ConnectionProfile{ID: "remote", External: &ConnectionConfig{}},
			client:  cl,
		})
		conn.SetManager(m)
		if !b.JoinTokenMintable() {
			t.Fatal("JoinTokenMintable = false on an external connection; want true")
		}
		resp, err := b.MintJoinToken("node-4", 90)
		if err != nil {
			t.Fatalf("MintJoinToken: %v", err)
		}
		if resp == nil || resp.Token != wantToken || !resp.ExpiresAt.Equal(expires) {
			t.Fatalf("MintJoinToken = %+v; want token %q expiring %v", resp, wantToken, expires)
		}
		if gotNode != "node-4" || gotTTL != 90*time.Second {
			t.Fatalf("admin service got node %q ttl %v; want node-4 90s", gotNode, gotTTL)
		}
	})
}
