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
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

type fakeMoveMembership struct {
	leader   uint64
	known    bool
	nodes    map[uint64]string
	leadErr  error
	shardErr error
}

func (f *fakeMoveMembership) LeaderID(uint64) (uint64, bool, error) {
	return f.leader, f.known, f.leadErr
}

func (f *fakeMoveMembership) SyncGetShardMembership(context.Context, uint64) (*quicraft.Membership, error) {
	if f.shardErr != nil {
		return nil, f.shardErr
	}
	return &quicraft.Membership{Nodes: f.nodes}, nil
}

func TestSystemLeaderCheck(t *testing.T) {
	if !systemLeaderCheck(nil, 7)() {
		t.Fatal("a node with no membership must lead its own moves")
	}
	if !systemLeaderCheck((*quicraft.Host)(nil), 7)() {
		t.Fatal("a nil *quicraft.Host must count as no membership")
	}
	m := &fakeMoveMembership{leader: 7, known: true}
	if !systemLeaderCheck(m, 7)() {
		t.Fatal("the system shard's leader does not lead")
	}
	if systemLeaderCheck(m, 8)() {
		t.Fatal("a follower leads")
	}
	m.known = false
	if systemLeaderCheck(m, 7)() {
		t.Fatal("no known leader, yet this node leads")
	}
	m.known, m.leadErr = true, errors.New("boom")
	if systemLeaderCheck(m, 7)() {
		t.Fatal("a leader read error leads")
	}
}

func TestMoveReplicaNodeSource(t *testing.T) {
	ctx := context.Background()
	local := &moveReplicaNodeSource{local: "1"}
	if got, err := local.MoveReplicaNodes(ctx, "acme"); err != nil || !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("no shard group = %v, %v; want this node", got, err)
	}

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m := &fakeMoveMembership{nodes: map[uint64]string{1: "127.0.0.1:7001", 2: "127.0.0.1:7002", 3: "127.0.0.1:7003"}}
	src := &moveReplicaNodeSource{membership: m, placements: store, local: "1"}
	if got, err := src.MoveReplicaNodes(ctx, "acme"); err != nil || !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("no placement = %v, %v; want this node", got, err)
	}
	typedNil := &moveReplicaNodeSource{membership: (*quicraft.Host)(nil), placements: store, local: "1"}
	if got, err := typedNil.MoveReplicaNodes(ctx, "acme"); err != nil || !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("nil *quicraft.Host = %v, %v; want this node", got, err)
	}
	for _, db := range []string{"a", "b"} {
		if err := store.Put(ctx, &placement.Placement{
			TenantID: "acme", DatabaseID: db, Database: db, ShardID: 100, RF: 2, LeaderReplicaID: 1,
			Replicas: []placement.Replica{
				{ReplicaID: 1, NodeID: "127.0.0.1:7003", RaftAddr: "127.0.0.1:7003"},
				{ReplicaID: 2, NodeID: "127.0.0.1:7002", RaftAddr: "127.0.0.1:7002"},
			},
		}); err != nil {
			t.Fatalf("Put %s: %v", db, err)
		}
	}
	got, err := src.MoveReplicaNodes(ctx, "acme")
	if err != nil || !reflect.DeepEqual(got, []string{"2", "3"}) {
		t.Fatalf("replica nodes = %v, %v; want the system replica ids [2 3]", got, err)
	}

	delete(m.nodes, 2)
	var conflict *volume.MoveConflictError
	if _, err := src.MoveReplicaNodes(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("a replica off the cluster = %v, want *volume.MoveConflictError", err)
	}
	m.shardErr = errors.New("no leader")
	if _, err := src.MoveReplicaNodes(ctx, "acme"); !errors.Is(err, m.shardErr) {
		t.Fatalf("membership error = %v", err)
	}
}

