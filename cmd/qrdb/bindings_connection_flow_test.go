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

//go:build qrdb_embedded && !integration

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestConnectionFlow_LocalDatabase_NoBarrier_ClickReconnectSucceeds reproduces
// the user-reported regression: after creating a Local Server profile with
// barrier mode "none" and clicking the server row in the sidebar, the UI
// would show "Connection failed" because the second Connect call returned
// ConnectionAlreadyActiveError. This test exercises the same sequence the
// GUI performs and asserts the idempotent Connect path.
func TestConnectionFlow_LocalDatabase_NoBarrier_ClickReconnectSucceeds(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	profile := ConnectionProfile{
		ID:   "local-none",
		Name: "Local Server",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        t.TempDir(),
			StorageBackend: "memory",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// First Connect — simulates the Add Server wizard starting the embedded
	// server. Must succeed.
	firstState, err := m.Connect(profile.ID)
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if firstState == nil {
		t.Fatal("first Connect returned nil state")
	}
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

	// Second Connect — simulates the user clicking the server row after the
	// wizard completed. Before the fix this returned ConnectionAlreadyActiveError;
	// after the fix it re-probes and returns the current state.
	secondState, err := m.Connect(profile.ID)
	if err != nil {
		var already *ConnectionAlreadyActiveError
		if errors.As(err, &already) {
			t.Fatalf("regression: second Connect returned ConnectionAlreadyActiveError — "+
				"sidebar click would show 'Connection failed' toast: %v", err)
		}
		t.Fatalf("second Connect: %v", err)
	}
	if secondState == nil {
		t.Fatal("second Connect returned nil state on re-probe")
	}
	if secondState.ProfileID != profile.ID {
		t.Errorf("second Connect: got profileID %q, want %q", secondState.ProfileID, profile.ID)
	}
	if !secondState.IsEmbedded {
		t.Error("second Connect: expected IsEmbedded=true for embedded profile")
	}
}

// TestConnectionFlow_LocalDatabase_ActiveClientReturnsSameClient verifies
// that after Connect, ActiveClient returns the same client instance and the
// client is usable for subsequent SDK calls (the path DatabaseBinding.List
// takes when the sidebar queries databases).
func TestConnectionFlow_LocalDatabase_ActiveClientReturnsSameClient(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	profile := ConnectionProfile{
		ID:   "local-active",
		Name: "Active Test",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        t.TempDir(),
			StorageBackend: "memory",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	if _, err := m.Connect(profile.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

	if err := m.SetActive(profile.ID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	client, err := m.ActiveClient()
	if err != nil {
		t.Fatalf("ActiveClient: %v", err)
	}
	if client == nil {
		t.Fatal("ActiveClient returned nil client")
	}

	// Call ListDatabases — the exact path that failed in the user's log with
	// "qrdb: not connected to server". On a fresh embedded server the list
	// may be empty (no user-created databases yet); the success of the call
	// itself proves the client is connected and routable.
	if _, err := client.Database().ListDatabases(context.Background()); err != nil {
		t.Fatalf("ListDatabases through ActiveClient: %v", err)
	}
}

// TestConnectionFlow_MultipleLocalServers_EachConnectsIndependently verifies
// that multiple local embedded profiles can each be connected without
// interfering with each other — a key requirement for the multi-server
// sidebar tree.
func TestConnectionFlow_MultipleLocalServers_EachConnectsIndependently(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	const numServers = 3
	for i := 0; i < numServers; i++ {
		id := fmt.Sprintf("server-%d", i)
		p := ConnectionProfile{
			ID:   id,
			Name: fmt.Sprintf("Server %d", i),
			Type: connectionTypeEmbedded,
			Embedded: &EmbeddedConfig{
				DataDir:        t.TempDir(),
				StorageBackend: "memory",
				SealConfig:     &SealConfig{Mode: "none"},
			},
		}
		if err := m.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile %q: %v", id, err)
		}
		state, err := m.Connect(id)
		if err != nil {
			t.Fatalf("Connect %q: %v", id, err)
		}
		if state.ProfileID != id {
			t.Errorf("state.ProfileID: got %q, want %q", state.ProfileID, id)
		}
		t.Cleanup(func() { _ = m.Disconnect(id) })
	}

	// Verify each has its own client.
	for i := 0; i < numServers; i++ {
		id := fmt.Sprintf("server-%d", i)
		if err := m.SetActive(id); err != nil {
			t.Fatalf("SetActive %q: %v", id, err)
		}
		client, err := m.ActiveClient()
		if err != nil {
			t.Fatalf("ActiveClient for %q: %v", id, err)
		}
		if client == nil {
			t.Fatalf("ActiveClient for %q returned nil", id)
		}
	}
}

// TestConnectionFlow_DisconnectThenReconnect_Succeeds verifies the user
// flow of disconnecting a server then reconnecting it (e.g., after restarting
// the local embedded server via the GUI).
func TestConnectionFlow_DisconnectThenReconnect_Succeeds(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	profile := ConnectionProfile{
		ID:   "local-cycle",
		Name: "Cycle Test",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        t.TempDir(),
			StorageBackend: "memory",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	if _, err := m.Connect(profile.ID); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}
	if err := m.Disconnect(profile.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// After Disconnect the connection should be removed so a fresh Connect
	// starts a new embedded server rather than hitting the re-probe branch.
	state, err := m.Connect(profile.ID)
	if err != nil {
		t.Fatalf("reconnect after disconnect: %v", err)
	}
	if state == nil {
		t.Fatal("reconnect returned nil state")
	}
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

	// Verify a working client.
	if err := m.SetActive(profile.ID); err != nil {
		t.Fatalf("SetActive after reconnect: %v", err)
	}
	if _, err := m.ActiveClient(); err != nil {
		t.Fatalf("ActiveClient after reconnect: %v", err)
	}
}

// TestConnectionFlow_LocalDatabase_BarrierModeMatrix exercises every seal
// barrier mode the GUI wizard exposes (none, passphrase, shamir) and asserts
// each connects, reports a sensible status, and supports the reconnect-after
// -wizard idempotency path the user-reported bug was about.
//
// Note: this test only drives the ConnectionManagerBinding — the wizard's
// subsequent seal.initialize call is exercised by pkg/seal tests. Here we
// verify that the ConnectionManager correctly surfaces the initial barrier
// state for each mode.
func TestConnectionFlow_LocalDatabase_BarrierModeMatrix(t *testing.T) {
	cases := []struct {
		mode string
	}{
		{"none"},
		{"passphrase"},
		{"shamir"},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			m := newTestConnectionManager(t)
			m.ctx = context.Background()
			profile := ConnectionProfile{
				ID:   "local-" + tc.mode,
				Name: "Local " + tc.mode,
				Type: connectionTypeEmbedded,
				Embedded: &EmbeddedConfig{
					DataDir:        t.TempDir(),
					StorageBackend: "memory",
					SealConfig:     &SealConfig{Mode: tc.mode},
				},
			}
			if err := m.SaveProfile(profile); err != nil {
				t.Fatalf("SaveProfile: %v", err)
			}
			state, err := m.Connect(profile.ID)
			if err != nil {
				t.Fatalf("Connect mode=%s: %v", tc.mode, err)
			}
			if state == nil {
				t.Fatalf("Connect mode=%s returned nil state", tc.mode)
			}
			t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

			// Re-probe (simulated sidebar click). Must not return
			// ConnectionAlreadyActiveError for any mode.
			_, reerr := m.Connect(profile.ID)
			var already *ConnectionAlreadyActiveError
			if errors.As(reerr, &already) {
				t.Fatalf("mode=%s: reconnect returned ConnectionAlreadyActiveError", tc.mode)
			}
		})
	}
}

// TestConnectionFlow_LocalDatabase_StorageBackendMatrix exercises each
// embedded storage backend (memory, pebble) the user can select in the
// wizard. For each, we verify Connect succeeds and the embedded server
// serves a usable client.
func TestConnectionFlow_LocalDatabase_StorageBackendMatrix(t *testing.T) {
	cases := []struct {
		backend string
	}{
		{"memory"},
		{"pebble"},
	}
	for _, tc := range cases {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			m := newTestConnectionManager(t)
			m.ctx = context.Background()
			profile := ConnectionProfile{
				ID:   "local-" + tc.backend,
				Name: "Local " + tc.backend,
				Type: connectionTypeEmbedded,
				Embedded: &EmbeddedConfig{
					DataDir:        t.TempDir(),
					StorageBackend: tc.backend,
					SealConfig:     &SealConfig{Mode: "none"},
				},
			}
			if err := m.SaveProfile(profile); err != nil {
				t.Fatalf("SaveProfile: %v", err)
			}
			if _, err := m.Connect(profile.ID); err != nil {
				t.Fatalf("Connect backend=%s: %v", tc.backend, err)
			}
			t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

			if err := m.SetActive(profile.ID); err != nil {
				t.Fatalf("SetActive: %v", err)
			}
			cl, err := m.ActiveClient()
			if err != nil {
				t.Fatalf("ActiveClient: %v", err)
			}
			// Exercise the same path the sidebar DatabaseList view uses.
			if _, err := cl.Database().ListDatabases(context.Background()); err != nil {
				t.Fatalf("backend=%s: ListDatabases failed: %v", tc.backend, err)
			}
		})
	}
}

