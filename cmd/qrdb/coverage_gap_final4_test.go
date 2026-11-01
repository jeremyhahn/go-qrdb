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

//go:build !integration

// coverage_gap_final4_test.go covers remaining gaps to push cmd/qrdb
// statement coverage to ≥91% under -tags "codec_json raft".
package main

import (
	"context"
	"errors"
	"os"
	"testing"
)

// ===========================================================================
// bindings.go — IsConnected with manager path (50%)
// ===========================================================================

func TestConnectionBinding_IsConnected_WithManager_NoActiveConn(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	m := newTestConnectionManager(t)
	conn.SetManager(m)

	// No active connection in manager → IsConnected returns false.
	if conn.IsConnected() {
		t.Fatal("expected IsConnected()=false when manager has no active connection")
	}
	// ... and loadClient reports NotConnectedError.
	_, err := conn.loadClient()
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError from loadClient, got %T: %v", err, err)
	}
}

func TestConnectionBinding_IsConnected_WithManager_HasActiveConn(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	m := newTestConnectionManager(t)
	conn.SetManager(m)

	mockCl, err := newTestConnectionBinding(t, newMockKVService(), newMockAdminService()).loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}
	m.connections.Store("active-1", &managedConnection{
		profile: ConnectionProfile{ID: "active-1", Type: connectionTypeExternal},
		client:  mockCl,
	})
	m.activeID.Store("active-1")

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected()=true when manager has active connection")
	}
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient via manager: %v", err)
	}
	if cl != mockCl {
		t.Fatal("expected loadClient to return the manager's active client")
	}
}

func TestConnectionManager_Disconnect_WithEmbeddedServer(t *testing.T) {
	m := newTestConnectionManager(t)

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	mc := &managedConnection{
		profile: ConnectionProfile{ID: "emb-disc", Type: connectionTypeEmbedded},
		server:  NewEmbeddedServerBinding(log, conn, nil),
	}
	mc.storeState(ConnectionState{ProfileID: "emb-disc", Status: connectionStatusError})
	m.connections.Store("emb-disc", mc)

	if err := m.Disconnect("emb-disc"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, ok := m.connections.Load("emb-disc"); ok {
		t.Fatal("expected Disconnect to remove the embedded connection")
	}
}

// ===========================================================================
// bindings_daemon.go — resolveConfigDir (63%)
// ===========================================================================

// ===========================================================================
// bindings_daemon.go — Start when already running
// ===========================================================================

func TestDaemonBinding_Start_AlreadyRunning_Final4(t *testing.T) {
	d := newTestDaemonBinding(t)
	// Inject current process to simulate "already running" state.
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	d.process.Store(proc)

	// Start should return DaemonAlreadyRunningError.
	err = d.Start("/tmp/config.yaml")
	var dare *DaemonAlreadyRunningError
	if !errors.As(err, &dare) {
		t.Fatalf("expected DaemonAlreadyRunningError, got %T: %v", err, err)
	}
}

// ===========================================================================
// bindings_config.go — Initialized when connected via client (66%)
// ===========================================================================

func TestConfigBinding_Initialized_WithConnectedBinding(t *testing.T) {
	log := testLogger(t)
	// Create a ConnectionBinding with an actual connected client.
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	esb := NewEmbeddedServerBinding(log, conn, nil)
	cb := NewConfigBinding(log, esb, conn)

	// With a connected client, Initialized should return true.
	resp := cb.Initialized()
	if resp == nil {
		t.Fatal("expected non-nil ConfigInitializedResponse")
	}
	if !resp.Initialized {
		t.Fatal("expected Initialized=true when client is connected")
	}
}

// ===========================================================================
// bindings_connection_manager.go — Connect re-probe existing connection (59%)
// ===========================================================================

// workaround to avoid lint complaints about unused vars in final4
var _ = context.Background
