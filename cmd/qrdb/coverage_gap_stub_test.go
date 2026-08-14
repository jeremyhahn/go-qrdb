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

//go:build !qrdb_embedded && !integration

// coverage_gap_stub_test.go tests the stub implementations of
// DatabaseInstanceRegistry and embedded connection when qrdb_embedded is NOT
// compiled in. Under qrdb_embedded, the real registry is used and the stub
// behaviour does not apply.
package main

import (
	"errors"
	"testing"
)

func TestDatabaseInstanceRegistry_Stub_Register(t *testing.T) {
	r := NewDatabaseInstanceRegistry(testLogger(t))
	err := r.Register("mydb", nil)
	if err == nil {
		t.Fatal("expected FeatureDisabledError from Register stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestDatabaseInstanceRegistry_Stub_Stop(t *testing.T) {
	r := NewDatabaseInstanceRegistry(testLogger(t))
	err := r.Stop("mydb")
	if err == nil {
		t.Fatal("expected FeatureDisabledError from Stop stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestDatabaseInstanceRegistry_Stub_StopAll(t *testing.T) {
	r := NewDatabaseInstanceRegistry(testLogger(t))
	err := r.StopAll()
	if err == nil {
		t.Fatal("expected FeatureDisabledError from StopAll stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestConfigBinding_Initialized_WhenNoConfig(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	cb := NewConfigBinding(log, esb, conn)
	// Stub EmbeddedServerBinding.HasSavedConfig returns false; Initialized
	// should report not initialized.
	resp := cb.Initialized()
	if resp == nil {
		t.Fatal("expected non-nil ConfigInitializedResponse")
	}
	if resp.Initialized {
		t.Fatal("expected Initialized=false when no saved config and not connected")
	}
}

func TestConnectionManager_ConnectEmbedded_FeatureDisabled(t *testing.T) {
	// connectEmbedded returns FeatureDisabledError when !qrdb_embedded.
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID:       "emb-1",
		Name:     "Embedded",
		Type:     "embedded",
		Embedded: &EmbeddedConfig{StorageBackend: "memory"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	state, err := m.Connect("emb-1")
	// Connect stores the error state instead of propagating it directly.
	if err != nil {
		t.Fatalf("Connect should not return error for embedded failure: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state even for failed embedded connect")
	}
	if state.Status != connectionStatusError {
		t.Fatalf("expected status=%q, got %q", connectionStatusError, state.Status)
	}
}