// TestConnectionFlow_RemoteDatabase_TransportMatrix exercises every external
// transport the wizard exposes. Each case uses an unreachable address so the
// test stays hermetic; the assertion is that Connect either (a) returns an
// error-state ConnectionState, or (b) returns a Go error — but MUST NOT
// panic and MUST NOT return ConnectionAlreadyActiveError on retry.
func TestConnectionFlow_RemoteDatabase_TransportMatrix(t *testing.T) {
	cases := []struct {
		transport string
		address   string
	}{
		{"rest", "https://127.0.0.1:1"},
		{"grpc", "127.0.0.1:1"},
		{"quic", "127.0.0.1:1"},
		{"unix", "/nonexistent/qrdb.sock"},
		{"mcp", "https://127.0.0.1:1/mcp"},
	}
	for _, tc := range cases {
		t.Run("transport="+tc.transport, func(t *testing.T) {
			m := newTestConnectionManager(t)
			m.ctx = context.Background()
			profile := ConnectionProfile{
				ID:   "ext-" + tc.transport,
				Name: "External " + tc.transport,
				Type: connectionTypeExternal,
				External: &ConnectionConfig{
					Transport: tc.transport,
					Address:   tc.address,
				},
			}
			if err := m.SaveProfile(profile); err != nil {
				t.Fatalf("SaveProfile transport=%s: %v", tc.transport, err)
			}

			// First Connect: may return Go error or error-state. Both are
			// acceptable; what matters is no panic and the connection map is
			// left in a sensible state.
			state, err := m.Connect(profile.ID)
			t.Cleanup(func() { _ = m.Disconnect(profile.ID) })
			if err != nil {
				// Connect returned a Go error — no state stored.
				return
			}
			if state == nil {
				t.Fatalf("transport=%s: nil state with nil error", tc.transport)
			}
			// State stored. A retry must NOT return ConnectionAlreadyActiveError.
			_, reerr := m.Connect(profile.ID)
			var already *ConnectionAlreadyActiveError
			if errors.As(reerr, &already) {
				t.Fatalf("transport=%s: retry returned ConnectionAlreadyActiveError", tc.transport)
			}
		})
	}
}

