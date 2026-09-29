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

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// newSharedTransportTestHost builds a shared Host WITH a Raft transport — the
// clustered daemon's shape — so a dedicated shard started on it is subject to
// go-quicraft's member-identity rule (go-qrdb#556). No shard is started here.
func newSharedTransportTestHost(t *testing.T) (*quicraft.Host, uint64, string) {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.Issue(filepath.Join(pki, "node"), qrdbdharness.WithSubject("node-1", "raft"), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	port, err := qrdbdharness.ReserveUDPPort()
	if err != nil {
		t.Fatalf("ReserveUDPPort: %v", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	dir := t.TempDir()
	cfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(dir, "wal"),
		NodeHostDir:   filepath.Join(dir, "nodehost"),
		RaftAddress:   addr,
		ListenAddress: addr,
		DeploymentID:  1,
	}
	cfg.TransportConfig.MTLSConfig = &quicraftconfig.MTLSConfig{CACert: caPEM, Cert: leaf.CertPEM, Key: leaf.KeyPEM}
	host, err := quicraft.NewHost(cfg, quicraft.WithoutEncryption())
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if host.NodeIdentity() == "" {
		t.Fatal("a host with a transport must report a node identity")
	}
	return host, sharedHostReplicaID, addr
}

// TestRaftBackendFactory_TransportHostFoundsDedicatedShard pins the dedicated
// shard's founding on a clustered shared host: the factory states the local
// replica's identity (the host's own), so go-quicraft admits the one founding
// member instead of refusing the start with InvalidMemberError.
func TestRaftBackendFactory_TransportHostFoundsDedicatedShard(t *testing.T) {
	host, replicaID, addr := newSharedTransportTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, replicaID, addr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	const shardID = uint64(1000)
	src.add("rt", "app", shardID, dataRoot)

	svcs, err := f.Services(ctx, "rt", "app")
	if err != nil {
		t.Fatalf("Services on a transport host: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, err)
	}
}

// TestRaftBackendFactory_FoundsThreeReplicaDedicatedShard is go-qrdb#556's
// founding path for IsolationRaft: three hosts found one dedicated database
// shard from a three-replica placement. Every founder must name every
// member's identity; the factory reads the other two hosts' identities from
// the system shard's committed membership. With only its own identity, each
// founder is refused by go-quicraft.
func TestRaftBackendFactory_FoundsThreeReplicaDedicatedShard(t *testing.T) {
	trio := raftharness.NewCluster(t, 3)
	const shardID = uint64(1000)
	p := &placement.Placement{TenantID: "rt", Database: "app", ShardID: shardID}
	for i, addr := range trio.Addrs {
		p.Replicas = append(p.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: addr, RaftAddr: addr})
	}

	factories := make([]*RaftBackendFactory, 3)
	for i, host := range trio.Hosts {
		reg := newVolumePooledRegistry(t)
		dataRoot, _ := registerVolumeTenant(t, reg, "rt")
		src := newFakeRaftInfoSource()
		src.add("rt", "app", shardID, dataRoot)
		src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "app"}: p}
		factories[i] = NewRaftBackendFactory(host, uint64(i+1), trio.Addrs[i], reg, src, volTestLogger())
		f := factories[i]
		t.Cleanup(func() { _ = f.CloseAll() })
	}

	// Founders start together: each waits for a leader, which needs a quorum.
	svcs := make([]*DatabaseServices, 3)
	errs := make([]error, 3)
	var wg sync.WaitGroup
	for i, f := range factories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svcs[i], errs[i] = f.Services(context.Background(), "rt", "app")
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Services on the founders: %v", err)
	}

	m, err := trio.Hosts[0].SyncGetShardMembership(context.Background(), shardID)
	if err != nil {
		t.Fatalf("SyncGetShardMembership(dedicated): %v", err)
	}
	if len(m.Nodes) != 3 || len(m.Identities) != 3 {
		t.Fatalf("dedicated shard membership = %d nodes / %d identities, want 3 / 3", len(m.Nodes), len(m.Identities))
	}
	sys, err := trio.Hosts[0].SyncGetShardMembership(context.Background(), shard.ShardKV)
	if err != nil {
		t.Fatalf("SyncGetShardMembership(system): %v", err)
	}
	for id, identity := range m.Identities {
		if identity != sys.Identities[id] {
			t.Fatalf("dedicated replica %d committed %q, but its host's system identity is %q", id, identity, sys.Identities[id])
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	deadline := time.Now().Add(raftharness.Bound)
	for {
		var lastErr error
		for _, s := range svcs {
			if lastErr = s.KV.Put(ctx, "k", []byte("v")); lastErr == nil {
				break
			}
		}
		if lastErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no founder accepted a write: %v", lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRaftBackendFactory_FounderWithUnknownPeerRefused pins the typed refusal
// when a placement names a host the system shard has never committed.
func TestRaftBackendFactory_FounderWithUnknownPeerRefused(t *testing.T) {
	trio := raftharness.NewCluster(t, 3)
	const shardID = uint64(1001)
	p := &placement.Placement{TenantID: "rt", Database: "app", ShardID: shardID, Replicas: []placement.Replica{
		{ReplicaID: 1, NodeID: trio.Addrs[0], RaftAddr: trio.Addrs[0]},
		{ReplicaID: 2, NodeID: "127.0.0.1:9", RaftAddr: "127.0.0.1:9"},
	}}
	reg := newVolumePooledRegistry(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src := newFakeRaftInfoSource()
	src.add("rt", "app", shardID, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "app"}: p}
	f := NewRaftBackendFactory(trio.Hosts[0], 1, trio.Addrs[0], reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	_, err := f.Services(context.Background(), "rt", "app")
	var mie *service.MemberIdentityError
	var unknown *cluster.MemberIdentityUnknownError
	if !errors.As(err, &mie) || !errors.As(err, &unknown) || unknown.ReplicaID != 2 {
		t.Fatalf("Services = %v, want a *service.MemberIdentityError naming replica 2", err)
	}
}
