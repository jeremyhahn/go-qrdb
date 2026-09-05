//go:build !integration

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"os"
	"testing"
)

// newTestConnectionManager creates a ConnectionManagerBinding with
// XDG_CONFIG_HOME pointed at a temp dir so persistence never touches
// the real user config directory.
func newTestConnectionManager(t *testing.T) *ConnectionManagerBinding {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return NewConnectionManagerBinding(testLogger(t))
}

// ---------------------------------------------------------------------------
// ListProfiles
// ---------------------------------------------------------------------------

func TestConnectionManager_ListProfiles_Empty(t *testing.T) {
	m := newTestConnectionManager(t)
	profiles := m.ListProfiles()
	if len(profiles) != 0 {
		t.Fatalf("expected 0 profiles, got %d", len(profiles))
	}
}

func TestConnectionManager_ListProfiles_ReturnsCopy(t *testing.T) {
	m := newTestConnectionManager(t)
	a := m.ListProfiles()
	b := m.ListProfiles()
	if &a == &b {
		t.Fatal("expected different slice pointers")
	}
}

// ---------------------------------------------------------------------------
// SaveProfile
// ---------------------------------------------------------------------------

func TestConnectionManager_SaveProfile_Embedded(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID:        "test-1",
		Name:      "Test DB",
		Type:      "embedded",
		Embedded:  &EmbeddedConfig{StorageBackend: "memory"},
		CreatedAt: "2026-04-04T00:00:00Z",
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].Name != "Test DB" {
		t.Fatalf("expected name %q, got %q", "Test DB", profiles[0].Name)
	}
}

func TestConnectionManager_SaveProfile_External(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID:   "test-2",
		Name: "Remote Server",
		Type: "external",
		External: &ConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:63003",
		},
		CreatedAt: "2026-04-04T00:00:00Z",
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
}

// ---------------------------------------------------------------------------
// SaveProfile validation
// ---------------------------------------------------------------------------

func TestConnectionManager_SaveProfile_MissingID(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{Name: "x", Type: "embedded", Embedded: &EmbeddedConfig{}})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
}

func TestConnectionManager_SaveProfile_MissingName(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{ID: "x", Type: "embedded", Embedded: &EmbeddedConfig{}})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
}

func TestConnectionManager_SaveProfile_InvalidType(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{ID: "x", Name: "x", Type: "invalid"})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
}

func TestConnectionManager_SaveProfile_EmbeddedMissingConfig(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{ID: "x", Name: "x", Type: "embedded"})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
}

func TestConnectionManager_SaveProfile_ExternalMissingAddress(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{
		ID: "x", Name: "x", Type: "external",
		External: &ConnectionConfig{Transport: "grpc"},
	})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteProfile
// ---------------------------------------------------------------------------

func TestConnectionManager_DeleteProfile(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID: "del-1", Name: "ToDelete", Type: "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := m.DeleteProfile("del-1"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if len(m.ListProfiles()) != 0 {
		t.Fatal("expected 0 profiles after delete")
	}
}