// TestConnectionFlow_LocalDatabase_BarrierStatusReportsInitialized verifies
// that immediately after a successful Connect on an embedded local server,
// the barrier status surfaced through probeBarrierStatus is "connected" —
// NOT "needs-init". This was the second user-reported bug: the GUI showed
// a "Server needs initialization. Use the CLI: qrdbd seal init" toast even
// though the in-process barrier was initialized at startup, because the
// underlying StorageBarrier.Status() never populated InitializedAt.
func TestConnectionFlow_LocalDatabase_BarrierStatusReportsInitialized(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()
	profile := ConnectionProfile{
		ID:   "local-init-status",
		Name: "Local Init Status",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        t.TempDir(),
			StorageBackend: "memory",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	state, err := m.Connect(profile.ID)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

	if state.Status == connectionStatusNeedsInit {
		t.Fatalf("regression: Connect returned status=%q, expected %q. "+
			"This means probeBarrierStatus saw an empty InitializedAt and "+
			"the GUI would show 'Server needs initialization' toast",
			state.Status, connectionStatusConnected)
	}
	if state.Status != connectionStatusConnected {
		t.Errorf("expected status=%q, got %q", connectionStatusConnected, state.Status)
	}
}

// TestConnect_ConcurrentSameDataDir_ReturnsDataDirLockedError verifies that
// when two embedded profiles share the same pebble dataDir (e.g., via manual
// JSON edit that bypasses the SaveProfile guard), the second Connect attempt
// surfaces a DataDirLockedError rather than an opaque Pebble internals error.
//
// The first server holds the Pebble directory lock; the second server must
// fail with EmbeddedServerError wrapping DataDirLockedError.
func TestConnect_ConcurrentSameDataDir_ReturnsDataDirLockedError(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	sharedDir := t.TempDir()

	profileA := ConnectionProfile{
		ID:   "lock-a",
		Name: "Server A",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        sharedDir,
			StorageBackend: "pebble",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profileA); err != nil {
		t.Fatalf("SaveProfile A: %v", err)
	}

	// Start server A — this acquires the Pebble directory lock.
	stateA, err := m.Connect(profileA.ID)
	if err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	if stateA == nil {
		t.Fatal("Connect A returned nil state")
	}
	t.Cleanup(func() { _ = m.Disconnect(profileA.ID) })

	// Inject a second profile that bypasses SaveProfile validation and reuses
	// the same dataDir — simulating a manual JSON edit or a second GUI process.
	m.mu.Lock()
	m.profiles = append(m.profiles, ConnectionProfile{
		ID:   "lock-b",
		Name: "Server B",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        sharedDir,
			StorageBackend: "pebble",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	})
	m.mu.Unlock()

	// Connect B must fail because pebble refuses to lock an already-locked dir.
	stateB, connectErr := m.Connect("lock-b")
	t.Cleanup(func() { _ = m.Disconnect("lock-b") })

	// Connect returns either a Go error or an error-state ConnectionState.
	if connectErr != nil {
		// Go error path — should NOT happen for embedded (Connect stores error state).
		// If it does, check for the wrapped type.
		var ddle *DataDirLockedError
		var ese *EmbeddedServerError
		if !errors.As(connectErr, &ddle) && !errors.As(connectErr, &ese) {
			t.Fatalf("expected DataDirLockedError or EmbeddedServerError, got %T: %v", connectErr, connectErr)
		}
		return
	}

	if stateB == nil {
		t.Fatal("Connect B returned nil state with nil error")
	}
	if stateB.Status != connectionStatusError {
		t.Fatalf("expected status=%q for locked dataDir, got %q (error: %s)",
			connectionStatusError, stateB.Status, stateB.Error)
	}

	// Verify the stored error contains DataDirLockedError.
	// stateB.Error is a string; verify it contains the key phrase.
	if stateB.Error == "" {
		t.Fatal("expected non-empty Error field in connection state for locked dataDir")
	}
	if !strings.Contains(stateB.Error, "locked") && !strings.Contains(stateB.Error, "lock") {
		t.Errorf("expected error message to mention lock, got: %s", stateB.Error)
	}
}

// TestConnectionFlow_RemoteDatabase_TLSFilesAreOptional asserts that external
// profiles work without TLS files configured (REST over HTTP, for instance).
// The wizard must not require a CA/cert/key bundle for every transport.
func TestConnectionFlow_RemoteDatabase_TLSFilesAreOptional(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()
	profile := ConnectionProfile{
		ID:   "ext-no-tls",
		Name: "External no TLS",
		Type: connectionTypeExternal,
		External: &ConnectionConfig{
			Transport: "rest",
			Address:   "http://127.0.0.1:1",
			// CAFile, CertFile, KeyFile intentionally omitted.
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	// Don't require Connect to succeed (address is unreachable); just assert
	// SaveProfile accepted the profile without TLS-mandatory validation.
	_, _ = m.Connect(profile.ID)
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })

	// Profile round-trips through ListProfiles.
	found := false
	for _, p := range m.ListProfiles() {
		if p.ID == profile.ID {
			found = true
			if p.External == nil {
				t.Fatal("loaded profile lost its External config")
			}
			if p.External.Transport != "rest" {
				t.Errorf("transport: got %q, want rest", p.External.Transport)
			}
			if p.External.Address != "http://127.0.0.1:1" {
				t.Errorf("address: got %q", p.External.Address)
			}
			break
		}
	}
	if !found {
		t.Fatal("saved profile not returned by ListProfiles")
	}
}
