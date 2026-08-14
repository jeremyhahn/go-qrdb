//go:build !qrdb_embedded

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
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// IsEmbeddedAvailable
// ---------------------------------------------------------------------------

func TestIsEmbeddedAvailable_ReturnsFalse(t *testing.T) {
	if IsEmbeddedAvailable() {
		t.Error("expected IsEmbeddedAvailable()=false without qrdb_embedded tag")
	}
}

func TestIsEmbeddedAvailable_ReturnsFalse_Consistent(t *testing.T) {
	first := IsEmbeddedAvailable()
	second := IsEmbeddedAvailable()
	if first != second {
		t.Errorf("expected IsEmbeddedAvailable() to return consistent results: first=%v second=%v", first, second)
	}
}

// ---------------------------------------------------------------------------
// EmbeddedServerBinding stubs
// ---------------------------------------------------------------------------

func TestEmbeddedServerBinding_Stub_IsEmbedded(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	if esb.IsEmbedded() {
		t.Error("expected IsEmbedded()=false for stub")
	}
}

func TestEmbeddedServerBinding_Stub_HasSavedConfig(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	if esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=false for stub")
	}
}

func TestEmbeddedServerBinding_Stub_StartEmbedded(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	err := esb.StartEmbedded(&EmbeddedConfig{})
	if err == nil {
		t.Fatal("expected error from StartEmbedded stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "embedded" {
		t.Fatalf("expected Feature %q, got %q", "embedded", fde.Feature)
	}
}

func TestEmbeddedServerBinding_Stub_StopEmbedded(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	err := esb.StopEmbedded()
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestEmbeddedServerBinding_Stub_RestoreEmbedded(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	err := esb.RestoreEmbedded()
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestEmbeddedServerBinding_Stub_ClearSavedConfig(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	err := esb.ClearSavedConfig()
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestEmbeddedServerBinding_Stub_Registry(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, nil)
	if esb.Registry() != nil {
		t.Error("expected nil Registry from stub")
	}
}

// ---------------------------------------------------------------------------
// SealBinding stubs
// ---------------------------------------------------------------------------

func TestSealBinding_Stub_Initialize(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	err := b.Initialize("secret")
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestSealBinding_Stub_InitializeShamir(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	_, err := b.InitializeShamir("secret", 3, 5)
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestSealBinding_Stub_Unseal(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	_, err := b.Unseal("share-abc")
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestSealBinding_Stub_Seal(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	err := b.Seal()
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestSealBinding_Stub_Status(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, nil)
	_, err := b.Status()
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseInstanceRegistry stubs
// ---------------------------------------------------------------------------

func TestDatabaseInstanceRegistry_Stub_Count(t *testing.T) {
	r := NewDatabaseInstanceRegistry(testLogger(t))
	if r.Count() != 0 {
		t.Error("expected Count()=0 from stub")
	}
}

func TestDatabaseInstanceRegistry_Stub_Names(t *testing.T) {
	r := NewDatabaseInstanceRegistry(testLogger(t))
	if r.Names() != nil {
		t.Error("expected nil Names from stub")
	}
}
