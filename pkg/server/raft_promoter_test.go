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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// startLocalRaftShard starts one dedicated raft database shard on a fresh
// transport-less shared host, through the real factory, and returns the
// factory, the host's promoter and the shard's on-disk data directory. The
// shard has one local voter, so it elects itself and commits on its own.
func startLocalRaftShard(t *testing.T, tenantID, db string, shardID uint64) (*RaftBackendFactory, *raftPromoter, string) {
	t.Helper()
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	dataRoot, _ := registerVolumeTenant(t, reg, tenantID)
	src.add(tenantID, db, shardID, dataRoot)
	svcs, err := f.Services(context.Background(), tenantID, db)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	// A committed write proves the shard has a leader before any assertion
	// about leadership is made.
	if err := svcs.KV.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	return f, newRaftPromoter(host), filepath.Join(dataRoot, "databases", db)
}

// TestRaftPromoter_LeaderReplicaID_LocalLeader asserts the adapter reports the
// shared host's view of the leader: the single local replica.
func TestRaftPromoter_LeaderReplicaID_LocalLeader(t *testing.T) {
	_, p, _ := startLocalRaftShard(t, "rp", "app", 1100)

	leader, ok, err := p.LeaderReplicaID(context.Background(), 1100)
	if err != nil {
		t.Fatalf("LeaderReplicaID: %v", err)
	}
	if !ok || leader != sharedHostReplicaID {
		t.Fatalf("LeaderReplicaID = (%d, %v), want (%d, true)", leader, ok, sharedHostReplicaID)
	}
}

// TestRaftPromoter_UnknownShard asserts both calls surface the host's error for
// a shard it does not run, rather than reporting a leader or a promotion.
func TestRaftPromoter_UnknownShard(t *testing.T) {
	host, _ := newSharedTestHost(t)
	p := newRaftPromoter(host)

	if _, ok, err := p.LeaderReplicaID(context.Background(), 4242); err == nil || ok {
		t.Fatalf("LeaderReplicaID(unknown) = ok %v, err %v; want an error", ok, err)
	}
	if err := p.PromoteObserver(context.Background(), 4242, 2, "127.0.0.1:63912"); err == nil {
		t.Fatal("PromoteObserver(unknown shard) returned nil; want the host's error")
	}
}

// TestRaftPromoter_PromoteObserver_CommitsConfigChange drives the blocking
// path end to end: the leader issues the membership change and the call
// returns only once it has committed. With one voter the change commits on the
// leader's own vote, so the result is success.
func TestRaftPromoter_PromoteObserver_CommitsConfigChange(t *testing.T) {
	_, p, _ := startLocalRaftShard(t, "rp", "app", 1101)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.PromoteObserver(ctx, 1101, 2, "127.0.0.1:63913"); err != nil {
		t.Fatalf("PromoteObserver: %v", err)
	}
}

// TestRaftBackendFactory_StartedAsObserver asserts the flag is false both for
// a shard that is not running and for one started as a founding voter.
func TestRaftBackendFactory_StartedAsObserver(t *testing.T) {
	f, _, _ := startLocalRaftShard(t, "rp", "app", 1102)

	if f.StartedAsObserver("rp", "app") {
		t.Error("StartedAsObserver = true for a shard started as a founding voter")
	}
	if f.StartedAsObserver("rp", "missing") {
		t.Error("StartedAsObserver = true for a shard that is not running")
	}
}

// TestRaftBackendFactory_CloseAndPurge_RemovesDataDir asserts demotion teardown
// stops the shard AND deletes its on-disk data, which is the point of the
// method: a demoted replica must not keep its tenant's ciphertext.
func TestRaftBackendFactory_CloseAndPurge_RemovesDataDir(t *testing.T) {
	f, _, dataDir := startLocalRaftShard(t, "rp", "app", 1103)
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("precondition: the shard's data dir %q must exist: %v", dataDir, err)
	}

	if err := f.CloseAndPurge("rp", "app"); err != nil {
		t.Fatalf("CloseAndPurge: %v", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data dir %q still present after CloseAndPurge (stat err %v)", dataDir, err)
	}
	f.mu.Lock()
	_, running := f.shards[raftShardKey{tenantID: "rp", database: "app"}]
	f.mu.Unlock()
	if running {
		t.Fatal("the shard is still registered after CloseAndPurge")
	}
}

// TestRaftBackendFactory_CloseAndPurge_RefusesADirectoryOutsideThisNodesPool
// asserts the purge never deletes outside this node's own volume pool: a shard
// whose directory resolved anywhere else -- another node's path on a shared
// host -- is stopped, its directory is left exactly as it was, and the purge is
// refused with *seal.DataRootOutsideLocalRootsError.
func TestRaftBackendFactory_CloseAndPurge_RefusesADirectoryOutsideThisNodesPool(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	registerVolumeTenant(t, reg, "rp")
	foreignRoot := filepath.Join(t.TempDir(), "another-node", "rp")
	src.add("rp", "app", 1105, foreignRoot)
	if _, err := f.Services(context.Background(), "rp", "app"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	foreignDir := filepath.Join(foreignRoot, "databases", "app")
	if _, err := os.Stat(foreignDir); err != nil {
		t.Fatalf("precondition: the shard's data dir %q must exist: %v", foreignDir, err)
	}

	err := f.CloseAndPurge("rp", "app")
	var outside *seal.DataRootOutsideLocalRootsError
	if !errors.As(err, &outside) || outside.DataRoot != foreignDir {
		t.Fatalf("CloseAndPurge = %v, want *seal.DataRootOutsideLocalRootsError for %s", err, foreignDir)
	}
	if _, err := os.Stat(foreignDir); err != nil {
		t.Fatalf("a directory outside this node's pool was removed: %v", err)
	}
}

// TestRaftBackendFactory_CloseAndPurge_UnknownIsNoop asserts an unknown pair
// is a no-op rather than an error, as the reconciler calls it for any demoted
// placement whether or not this node ever started the shard.
func TestRaftBackendFactory_CloseAndPurge_UnknownIsNoop(t *testing.T) {
	f, _, dataDir := startLocalRaftShard(t, "rp", "app", 1104)

	if err := f.CloseAndPurge("rp", "other"); err != nil {
		t.Fatalf("CloseAndPurge(unknown) = %v, want nil", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("an unrelated shard's data dir was touched: %v", err)
	}
}