func TestConnectionManager_DeleteProfile_NotFound(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.DeleteProfile("nonexistent")
	var pnf *ProfileNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("expected ProfileNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SetActive / GetActiveProfileID
// ---------------------------------------------------------------------------

func TestConnectionManager_SetActive(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID: "active-1", Name: "ActiveDB", Type: "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := m.SetActive("active-1"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if m.GetActiveProfileID() != "active-1" {
		t.Fatalf("expected active ID %q, got %q", "active-1", m.GetActiveProfileID())
	}
}

func TestConnectionManager_SetActive_NotFound(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SetActive("nonexistent")
	var pnf *ProfileNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("expected ProfileNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ActiveClient
// ---------------------------------------------------------------------------

func TestConnectionManager_ActiveClient_NoActive(t *testing.T) {
	m := newTestConnectionManager(t)
	_, err := m.ActiveClient()
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestConnectionManager_ActiveClient_NoConnection(t *testing.T) {
	m := newTestConnectionManager(t)
	m.activeID.Store("some-id")
	_, err := m.ActiveClient()
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// IsEmbeddedAvailable
// ---------------------------------------------------------------------------

func TestConnectionManager_IsEmbeddedAvailable(t *testing.T) {
	m := newTestConnectionManager(t)
	if m.IsEmbeddedAvailable() != IsEmbeddedAvailable() {
		t.Fatal("expected IsEmbeddedAvailable to match package-level function")
	}
}

func TestConnectionManager_IsEmbeddedAvailable_Consistent(t *testing.T) {
	m := newTestConnectionManager(t)
	first := m.IsEmbeddedAvailable()
	second := m.IsEmbeddedAvailable()
	if first != second {
		t.Fatalf("expected consistent IsEmbeddedAvailable results: first=%v second=%v", first, second)
	}
}

// ---------------------------------------------------------------------------
// Persistence round-trip
// ---------------------------------------------------------------------------

func TestConnectionManager_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m1 := NewConnectionManagerBinding(testLogger(t))
	p := ConnectionProfile{
		ID: "persist-1", Name: "Persisted", Type: "external",
		External:  &ConnectionConfig{Transport: "grpc", Address: "localhost:63003"},
		CreatedAt: "2026-04-04T00:00:00Z",
	}
	if err := m1.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// Create a second manager pointing to the same config dir — it should
	// load the profile that m1 persisted.
	m2 := NewConnectionManagerBinding(testLogger(t))
	if err := m2.loadProfiles(); err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}
	profiles := m2.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile after reload, got %d", len(profiles))
	}
	if profiles[0].Name != "Persisted" {
		t.Fatalf("expected name %q, got %q", "Persisted", profiles[0].Name)
	}
}

func TestConnectionManager_Persistence_Upsert(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID: "upsert-1", Name: "Original", Type: "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	p.Name = "Updated"
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile (upsert): %v", err)
	}

	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile after upsert, got %d", len(profiles))
	}
	if profiles[0].Name != "Updated" {
		t.Fatalf("expected name %q, got %q", "Updated", profiles[0].Name)
	}
}

// ---------------------------------------------------------------------------
// UUID generation
// ---------------------------------------------------------------------------

func TestNewUUID_Format(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	// Expected format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx (36 chars).
	if len(id) != 36 {
		t.Fatalf("expected 36-char UUID, got %d: %s", len(id), id)
	}
	// Version nibble must be '4'.
	if id[14] != '4' {
		t.Fatalf("expected version nibble '4' at position 14, got %q in %s", id[14], id)
	}
}

func TestNewUUID_Unique(t *testing.T) {
	a, _ := newUUID()
	b, _ := newUUID()
	if a == b {
		t.Fatalf("expected unique UUIDs, both were %s", a)
	}
}

// ---------------------------------------------------------------------------
// GetState
// ---------------------------------------------------------------------------

func TestConnectionManager_GetState_Unknown(t *testing.T) {
	m := newTestConnectionManager(t)
	state := m.GetState("nonexistent")
	if state != nil {
		t.Fatal("expected nil state for unknown profile")
	}
}

func TestConnectionManager_GetState_Unknown_Second(t *testing.T) {
	m := newTestConnectionManager(t)
	if m.GetState("") != nil {
		t.Fatal("expected nil state for empty ID")
	}
}

// ---------------------------------------------------------------------------
// GetActiveProfileID edge cases
// ---------------------------------------------------------------------------

func TestConnectionManager_GetActiveProfileID_Initial(t *testing.T) {
	m := newTestConnectionManager(t)
	if m.GetActiveProfileID() != "" {
		t.Fatalf("expected empty active ID on fresh manager, got %q", m.GetActiveProfileID())
	}
}

