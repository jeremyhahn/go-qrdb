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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// TestApplyConfigToFlags_ModeFromConfig verifies the YAML mode key reaches
// the start flags when the CLI flag was not explicitly set.
func TestApplyConfigToFlags_ModeFromConfig(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{mode: "raft"}
	cfg := &qrdbconfig.Config{Mode: capabilities.ModeStandalone}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, capabilities.ModeStandalone, f.mode)
}

// TestApplyConfigToFlags_ModeCLIPrecedence verifies an explicit --mode
// flag wins over the YAML value.
func TestApplyConfigToFlags_ModeCLIPrecedence(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	require.NoError(t, cmd.Flags().Set("mode", "raft"))

	f := &startFlags{mode: "raft"}
	cfg := &qrdbconfig.Config{Mode: capabilities.ModeStandalone}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "raft", f.mode)
}

// TestBuildServerConfig_CarriesMode pins that the flag-derived server
// config carries the run mode, so EffectiveCapabilities computes the
// right profile for flags-only boots.
func TestBuildServerConfig_CarriesMode(t *testing.T) {
	t.Parallel()

	f := &startFlags{mode: capabilities.ModeStandalone, adminPort: 63002}
	cfg := buildServerConfig(f)
	assert.Equal(t, capabilities.ModeStandalone, cfg.Mode)

	caps := cfg.EffectiveCapabilities()
	assert.Equal(t, capabilities.ModeStandalone, caps.Mode)
	assert.False(t, caps.MVCC)
	assert.False(t, caps.Watch)
	assert.True(t, caps.KV)
}

// TestStandaloneErrors_MessagesNameTheRemedy pins the typed refusal texts.
func TestStandaloneErrors_MessagesNameTheRemedy(t *testing.T) {
	t.Parallel()

	if msg := (&StandaloneRaftBackendError{}).Error(); !strings.Contains(msg, "requires mode: raft") {
		t.Errorf("StandaloneRaftBackendError = %q, must name the remedy", msg)
	}
	if msg := (&StandaloneMigrationError{}).Error(); !strings.Contains(msg, "requires mode: raft") {
		t.Errorf("StandaloneMigrationError = %q, must name the remedy", msg)
	}
}

// TestLocalDataPlaneCloser_ClosesBackendThenStorage verifies both halves
// are closed and the backend goes first.
func TestLocalDataPlaneCloser_ClosesBackendThenStorage(t *testing.T) {
	t.Parallel()

	stg := memory.NewStorage()
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, nil, nil)
	require.NoError(t, err)
	c := localDataPlaneCloser{local: backend.NewLocalBackend(eng), storage: stg}
	require.NoError(t, c.Close())

	// Storage is closed: subsequent operations must fail.
	_, err = stg.Get([]byte("k"))
	assert.Error(t, err, "storage must be closed by the closer")
}

// TestValidateSMStorageFlags mirrors the config-level sm_storage rules on
// the flags-only boot path.
func TestValidateSMStorageFlags(t *testing.T) {
	t.Parallel()

	if err := validateSMStorageFlags("pebble", "", ""); err != nil {
		t.Fatalf("empty sm-storage must validate: %v", err)
	}
	if err := validateSMStorageFlags("disk", "", "file"); err != nil {
		t.Fatalf("disk+file must validate: %v", err)
	}
	if err := validateSMStorageFlags("standard", "fast", "memory"); err != nil {
		t.Fatalf("composite fast backend must validate: %v", err)
	}

	err := validateSMStorageFlags("disk", "", "objstore")
	var unknown *UnknownSMStorageError
	if !errors.As(err, &unknown) {
		t.Fatalf("objstore: got %v, want *UnknownSMStorageError", err)
	}
	if !strings.Contains(err.Error(), "snapshot/backup tier") {
		t.Errorf("objstore refusal %q must explain the tier positioning", err.Error())
	}

	err = validateSMStorageFlags("mvcc", "", "file")
	var mismatch *SMStorageTypeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("pebble-native mismatch: got %v, want *SMStorageTypeMismatchError", err)
	}
}