func TestLocalMoveReplicas(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m := &fakeMoveMembership{nodes: map[uint64]string{1: "127.0.0.1:7001", 2: "127.0.0.1:7002", 3: "127.0.0.1:7003"}}
	if localMoveReplicas(nil, store, 1) != nil || localMoveReplicas(m, nil, 1) != nil || localMoveReplicas(m, store, 0) != nil {
		t.Fatal("nil membership, nil placements or replica ID 0 must return nil")
	}
	if localMoveReplicas((*quicraft.Host)(nil), store, 1) != nil {
		t.Fatal("a nil *quicraft.Host must return nil, not a func that panics")
	}
	put := func(tenant, db string, addrs ...string) {
		t.Helper()
		p := &placement.Placement{TenantID: tenant, DatabaseID: db, Database: db, ShardID: 100, RF: len(addrs), LeaderReplicaID: 1}
		for i, a := range addrs {
			p.Replicas = append(p.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: a, RaftAddr: a})
		}
		if err := store.Put(ctx, p); err != nil {
			t.Fatalf("Put %s/%s: %v", tenant, db, err)
		}
	}
	put("acme", "c", "127.0.0.1:7003", "127.0.0.1:7002")
	put("acme", "a", "127.0.0.1:7002", "127.0.0.1:7003")
	put("acme", "b", "127.0.0.1:7001", "127.0.0.1:7002")
	put("other", "a", "127.0.0.1:7003")
	local := localMoveReplicas(m, store, 3)
	got, err := local(ctx, "acme")
	if err != nil || !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("local replicas = %v, %v; want [a c]", got, err)
	}
	if got, err := local(ctx, "nobody"); err != nil || len(got) != 0 {
		t.Fatalf("tenant without placement = %v, %v; want none", got, err)
	}
	delete(m.nodes, 3)
	if _, err := local(ctx, "acme"); err == nil || !strings.Contains(err.Error(), "replica 3") {
		t.Fatalf("replica absent from the membership = %v, want an error naming replica 3", err)
	}
	m.shardErr = errors.New("no leader")
	if _, err := local(ctx, "acme"); !errors.Is(err, m.shardErr) {
		t.Fatalf("membership error = %v", err)
	}
}

type fakeReadProbe struct {
	err   error
	shard uint64
}

func (f *fakeReadProbe) LeaderID(uint64) (uint64, bool, error) { return 1, true, nil }

func (f *fakeReadProbe) ReadReady(_ context.Context, shardID uint64) error {
	f.shard = shardID
	return f.err
}

func TestReadIndexCaughtUp(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	probe := &fakeReadProbe{err: quicraft.ErrReadIndexNotReady}
	caughtUp := readIndexCaughtUp(probe, store)
	if ok, err := caughtUp(ctx, "acme", "a"); err != nil || !ok {
		t.Fatalf("no placement = %v, %v; want caught up", ok, err)
	}
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "acme", DatabaseID: "a", Database: "a", ShardID: 42, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "127.0.0.1:7001", RaftAddr: "127.0.0.1:7001"}},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ok, err := caughtUp(ctx, "acme", "a"); err != nil || ok || probe.shard != 42 {
		t.Fatalf("not ready = %v, %v (shard %d); want not caught up on shard 42", ok, err, probe.shard)
	}
	probe.err = nil
	if ok, err := caughtUp(ctx, "acme", "a"); err != nil || !ok {
		t.Fatalf("ready = %v, %v; want caught up", ok, err)
	}
}

// A replica whose shard is gone or whose host closed never catches up, so
// the probe's error comes back rather than reading as "not yet"; a probe
// that ran out its own timeout while the caller still waits is "not yet".
func TestReadIndexCaughtUp_PermanentShardFailureIsAnError(t *testing.T) {
	ctx := context.Background()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "acme", DatabaseID: "a", Database: "a", ShardID: 42, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "127.0.0.1:7001", RaftAddr: "127.0.0.1:7001"}},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, perm := range []error{quicraft.ErrShardNotFound, quicraft.ErrClosed} {
		caughtUp := readIndexCaughtUp(&fakeReadProbe{err: perm}, store)
		if ok, err := caughtUp(ctx, "acme", "a"); ok || !errors.Is(err, perm) {
			t.Fatalf("probe %v = %v, %v; want the error", perm, ok, err)
		}
	}
	caughtUp := readIndexCaughtUp(&fakeReadProbe{err: context.DeadlineExceeded}, store)
	if ok, err := caughtUp(ctx, "acme", "a"); ok || err != nil {
		t.Fatalf("probe timeout = %v, %v; want not yet", ok, err)
	}
}