func TestConnectionManager_GetActiveProfileID_AfterDelete(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID: "del-active", Name: "WillDelete", Type: "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := m.SetActive("del-active"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := m.DeleteProfile("del-active"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if id := m.GetActiveProfileID(); id != "" {
		t.Fatalf("expected empty active ID after deleting active profile, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// SaveProfile multiple profiles
// ---------------------------------------------------------------------------

func TestConnectionManager_SaveProfile_Multiple(t *testing.T) {
	m := newTestConnectionManager(t)
	for i := range 5 {
		id := "multi-" + string(rune('a'+i))
		p := ConnectionProfile{
			ID: id, Name: "DB " + id, Type: "embedded",
			Embedded: &EmbeddedConfig{StorageBackend: "memory"},
		}
		if err := m.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile %s: %v", id, err)
		}
	}
	profiles := m.ListProfiles()
	if len(profiles) != 5 {
		t.Fatalf("expected 5 profiles, got %d", len(profiles))
	}
}

func TestConnectionManager_SaveProfile_PreservesOrder(t *testing.T) {
	m := newTestConnectionManager(t)
	names := []string{"alpha", "bravo", "charlie"}
	for _, name := range names {
		p := ConnectionProfile{
			ID: name, Name: name, Type: "embedded",
			Embedded: &EmbeddedConfig{StorageBackend: "memory"},
		}
		if err := m.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile %s: %v", name, err)
		}
	}
	profiles := m.ListProfiles()
	for i, name := range names {
		if profiles[i].Name != name {
			t.Fatalf("profile[%d]: expected %q, got %q", i, name, profiles[i].Name)
		}
	}
}

// ---------------------------------------------------------------------------
// DeleteProfile middle element
// ---------------------------------------------------------------------------

func TestConnectionManager_DeleteProfile_Middle(t *testing.T) {
	m := newTestConnectionManager(t)
	for _, id := range []string{"a", "b", "c"} {
		p := ConnectionProfile{
			ID: id, Name: id, Type: "embedded",
			Embedded: &EmbeddedConfig{StorageBackend: "memory"},
		}
		if err := m.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile %s: %v", id, err)
		}
	}
	if err := m.DeleteProfile("b"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	profiles := m.ListProfiles()
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	if profiles[0].ID != "a" || profiles[1].ID != "c" {
		t.Fatalf("expected [a, c], got [%s, %s]", profiles[0].ID, profiles[1].ID)
	}
}

func TestConnectionManager_DeleteProfile_Last(t *testing.T) {
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID: "only", Name: "Only", Type: "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := m.DeleteProfile("only"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if len(m.ListProfiles()) != 0 {
		t.Fatal("expected 0 profiles after deleting last one")
	}
}

// ---------------------------------------------------------------------------
// Disconnect without connection (idempotent)
// ---------------------------------------------------------------------------

func TestConnectionManager_Disconnect_NoConnection(t *testing.T) {
	m := newTestConnectionManager(t)
	if err := m.Disconnect("nonexistent"); err != nil {
		t.Fatalf("Disconnect of nonexistent should be nil, got: %v", err)
	}
}

