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
	"os"
	"path/filepath"
	"testing"
)

// TestRaftBackendFactory_PurgeHoldsOffARecreate drops a database's group and,
// while the purge is held after the key is forgotten, recreates the name on a
// new shard under the same directory. The recreate must not start until the
// purge has stopped the old shard and removed its directory: started inside
// the window, the purge's RemoveAll would delete the successor's files.
func TestRaftBackendFactory_PurgeHoldsOffARecreate(t *testing.T) {
	const tenantID, db = "rp", "app"
	const oldShard, newShard = uint64(1191), uint64(1192)
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, tenantID)
	src.add(tenantID, db, oldShard, dataRoot)
	svcs, err := f.Services(ctx, tenantID, db)
	if err != nil {
		t.Fatalf("Services(old): %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("Put(old): %v", err)
	}

	// The recreate is attempted inside the purge window. Held off, it
	// reports its wait and finishes once the purge is done; let through, it
	// starts the successor's group before the purge removes the directory.
	type result struct {
		svcs *DatabaseServices
		err  error
	}
	recreated := make(chan result, 1)
	waited := make(chan struct{})
	f.purgeWaitHook = func(raftShardKey) { close(waited) }
	f.purgeHook = func(key raftShardKey) {
		if key != (raftShardKey{tenantID: tenantID, database: db}) {
			return
		}
		src.add(tenantID, db, newShard, dataRoot)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s, err := f.Services(ctx, tenantID, db)
			recreated <- result{svcs: s, err: err}
		}()
		select {
		case <-waited:
		case <-done:
		}
	}
	if err := f.CloseAndPurge(tenantID, db); err != nil {
		t.Fatalf("CloseAndPurge: %v", err)
	}
	r := <-recreated
	if r.err != nil {
		t.Fatalf("Services(new): %v", r.err)
	}
	svcs = r.svcs
	if err := svcs.KV.Put(ctx, "k", []byte("new")); err != nil {
		t.Fatalf("Put(new): %v", err)
	}
	successorDir := filepath.Join(dataRoot, "databases", db)
	if _, err := os.Stat(successorDir); err != nil {
		t.Fatalf("the successor's directory %q is gone after the purge: %v", successorDir, err)
	}
	f.mu.Lock()
	rs := f.shards[raftShardKey{tenantID: tenantID, database: db}]
	_, purging := f.purging[raftShardKey{tenantID: tenantID, database: db}]
	f.mu.Unlock()
	if rs == nil || rs.shardID != newShard {
		t.Fatalf("cached shard = %+v, want the successor shard %d", rs, newShard)
	}
	if purging {
		t.Fatal("the purge reservation outlived the purge")
	}
}

// TestRaftBackendFactory_PurgeRemovesTheReplicasLogData purges a started
// group and reads the host's LogDB: the replica's log entries, hard state
// and bootstrap record must be gone, not only its directory. They live in the
// node's shared WAL, outside the directory a purge removes.
func TestRaftBackendFactory_PurgeRemovesTheReplicasLogData(t *testing.T) {
	const tenantID, db = "rl", "app"
	const shardID = uint64(1193)
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, tenantID)
	src.add(tenantID, db, shardID, dataRoot)
	svcs, err := f.Services(ctx, tenantID, db)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f.mu.Lock()
	replicaID := f.shards[raftShardKey{tenantID: tenantID, database: db}].replicaID
	f.mu.Unlock()
	if _, ok, err := mux.GetBootstrap(shardID, replicaID); err != nil || !ok {
		t.Fatalf("precondition: GetBootstrap = ok %v, err %v; want the started replica's bootstrap", ok, err)
	}
	if _, count, err := mux.EntryRange(shardID, replicaID); err != nil || count == 0 {
		t.Fatalf("precondition: EntryRange count %d, err %v; want the replica's entries", count, err)
	}

	if err := f.CloseAndPurge(tenantID, db); err != nil {
		t.Fatalf("CloseAndPurge: %v", err)
	}
	if _, ok, err := mux.GetBootstrap(shardID, replicaID); err == nil && ok {
		t.Fatal("the LogDB still holds the purged replica's bootstrap record")
	}
	if _, count, err := mux.EntryRange(shardID, replicaID); err == nil && count != 0 {
		t.Fatalf("the LogDB still holds %d entries of the purged replica", count)
	}
	infos, err := mux.ListNodeInfo()
	if err != nil {
		t.Fatalf("ListNodeInfo: %v", err)
	}
	for _, ni := range infos {
		if ni.ShardID == shardID && ni.ReplicaID == replicaID {
			t.Fatalf("the LogDB still lists the purged replica (shard %d, replica %d)", shardID, replicaID)
		}
	}
}
