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
	"os"
	"path/filepath"
	"testing"
)

// newRestoreFixture wires an EmbeddedServerBinding onto a ConnectionBinding
// backed by a connection manager, as createGUIBindings does, and saves cfg
// as the embedded server's saved configuration.
func newRestoreFixture(t *testing.T, cfg *EmbeddedConfig) (*ConnectionManagerBinding, *EmbeddedServerBinding) {
	t.Helper()
	m := newTestConnectionManager(t)
	m.ctx = context.Background()
	conn := NewConnectionBinding(testLogger(t))
	conn.SetManager(m)
	srv := NewEmbeddedServerBinding(testLogger(t), conn, nil)
	if err := srv.saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	return m, srv
}

func restoreTestConfig(t *testing.T) *EmbeddedConfig {
	t.Helper()
	return &EmbeddedConfig{
		DataDir:        t.TempDir(),
		StorageBackend: "memory",
		SealConfig:     &SealConfig{Mode: "none"},
	}
}

// assertRestoredActive checks that the active profile is wantID (or any
// embedded profile when wantID is empty) and that its client serves calls.
func assertRestoredActive(t *testing.T, m *ConnectionManagerBinding, wantID string) string {
	t.Helper()
	active := m.GetActiveProfileID()
	if active == "" {
		t.Fatal("RestoreEmbedded left no active profile")
	}
	if wantID != "" && active != wantID {
		t.Fatalf("active profile = %q, want %q", active, wantID)
	}
	t.Cleanup(func() { _ = m.Disconnect(active) })
	client, err := m.ActiveClient()
	if err != nil {
		t.Fatalf("ActiveClient after RestoreEmbedded: %v", err)
	}
	if client == nil {
		t.Fatal("ActiveClient returned a nil client")
	}
	if _, err := client.Database().ListDatabases(context.Background()); err != nil {
		t.Fatalf("SDK call through the restored client: %v", err)
	}
	return active
}

func TestRestoreEmbedded_WithManager_ConnectsMatchingProfile(t *testing.T) {
	cfg := restoreTestConfig(t)
	m, srv := newRestoreFixture(t, cfg)

	profile := ConnectionProfile{
		ID:       "local-restore",
		Name:     "Restore Test",
		Type:     connectionTypeEmbedded,
		Embedded: cfg,
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := m.Connect(profile.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := m.Disconnect(profile.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if err := srv.RestoreEmbedded(); err != nil {
		t.Fatalf("RestoreEmbedded: %v", err)
	}
	assertRestoredActive(t, m, profile.ID)
	if got := len(m.ListProfiles()); got != 1 {
		t.Fatalf("profiles after restore = %d, want 1 (the matching profile reused)", got)
	}
	if srv.IsEmbedded() {
		t.Fatal("RestoreEmbedded with a manager must not start the standalone server")
	}
}

func TestRestoreEmbedded_WithManager_SavesNewProfile(t *testing.T) {
	cfg := restoreTestConfig(t)
	m, srv := newRestoreFixture(t, cfg)

	if err := srv.RestoreEmbedded(); err != nil {
		t.Fatalf("RestoreEmbedded: %v", err)
	}
	active := assertRestoredActive(t, m, "")

	profiles := m.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("profiles after restore = %d, want 1", len(profiles))
	}
	p := profiles[0]
	if p.ID != active {
		t.Fatalf("saved profile %q is not the active one %q", p.ID, active)
	}
	if p.Type != connectionTypeEmbedded || p.Embedded == nil || p.Embedded.DataDir != cfg.DataDir {
		t.Fatalf("saved profile = %+v, want an embedded profile on %q", p, cfg.DataDir)
	}
	if p.Name != "Local Server" {
		t.Fatalf("saved profile name = %q, want %q", p.Name, "Local Server")
	}
}

// TestRestoreEmbedded_WithManager_FailedStartReturnsError proves a restore
// whose embedded server cannot start (its data directory is a regular file)
// returns an error and never makes the failed profile the active one.
func TestRestoreEmbedded_WithManager_FailedStartReturnsError(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := &EmbeddedConfig{
		DataDir:        notADir,
		StorageBackend: "pebble",
		SealConfig:     &SealConfig{Mode: "none"},
	}
	m, srv := newRestoreFixture(t, cfg)

	err := srv.RestoreEmbedded()
	if err == nil {
		t.Fatal("RestoreEmbedded on an unusable data directory returned nil; want the start failure")
	}
	var ese *EmbeddedServerError
	if !errors.As(err, &ese) {
		t.Fatalf("RestoreEmbedded error = %T: %v; want *EmbeddedServerError", err, err)
	}
	if active := m.GetActiveProfileID(); active != "" {
		t.Fatalf("active profile after a failed restore = %q; want none", active)
	}
	if _, err := m.ActiveClient(); err == nil {
		t.Fatal("ActiveClient after a failed restore returned a client")
	}
}
