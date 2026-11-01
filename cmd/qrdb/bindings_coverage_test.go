//go:build !integration

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
)

// ---------------------------------------------------------------------------
// SMOpenError — Error() message (Unwrap is in bindings_server_test.go)
// ---------------------------------------------------------------------------

func TestSMOpenError_Error(t *testing.T) {
	inner := errors.New("pebble open failed")
	e := &SMOpenError{Backend: "pebble", Err: inner}
	want := "qrdb: open state machine (pebble): pebble open failed"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding constructor (NotConnected tests are in bindings_collection_test.go)
// ---------------------------------------------------------------------------

func TestCollectionBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil CollectionBinding")
	}
}

// ---------------------------------------------------------------------------
// EmbeddedServerBinding stub — startup/shutdown (no-ops in stub build)
// ---------------------------------------------------------------------------

func TestEmbeddedServerBinding_Startup_Noop(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewEmbeddedServerBinding(log, conn, nil)
	// startup and shutdown are no-ops on the stub; must not panic.
	b.startup(context.Background())
	if b.IsEmbedded() {
		t.Fatal("expected IsEmbedded=false after startup without StartEmbedded")
	}
	b.shutdown(context.Background())
	if b.IsEmbedded() {
		t.Fatal("expected IsEmbedded=false after an idle shutdown")
	}
}

// ---------------------------------------------------------------------------
// SealBinding stub — SetConfigBinding is a no-op
// ---------------------------------------------------------------------------

func TestSealBinding_SetConfigBinding_Noop(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	// SetConfigBinding is a no-op; must not panic.
	b.SetConfigBinding(nil)
}

// ---------------------------------------------------------------------------
// managedConnection.loadState — both branches (nil and stored)
// ---------------------------------------------------------------------------

func TestManagedConnection_LoadState_Nil(t *testing.T) {
	mc := &managedConnection{
		profile: ConnectionProfile{ID: "p1"},
	}
	// state is not set → returns default disconnected state.
	s := mc.loadState()
	if s.ProfileID != "p1" {
		t.Errorf("ProfileID = %q, want p1", s.ProfileID)
	}
	if s.Status != connectionStatusDisconnected {
		t.Errorf("Status = %q, want %q", s.Status, connectionStatusDisconnected)
	}
}

func TestManagedConnection_LoadState_Stored(t *testing.T) {
	mc := &managedConnection{
		profile: ConnectionProfile{ID: "p2"},
	}
	stored := ConnectionState{ProfileID: "p2", Status: connectionStatusConnected}
	mc.storeState(stored)

	s := mc.loadState()
	if s.Status != connectionStatusConnected {
		t.Errorf("Status = %q, want %q", s.Status, connectionStatusConnected)
	}
}

// ---------------------------------------------------------------------------
// Additional error type coverage (distinct from _Message tests in other files)
// ---------------------------------------------------------------------------

func TestProfileNotFoundError_Error(t *testing.T) {
	e := &ProfileNotFoundError{ID: "profile-1"}
	want := "qrdb: connection profile not found: profile-1"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestProfileValidationError_Error(t *testing.T) {
	e := &ProfileValidationError{Field: "address", Reason: "must not be empty"}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}
