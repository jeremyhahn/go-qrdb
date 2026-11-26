// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeTenantMoveService records the replica-node source WireTenantMoves
// hands the tenant service.
type fakeTenantMoveService struct {
	source tenant.MoveReplicaNodeSource
}

func (f *fakeTenantMoveService) AdmitTenantMove(context.Context, volume.MoveTenantRequest) ([]string, error) {
	return nil, nil
}

func (f *fakeTenantMoveService) RecordTenantPoolMoved(context.Context, string, string, string) error {
	return nil
}

func (f *fakeTenantMoveService) SetMoveReplicaNodeSource(s tenant.MoveReplicaNodeSource) {
	f.source = s
}

// moveWiringCatalog returns a volume-pool catalog with a "default" pool.
func moveWiringCatalog(t *testing.T) *volume.Catalog {
	t.Helper()
	allocator, _, _ := sealtest.ShareableVolumeComponents(t)
	cat, ok := allocator.(*volume.Catalog)
	if !ok {
		t.Fatalf("allocator is %T, want *volume.Catalog", allocator)
	}
	return cat
}

func TestWireTenantMoves_RefusesEachMissingDependency(t *testing.T) {
	cat := moveWiringCatalog(t)
	reg := &seal.BarrierRegistry{}
	svc := &fakeTenantMoveService{}
	full := TenantMoveConfig{Catalog: cat, BarrierReg: reg, Tenants: svc, NodeID: "n1"}
	cases := map[string]func(c *TenantMoveConfig){
		"no catalog":        func(c *TenantMoveConfig) { c.Catalog = nil },
		"no barrier":        func(c *TenantMoveConfig) { c.BarrierReg = nil },
		"no tenant service": func(c *TenantMoveConfig) { c.Tenants = nil },
		"no node id":        func(c *TenantMoveConfig) { c.NodeID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full
			mutate(&cfg)
			if w, err := WireTenantMoves(cfg); err == nil || w != nil {
				t.Fatalf("WireTenantMoves = %v, %v; want a refusal", w, err)
			}
		})
	}
}

func TestWireTenantMoves_Standalone(t *testing.T) {
	svc := &fakeTenantMoveService{}
	w, err := WireTenantMoves(TenantMoveConfig{
		Catalog:    moveWiringCatalog(t),
		BarrierReg: &seal.BarrierRegistry{},
		Tenants:    svc,
		NodeID:     "n1",
	})
	if err != nil {
		t.Fatalf("WireTenantMoves: %v", err)
	}
	if w.interval != DefaultTenantMoveInterval {
		t.Fatalf("interval = %v, want the default", w.interval)
	}
	if w.logger == nil || w.reconciler == nil || w.reopens == nil || w.nodeID != "n1" {
		t.Fatalf("worker not fully wired: %+v", w)
	}
	if !w.leads() {
		t.Fatal("a node with no shard group must lead its own moves")
	}
	if svc.source == nil {
		t.Fatal("the tenant service got no replica-node source")
	}
	stores, ok := w.reopens.(*MoveStoreCloser)
	if !ok {
		t.Fatalf("reopens is %T", w.reopens)
	}
	if stores.shards != nil || stores.caughtUp != nil {
		t.Fatal("a standalone node must have no shard lifecycle or catch-up probe")
	}
}

type noopTransitions struct{}

func (noopTransitions) RecordMoveTransition(context.Context, string, string, string, string) error {
	return nil
}

type noopStartAudit struct{}

func (noopStartAudit) BeginMoveStart(context.Context, volume.MoveTenantRequest, volume.MoveStartFacts) (func(*volume.Move, error), error) {
	return func(*volume.Move, error) {}, nil
}

// fakeDataDirs records each relocation and fails with err.
type fakeDataDirs struct {
	calls int
	err   error
}

func (f *fakeDataDirs) RelocateTenantDataDirs(context.Context, string, string, string) error {
	f.calls++
	return f.err
}