// TenantMoveConfig.CatchUpTimeout reaches the store closer; zero keeps the
// default.
func TestNewMoveStores_CatchUpTimeout(t *testing.T) {
	reg := &seal.BarrierRegistry{} // construction only; never opened
	stores, err := newMoveStores(TenantMoveConfig{BarrierReg: reg, CatchUpTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("newMoveStores: %v", err)
	}
	if stores.catchUpTimeout != 3*time.Second {
		t.Fatalf("catchUpTimeout = %v, want 3s", stores.catchUpTimeout)
	}
	stores, err = newMoveStores(TenantMoveConfig{BarrierReg: reg})
	if err != nil {
		t.Fatalf("newMoveStores: %v", err)
	}
	if stores.catchUpTimeout != DefaultMoveCatchUpTimeout {
		t.Fatalf("default catchUpTimeout = %v", stores.catchUpTimeout)
	}
}

func TestWireTenantMoves_RefusesAnIncompleteConfig(t *testing.T) {
	if _, err := WireTenantMoves(TenantMoveConfig{NodeID: "1"}); err == nil {
		t.Fatal("a config with no catalog, registry or tenant service was wired")
	}
}

// countingMoveSteps stands in for the move reconciler: it counts the passes.
type countingMoveSteps struct{ local, leader int }

func (s *countingMoveSteps) RunLocalSteps(context.Context, string) error {
	s.local++
	return nil
}

func (s *countingMoveSteps) ReconcileOnce(context.Context) error {
	s.leader++
	return nil
}

func TestTenantMoveWorker_TickRetriesAFailedReopen(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	stores, err := NewMoveStoreCloser(shards, barriers, nil, 0)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := stores.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	barriers.err = errors.New("reopen failed")
	if err := stores.OpenTenantStores(ctx, "acme"); err == nil {
		t.Fatal("open: want error")
	}
	steps := &countingMoveSteps{}
	w := &TenantMoveWorker{
		reconciler: steps,
		reopens:    stores,
		nodeID:     "n1",
		leads:      func() bool { return false },
		interval:   DefaultTenantMoveInterval,
		logger:     slog.Default(),
	}
	// The failure persists: each tick retries once and the tenant stays
	// pending and unheld.
	w.Tick(ctx)
	w.Tick(ctx)
	if got := stores.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
	if stores.HoldsTenant("acme") {
		t.Fatal("acme held after a failed retry")
	}
	if want := []string{"close acme", "reopen acme", "reopen acme", "reopen acme"}; !reflect.DeepEqual(barriers.calls, want) {
		t.Fatalf("barrier calls = %v, want %v", barriers.calls, want)
	}
	// The failure clears: the next tick serves the tenant again, without
	// a restart, on a node that does not lead.
	barriers.err = nil
	w.Tick(ctx)
	if got := stores.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after the recovering tick = %v", got)
	}
	if !shards.started[StartedShard{TenantID: "acme", Database: "a"}] {
		t.Fatal("acme/a not served after the recovering tick")
	}
	if steps.local != 3 || steps.leader != 0 {
		t.Fatalf("passes local=%d leader=%d, want 3 and 0", steps.local, steps.leader)
	}
	// Nothing pending: a tick retries nothing.
	w.Tick(ctx)
	if n := len(barriers.calls); n != 5 {
		t.Fatalf("barrier calls after an idle tick = %d, want 5", n)
	}
}
