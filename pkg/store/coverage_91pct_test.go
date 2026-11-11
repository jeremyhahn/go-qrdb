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

package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// factory.go: WithNoSync (0% -> covered)
// ---------------------------------------------------------------------------

// TestWithNoSync_ViaCreateFuncWith confirms that WithNoSync is applied when
// the state machine is constructed through NewMVCCCreateFuncWith.
func TestWithNoSync_ViaCreateFuncWith(t *testing.T) {
	cf := NewMVCCCreateFuncWith(nil, WithNoSync())
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
	s, ok := dsm.(*MVCCStateMachine)
	if !ok {
		t.Fatalf("expected *MVCCStateMachine, got %T", dsm)
	}
	if !s.noSync {
		t.Fatal("expected noSync to be true after WithNoSync via factory")
	}
}

// TestWithNoSync_UpdateWorks confirms that a state machine with noSync=true
// still accepts and applies commands correctly.
func TestWithNoSync_UpdateWorks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)
	s.noSync = true
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("ns-key"), []byte("ns-val"), 0))},
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with noSync: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from put with noSync")
	}
}

// ---------------------------------------------------------------------------
// factory.go: NewPebbleCreateFuncWith (0% -> covered)
// ---------------------------------------------------------------------------

// TestNewPebbleCreateFuncWith_WithConfigurators verifies that PebbleConfigurator
// functions are applied to the state machine after construction.
func TestNewPebbleCreateFuncWith_WithConfigurators(t *testing.T) {
	lr := lease.NewLessor()
	defer lr.Close()

	hub := watch.NewHub()
	defer hub.Close()

	cf := NewPebbleCreateFuncWith(
		nil,
		func(s *PebbleStateMachine) { s.SetLessor(lr) },
		func(s *PebbleStateMachine) { s.SetWatchHub(hub) },
		func(s *PebbleStateMachine) { s.SetTimeSeriesEnabled(true) },
	)

	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
	s, ok := dsm.(*PebbleStateMachine)
	if !ok {
		t.Fatalf("expected *PebbleStateMachine, got %T", dsm)
	}
	if s.Lessor() != lr {
		t.Fatal("expected lessor to be set via PebbleConfigurator")
	}
	if s.hub != hub {
		t.Fatal("expected hub to be set via PebbleConfigurator")
	}
	if !s.enableTimeSeries {
		t.Fatal("expected enableTimeSeries to be true via PebbleConfigurator")
	}
}

// TestNewPebbleCreateFuncWith_CanOpenAndOperate confirms a PebbleStateMachine
// constructed via the factory can be opened and operated.
func TestNewPebbleCreateFuncWith_CanOpenAndOperate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	cf := NewPebbleCreateFuncWith(nil)
	dsm := cf(1, 1)
	s := dsm.(*PebbleStateMachine)

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cmd, err := kv.EncodePut([]byte("pebble-factory-key"), []byte("pebble-factory-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	val, err := s.Lookup(context.Background(), []byte("pebble-factory-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("pebble-factory-val")) {
		t.Fatalf("expected pebble-factory-val, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: getKeyInBatch - defensive copy verification (90%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_GetKeyInBatch_DefensiveCopy confirms that
// getKeyInBatch returns a copy, not a slice pointing into pebble's internal
// buffer.
func TestPebbleStateMachine_GetKeyInBatch_DefensiveCopy(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("copy-key"), []byte("copy-val"), 0))},
	})

	s.mu.Lock()
	val, err := s.getKeyInBatch(readBatch(t, s), []byte("copy-key"))
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("getKeyInBatch: %v", err)
	}
	if !bytes.Equal(val, []byte("copy-val")) {
		t.Fatalf("expected copy-val, got %q", val)
	}

	// Mutate the returned slice; re-reading should still show original.
	val[0] = 'X'

	s.mu.Lock()
	val2, err := s.getKeyInBatch(readBatch(t, s), []byte("copy-key"))
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("getKeyInBatch 2: %v", err)
	}
	if !bytes.Equal(val2, []byte("copy-val")) {
		t.Fatalf("defensive copy violated: got %q", val2)
	}
}
