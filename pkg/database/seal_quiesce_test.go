// Copyright (c) 2026 Jeremy Hahn
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

package database

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// gateProbeBackend is a standalone backend that counts the mutations reaching
// it and, when block is set, holds a Put inside the backend until released.
type gateProbeBackend struct {
	*StandaloneBackend
	mutations atomic.Int64
	block     bool
	entered   chan struct{}
	release   chan struct{}
}

func (p *gateProbeBackend) Put(ctx context.Context, key, value []byte) error {
	p.mutations.Add(1)
	if p.block {
		close(p.entered)
		<-p.release
	}
	return p.StandaloneBackend.Put(ctx, key, value)
}

func (p *gateProbeBackend) Delete(ctx context.Context, key []byte) error {
	p.mutations.Add(1)
	return p.StandaloneBackend.Delete(ctx, key)
}

func (p *gateProbeBackend) DeleteRange(ctx context.Context, start, end []byte) error {
	p.mutations.Add(1)
	return p.StandaloneBackend.DeleteRange(ctx, start, end)
}

func (p *gateProbeBackend) Batch(ctx context.Context, ops []BatchOp) error {
	p.mutations.Add(1)
	return p.StandaloneBackend.Batch(ctx, ops)
}

// newGatedDatabase builds a passphrase database barrier and a BarrierBackend
// over probe through the manager's own GetBarrier, returning the database
// barrier's quiesce gate.
func newGatedDatabase(t *testing.T, probe *gateProbeBackend) (*DatabaseBarrierManager, *BarrierBackend, *quiesce.Gate) {
	t.Helper()
	mgr := newTestBarrierManager(t)
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.EnsureBarrier(context.Background(), "testdb", "", cfg, seal.NewMemoryBackend(), testCreds()); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}
	encryptor := mgr.GetBarrier("testdb", "")
	if encryptor == nil {
		t.Fatal("GetBarrier returned nil")
	}
	tb, err := mgr.registry.GetTenant(barrierKey("testdb", ""))
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	backend := NewBarrierBackend(probe, encryptor)
	t.Cleanup(func() { _ = backend.Close() })
	return mgr, backend, tb.Barrier().Quiesce()
}

// TestBarrierBackend_WriteInFlightHoldsTheDatabaseDrain: a database write
// blocked inside its backend holds the database barrier's quiesce lease, so
// the seal's drain cannot finish and reports the store; once the write
// returns the drain finishes and the database seals.
func TestBarrierBackend_WriteInFlightHoldsTheDatabaseDrain(t *testing.T) {
	probe := &gateProbeBackend{
		StandaloneBackend: newTestBackend(t),
		block:             true,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	mgr, backend, gate := newGatedDatabase(t, probe)

	done := make(chan error, 1)
	go func() { done <- backend.Put(context.Background(), []byte("k"), []byte("v")) }()
	<-probe.entered

	gate.Close()
	var timeout *quiesce.DrainTimeoutError
	if err := gate.Drain(-1); !errors.As(err, &timeout) || len(timeout.Stores) != 1 {
		t.Fatalf("Drain with a write in flight = %v; want a DrainTimeoutError naming one store", err)
	}

	close(probe.release)
	if err := <-done; err != nil {
		t.Fatalf("Put admitted before the seal = %v; want nil", err)
	}
	if err := gate.Drain(-1); err != nil {
		t.Fatalf("Drain after the write returned = %v; want nil", err)
	}
	gate.Reopen()
	if err := mgr.SealDatabase("testdb", ""); err != nil {
		t.Fatalf("SealDatabase after the write returned = %v; want nil", err)
	}
}

// TestBarrierBackend_ClosedGateRefusesEveryMutation: once the database
// barrier's gate is closed for a seal, every mutation is refused with the
// sealed refusal before it reaches the backend, holds nothing, and leaves no
// state behind.
func TestBarrierBackend_ClosedGateRefusesEveryMutation(t *testing.T) {
	probe := &gateProbeBackend{StandaloneBackend: newTestBackend(t)}
	_, backend, gate := newGatedDatabase(t, probe)
	ctx := context.Background()

	if err := backend.Put(ctx, []byte("present"), []byte("v")); err != nil {
		t.Fatalf("Put before the seal: %v", err)
	}
	before := probe.mutations.Load()
	gate.Close()

	var sealed *SealedError
	mutations := map[string]func() error{
		"Put": func() error { return backend.Put(ctx, []byte("k"), []byte("v")) },
		"PutIfAbsent": func() error {
			_, err := backend.PutIfAbsent(ctx, []byte("k"), []byte("v"))
			return err
		},
		"CompareAndDelete": func() error {
			_, err := backend.CompareAndDelete(ctx, []byte("present"), []byte("v"))
			return err
		},
		"CompareAndSwap": func() error {
			_, err := backend.CompareAndSwap(ctx, []byte("present"), []byte("v"), []byte("w"))
			return err
		},
		"Delete":      func() error { return backend.Delete(ctx, []byte("present")) },
		"DeleteRange": func() error { return backend.DeleteRange(ctx, []byte("a"), nil) },
		"Batch": func() error {
			return backend.Batch(ctx, []BatchOp{{Op: BatchOpPut, Key: []byte("k"), Value: []byte("v")}})
		},
	}
	for name, mutate := range mutations {
		if err := mutate(); !errors.As(err, &sealed) {
			t.Errorf("%s on a closed gate = %v; want *SealedError", name, err)
		}
	}
	if n := probe.mutations.Load(); n != before {
		t.Errorf("%d mutations reached the backend on a closed gate; want 0", n-before)
	}
	if err := gate.Drain(-1); err != nil {
		t.Errorf("Drain after refused mutations = %v; want nil (no lease held)", err)
	}

	gate.Reopen()
	if v, err := backend.Get(ctx, []byte("k")); err != nil || v != nil {
		t.Errorf("Get(k) = %q, %v; want nothing written", v, err)
	}
	if v, err := backend.Get(ctx, []byte("present")); err != nil || string(v) != "v" {
		t.Errorf("Get(present) = %q, %v; want the value written before the seal", v, err)
	}
}

// TestBarrierBackend_CloseKeepsAWriteInFlightInTheDrain: closing a database
// backend while a mutation it admitted is still inside the store must not
// take that mutation out of the seal's drain, and the closed backend admits
// no new mutation.
func TestBarrierBackend_CloseKeepsAWriteInFlightInTheDrain(t *testing.T) {
	probe := &gateProbeBackend{
		StandaloneBackend: newTestBackend(t),
		block:             true,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	_, backend, gate := newGatedDatabase(t, probe)

	done := make(chan error, 1)
	go func() { done <- backend.Put(context.Background(), []byte("k"), []byte("v")) }()
	<-probe.entered

	_ = backend.Close()
	var sealed *SealedError
	if err := backend.Delete(context.Background(), []byte("k")); !errors.As(err, &sealed) {
		t.Errorf("Delete on a closed backend = %v; want *SealedError", err)
	}

	gate.Close()
	var timeout *quiesce.DrainTimeoutError
	if err := gate.Drain(-1); !errors.As(err, &timeout) || len(timeout.Stores) != 1 {
		t.Fatalf("Drain with a write in flight on a closed backend = %v; want a DrainTimeoutError naming one store", err)
	}

	close(probe.release)
	<-done // the inner store is closed; only the drain matters here
	if err := gate.Drain(-1); err != nil {
		t.Fatalf("Drain after the write returned = %v; want nil", err)
	}
}
