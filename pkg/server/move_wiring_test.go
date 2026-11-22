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
	"reflect"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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
	for _, db := range []string{"a", "b"} {
		if err := store.Put(ctx, &placement.Placement{
			TenantID: "acme", Database: db, ShardID: 100, RF: 2, LeaderReplicaID: 1,
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
		TenantID: "acme", Database: "a", ShardID: 42, RF: 1, LeaderReplicaID: 1,
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

func TestWireTenantMoves_RefusesAnIncompleteConfig(t *testing.T) {
	if _, err := WireTenantMoves(TenantMoveConfig{NodeID: "1"}); err == nil {
		t.Fatal("a config with no catalog, registry or tenant service was wired")
	}
}