func TestWireTenantMoves_Raft(t *testing.T) {
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	infra := &TenantInfra{
		Hosts:      &TenantHostRegistry{},
		Raft:       &RaftBackendFactory{},
		Reconciler: &TenantShardReconciler{},
	}
	m := &fakeMoveMembership{leader: 2, known: true, nodes: map[uint64]string{2: "127.0.0.1:7002"}}
	w, err := WireTenantMoves(TenantMoveConfig{
		Catalog:        moveWiringCatalog(t),
		BarrierReg:     &seal.BarrierRegistry{},
		Tenants:        &fakeTenantMoveService{},
		Infra:          infra,
		NodeID:         "n2",
		Membership:     m,
		ReplicaID:      2,
		Placements:     store,
		Interval:       7 * time.Second,
		CatchUpTimeout: 2 * time.Second,
		DataDirs:       &fakeDataDirs{},
		Transitions:    noopTransitions{},
		StartAudit:     noopStartAudit{},
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatalf("WireTenantMoves: %v", err)
	}
	if w.interval != 7*time.Second {
		t.Fatalf("interval = %v, want 7s", w.interval)
	}
	if !w.leads() {
		t.Fatal("replica 2 is the system shard leader")
	}
	if infra.Reconciler.moveHold.Load() == nil || infra.Hosts.moveHold.Load() == nil {
		t.Fatal("the reconciler and the host registry must hold for moves")
	}
	stores := w.reopens.(*MoveStoreCloser)
	if stores.shards == nil || stores.localReplicas == nil || stores.dataDirs == nil ||
		stores.hosts == nil || stores.catchUpTimeout != 2*time.Second {
		t.Fatalf("raft move stores not fully wired: %+v", stores)
	}
}

func TestNewMoveStores_QuicraftHostProbesCatchUp(t *testing.T) {
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	reg := &seal.BarrierRegistry{}
	stores, err := newMoveStores(TenantMoveConfig{BarrierReg: reg, Membership: &quicraft.Host{}, Placements: store})
	if err != nil {
		t.Fatalf("newMoveStores: %v", err)
	}
	if stores.caughtUp == nil {
		t.Fatal("a quicraft host with placements must probe catch-up")
	}
	stores, err = newMoveStores(TenantMoveConfig{BarrierReg: reg, Membership: (*quicraft.Host)(nil), Placements: store})
	if err != nil {
		t.Fatalf("newMoveStores: %v", err)
	}
	if stores.caughtUp != nil {
		t.Fatal("a nil quicraft host must not probe catch-up")
	}
}

// failingMoveSteps fails both passes and counts them.
type failingMoveSteps struct{ local, leader int }

func (s *failingMoveSteps) RunLocalSteps(context.Context, string) error {
	s.local++
	return errors.New("local steps failed")
}

func (s *failingMoveSteps) ReconcileOnce(context.Context) error {
	s.leader++
	return errors.New("leader pass failed")
}

// TestTenantMoveWorker_TickLeaderPassesFailSoft: a leading node runs both
// passes; a failing pass is logged, not fatal, and a worker with no stores
// retries nothing.
func TestTenantMoveWorker_TickLeaderPassesFailSoft(t *testing.T) {
	steps := &failingMoveSteps{}
	w := &TenantMoveWorker{
		reconciler: steps,
		nodeID:     "n1",
		leads:      func() bool { return true },
		interval:   DefaultTenantMoveInterval,
		logger:     slog.Default(),
	}
	w.Tick(context.Background())
	w.Tick(context.Background())
	if steps.local != 2 || steps.leader != 2 {
		t.Fatalf("passes local=%d leader=%d, want 2 and 2", steps.local, steps.leader)
	}
}

// TestTenantMoveWorker_TickKeepsASealedReopenPending: a tenant whose barrier
// stays sealed stays pending across ticks until its administrator unseals it.
func TestTenantMoveWorker_TickKeepsASealedReopenPending(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{{TenantID: "acme", Database: "a"}: true}}
	barriers := &fakeMoveBarriers{}
	stores, err := NewMoveStoreCloser(shards, barriers, nil, 0)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := stores.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	barriers.sealed = map[string]bool{"acme": true}
	if err := stores.OpenTenantStores(ctx, "acme"); err == nil {
		t.Fatal("open of a sealed tenant: want error")
	}
	w := &TenantMoveWorker{
		reconciler: &countingMoveSteps{},
		reopens:    stores,
		nodeID:     "n1",
		leads:      func() bool { return false },
		interval:   DefaultTenantMoveInterval,
		logger:     slog.Default(),
	}
	w.Tick(ctx)
	if got := stores.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
	barriers.sealed = nil
	w.Tick(ctx)
	if got := stores.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after the unseal = %v", got)
	}
}