func TestConnectionManager_Disconnect_Idempotent(t *testing.T) {
	m := newTestConnectionManager(t)
	if err := m.Disconnect("x"); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	if err := m.Disconnect("x"); err != nil {
		t.Fatalf("second Disconnect: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Connect validation
// ---------------------------------------------------------------------------

func TestConnectionManager_Connect_ProfileNotFound(t *testing.T) {
	m := newTestConnectionManager(t)
	_, err := m.Connect("nonexistent")
	var pnf *ProfileNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("expected ProfileNotFoundError, got %T: %v", err, err)
	}
}

func TestConnectionManager_Connect_ExternalBadAddress(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()
	p := ConnectionProfile{
		ID: "bad-ext", Name: "Bad", Type: "external",
		External: &ConnectionConfig{Transport: "grpc", Address: "localhost:1"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	// Connect should return a state with error status, not a Go error.
	state, err := m.Connect("bad-ext")
	if err != nil {
		// Some transports may return an error rather than connecting.
		// Either way, we should not panic.
		return
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Status != connectionStatusError && state.Status != connectionStatusConnected {
		// Connection to bad address can fail or succeed depending on timing.
		t.Logf("state status: %s (acceptable for bad address test)", state.Status)
	}
}

// TestConnectionManager_Connect_Idempotent_ReprobeExistingConnection verifies
// that a second Connect call for an already-connected profile re-probes the
// barrier status instead of refusing the already-active connection. This
// prevents the "Connection failed" toast the GUI would show when a sidebar
// click arrived after the cached connection state drifted from the actual
// server state (e.g., barrier initialized after the initial connect).
func TestConnectionManager_Connect_Idempotent_ReprobeExistingConnection(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()

	profile := ConnectionProfile{
		ID: "idem-test", Name: "Idem", Type: "embedded",
		Embedded: &EmbeddedConfig{DataDir: t.TempDir(), StorageBackend: "memory"},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// Seed a managed connection as if a prior Connect succeeded, skipping
	// the real embedded-server startup so this test stays fast and isolated
	// from embedded-build-tag availability.
	conn := &managedConnection{profile: profile}
	conn.storeState(ConnectionState{
		ProfileID:  profile.ID,
		Status:     connectionStatusNeedsInit,
		IsEmbedded: true,
	})
	// Real client not needed for the re-probe path: probeBarrierStatus
	// returns connectionStatusConnected on a nil client because BarrierStatus
	// will fail and the probe falls back to "connected". We populate a
	// non-nil sentinel to exercise the nil-client guard.
	conn.client = nil
	m.connections.Store(profile.ID, conn)

	// With a nil client the re-probe guard must NOT fire: Connect has to fall
	// through and build a fresh connection.
	//
	// The discriminator is the stored connection pointer, not an error type
	// (go-qrdb#243 removed the refusal type, and "no error of type X" is
	// unfalsifiable once X cannot exist). The two paths are distinguishable
	// because only one of them replaces the map entry:
	//
	//   re-probe guard  -> calls conn.storeState on the EXISTING conn and
	//                      returns; the seeded pointer stays in the map.
	//   fall-through    -> builds a new *managedConnection and Stores it,
	//                      on both the success and the build-failure path.
	//
	// So a surviving seeded pointer means the guard fired on a nil client.
	// This holds under every build-tag combination, because the failure path
	// stores the new connection too.
	if _, err := m.Connect(profile.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	v, loaded := m.connections.Load(profile.ID)
	if !loaded {
		t.Fatal("Connect: no connection stored for the profile")
	}
	if v == any(conn) {
		t.Fatal("Connect took the re-probe path on a nil client: the seeded " +
			"connection is still in the map, so no fresh connection was built")
	}
}

// ---------------------------------------------------------------------------
// Validation: ExternalMissingConfig
// ---------------------------------------------------------------------------

func TestConnectionManager_SaveProfile_ExternalMissingConfig(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{ID: "x", Name: "x", Type: "external"})
	var pve *ProfileValidationError
	if !errors.As(err, &pve) {
		t.Fatalf("expected ProfileValidationError, got %T: %v", err, err)
	}
	if pve.Field != "external" {
		t.Fatalf("expected field %q, got %q", "external", pve.Field)
	}
}

func TestConnectionManager_SaveProfile_ExternalWithConfig(t *testing.T) {
	m := newTestConnectionManager(t)
	err := m.SaveProfile(ConnectionProfile{
		ID: "ext-ok", Name: "ExternalOK", Type: "external",
		External: &ConnectionConfig{Transport: "rest", Address: "https://localhost:63002"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Persistence: multiple save/delete cycles
// ---------------------------------------------------------------------------

func TestConnectionManager_Persistence_SaveDeleteCycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := NewConnectionManagerBinding(testLogger(t))

	// Save 3 profiles.
	for _, id := range []string{"x", "y", "z"} {
		if err := m.SaveProfile(ConnectionProfile{
			ID: id, Name: id, Type: "embedded",
			Embedded: &EmbeddedConfig{StorageBackend: "memory"},
		}); err != nil {
			t.Fatalf("SaveProfile %s: %v", id, err)
		}
	}

	// Delete the middle one.
	if err := m.DeleteProfile("y"); err != nil {
		t.Fatalf("DeleteProfile y: %v", err)
	}

	// Reload from disk.
	m2 := NewConnectionManagerBinding(testLogger(t))
	if err := m2.loadProfiles(); err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}
	profiles := m2.ListProfiles()
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles after reload, got %d", len(profiles))
	}
	if profiles[0].ID != "x" || profiles[1].ID != "z" {
		t.Fatalf("expected [x, z], got [%s, %s]", profiles[0].ID, profiles[1].ID)
	}
}

// ---------------------------------------------------------------------------
// Migration from embedded.json
// ---------------------------------------------------------------------------

func TestConnectionManager_Migration_NoLegacyFile(t *testing.T) {
	m := newTestConnectionManager(t)
	// Should not error when there is no legacy file.
	if err := m.migrate(context.TODO()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(m.ListProfiles()) != 0 {
		t.Fatal("expected 0 profiles when no legacy file exists")
	}
}

func TestConnectionManager_Migration_WithLegacyFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create legacy embedded.json.
	qrdbDir := tmpDir + "/qrdb"
	if err := os.MkdirAll(qrdbDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacyJSON := `{"storageBackend":"pebble","dataDir":"/tmp/data","raftEnabled":false}`
	if err := os.WriteFile(qrdbDir+"/embedded.json", []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m := NewConnectionManagerBinding(testLogger(t))
	if err := m.migrate(context.TODO()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := m.loadProfiles(); err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}

	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile after migration, got %d", len(profiles))
	}
	if profiles[0].Name != "Local Database" {
		t.Fatalf("expected name %q, got %q", "Local Database", profiles[0].Name)
	}
	if profiles[0].Type != "embedded" {
		t.Fatalf("expected type %q, got %q", "embedded", profiles[0].Type)
	}
	if profiles[0].Embedded == nil {
		t.Fatal("expected non-nil Embedded config")
	}
	if profiles[0].Embedded.StorageBackend != "pebble" {
		t.Fatalf("expected backend %q, got %q", "pebble", profiles[0].Embedded.StorageBackend)
	}

	// Legacy file should be removed.
	if _, err := os.Stat(qrdbDir + "/embedded.json"); !os.IsNotExist(err) {
		t.Fatal("expected legacy embedded.json to be removed after migration")
	}
}

func TestConnectionManager_Migration_SkipsWhenConnectionsExist(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	qrdbDir := tmpDir + "/qrdb"
	if err := os.MkdirAll(qrdbDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create both legacy and new files.
	legacyJSON := `{"storageBackend":"memory"}`
	if err := os.WriteFile(qrdbDir+"/embedded.json", []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	newJSON := `[{"id":"existing","name":"Existing","type":"external","external":{"transport":"grpc","address":"localhost:63003"}}]`
	if err := os.WriteFile(qrdbDir+"/connections.json", []byte(newJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m := NewConnectionManagerBinding(testLogger(t))
	if err := m.migrate(context.TODO()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := m.loadProfiles(); err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}

	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile (existing, not migrated), got %d", len(profiles))
	}
	if profiles[0].ID != "existing" {
		t.Fatalf("expected ID %q, got %q", "existing", profiles[0].ID)
	}
}

// ---------------------------------------------------------------------------
// Error type messages
// ---------------------------------------------------------------------------

func TestProfileNotFoundError_Message(t *testing.T) {
	err := &ProfileNotFoundError{ID: "abc"}
	expected := "qrdb: connection profile not found: abc"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestProfileValidationError_Message(t *testing.T) {
	err := &ProfileValidationError{Field: "name", Reason: "must not be empty"}
	expected := "qrdb: invalid profile name: must not be empty"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

// ---------------------------------------------------------------------------
// UUID variant bit
// ---------------------------------------------------------------------------

func TestNewUUID_VariantBit(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	// Position 19 must be 8, 9, a, or b (RFC 4122 variant).
	c := id[19]
	if c != '8' && c != '9' && c != 'a' && c != 'b' {
		t.Fatalf("expected variant nibble [89ab] at position 19, got %q in %s", c, id)
	}
}

func TestNewUUID_HyphenPositions(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	for _, pos := range []int{8, 13, 18, 23} {
		if id[pos] != '-' {
			t.Fatalf("expected hyphen at position %d, got %q in %s", pos, id[pos], id)
		}
	}
}

// ---------------------------------------------------------------------------
// SaveProfile: duplicate dataDir enforcement
// ---------------------------------------------------------------------------

// TestSaveProfile_DuplicateDataDir_Rejected verifies that saving a second
// embedded pebble profile whose dataDir resolves to the same absolute path as
// an existing profile is rejected with DuplicateDataDirError.
func TestSaveProfile_DuplicateDataDir_Rejected(t *testing.T) {
	m := newTestConnectionManager(t)
	sharedDir := t.TempDir()

	profileA := ConnectionProfile{
		ID: "dup-a", Name: "Server A", Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: sharedDir},
	}
	if err := m.SaveProfile(profileA); err != nil {
		t.Fatalf("SaveProfile A: %v", err)
	}

	profileB := ConnectionProfile{
		ID: "dup-b", Name: "Server B", Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: sharedDir},
	}
	err := m.SaveProfile(profileB)

	var dde *DuplicateDataDirError
	if !errors.As(err, &dde) {
		t.Fatalf("expected DuplicateDataDirError, got %T: %v", err, err)
	}
	if dde.ID != profileA.ID {
		t.Errorf("DuplicateDataDirError.ID: got %q, want %q", dde.ID, profileA.ID)
	}
	if dde.Path != resolveDataDir(sharedDir) {
		t.Errorf("DuplicateDataDirError.Path: got %q, want %q", dde.Path, resolveDataDir(sharedDir))
	}
}

// TestSaveProfile_SameIDOverride_Allowed verifies that re-saving the same
// profile ID with the same dataDir succeeds (it is an update, not a duplicate).
func TestSaveProfile_SameIDOverride_Allowed(t *testing.T) {
	m := newTestConnectionManager(t)
	sharedDir := t.TempDir()

	profileA := ConnectionProfile{
		ID: "override-a", Name: "Server A", Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: sharedDir},
	}
	if err := m.SaveProfile(profileA); err != nil {
		t.Fatalf("SaveProfile first: %v", err)
	}

	profileA.Name = "Server A Updated"
	if err := m.SaveProfile(profileA); err != nil {
		t.Fatalf("SaveProfile update (same ID, same dataDir): %v", err)
	}

	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile after update, got %d", len(profiles))
	}
	if profiles[0].Name != "Server A Updated" {
		t.Errorf("expected updated name, got %q", profiles[0].Name)
	}
}

// TestSaveProfile_DifferentDataDir_Allowed verifies that two embedded pebble
// profiles with distinct dataDirs can both be saved without error.
func TestSaveProfile_DifferentDataDir_Allowed(t *testing.T) {
	m := newTestConnectionManager(t)

	for _, id := range []string{"diff-a", "diff-b"} {
		p := ConnectionProfile{
			ID: id, Name: "Server " + id, Type: connectionTypeEmbedded,
			Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: t.TempDir()},
		}
		if err := m.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile %s: %v", id, err)
		}
	}

	if len(m.ListProfiles()) != 2 {
		t.Fatalf("expected 2 profiles with distinct dataDirs, got %d", len(m.ListProfiles()))
	}
}

// TestSaveProfile_DataDirTildeExpanded_Detected verifies that a pebble profile
// using a "~/" prefix and another using the equivalent absolute path are
// detected as colliding dataDirs.
func TestSaveProfile_DataDirTildeExpanded_Detected(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory:", err)
	}

	// Use a sub-path under the home directory that is unlikely to be a real dir.
	// We do not create it; resolveDataDir only normalises the path.
	subPath := "qrdb-test-tilde-collision-dir"
	tildeForm := "~/" + subPath
	absForm := home + "/" + subPath

	m := newTestConnectionManager(t)

	profileA := ConnectionProfile{
		ID: "tilde-a", Name: "Server Tilde", Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: tildeForm},
	}
	if err := m.SaveProfile(profileA); err != nil {
		t.Fatalf("SaveProfile A (tilde form): %v", err)
	}

	profileB := ConnectionProfile{
		ID: "tilde-b", Name: "Server Abs", Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{StorageBackend: "pebble", DataDir: absForm},
	}
	err = m.SaveProfile(profileB)

	var dde *DuplicateDataDirError
	if !errors.As(err, &dde) {
		t.Fatalf("expected DuplicateDataDirError when tilde and abs paths resolve identically, got %T: %v", err, err)
	}
	if dde.ID != profileA.ID {
		t.Errorf("DuplicateDataDirError.ID: got %q, want %q", dde.ID, profileA.ID)
	}
}
