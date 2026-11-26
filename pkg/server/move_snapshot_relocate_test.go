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
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
)

type fakeSnapshotRecords struct {
	rec logdb.Snapshot
	err error
}

func (f *fakeSnapshotRecords) GetSnapshot(uint64, uint64) (logdb.Snapshot, error) {
	return f.rec, f.err
}

type fakeSnapshotHost struct {
	snapErrs  []error
	snapshots int
	proposals int
}

func (f *fakeSnapshotHost) SyncRequestSnapshot(context.Context, uint64, quicraft.SnapshotOption) (uint64, error) {
	f.snapshots++
	if len(f.snapErrs) == 0 {
		return 1, nil
	}
	err := f.snapErrs[0]
	f.snapErrs = f.snapErrs[1:]
	return 0, err
}

func (f *fakeSnapshotHost) SyncPropose(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	f.proposals++
	return sm.Result{}, nil
}

type fakeSnapshotShards []SnapshotShard

func (f fakeSnapshotShards) SnapshotShards(string) ([]SnapshotShard, error) { return f, nil }

func TestMoveStoreCloser_RelocateSnapshots(t *testing.T) {
	root := filepath.FromSlash("/pools/b/t1")
	outside := filepath.FromSlash("/pools/a/t1/nodehost/snapshots/shard-1/snapshot-1")
	inside := filepath.Join(root, "nodehost", "snapshots", "shard-1", "snapshot-2")
	cases := []struct {
		name      string
		records   *fakeSnapshotRecords
		snapErrs  []error
		snapshots int
		proposals int
		wantErr   bool
	}{
		{name: "outside root", records: &fakeSnapshotRecords{rec: logdb.Snapshot{Filepath: outside}}, snapshots: 1},
		{name: "inside root", records: &fakeSnapshotRecords{rec: logdb.Snapshot{Filepath: inside}}},
		{name: "no snapshot", records: &fakeSnapshotRecords{err: logdb.ErrNoSnapshot}},
		{name: "empty record", records: &fakeSnapshotRecords{}},
		{name: "out of date", records: &fakeSnapshotRecords{rec: logdb.Snapshot{Filepath: outside}}, snapErrs: []error{logdb.ErrSnapshotOutOfDate}, snapshots: 2, proposals: 1},
		{name: "refused", records: &fakeSnapshotRecords{rec: logdb.Snapshot{Filepath: outside}}, snapErrs: []error{errors.New("refused")}, snapshots: 1, wantErr: true},
		{name: "record unreadable", records: &fakeSnapshotRecords{err: errors.New("io")}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host := &fakeSnapshotHost{snapErrs: c.snapErrs}
			closer := &MoveStoreCloser{snapshots: fakeSnapshotShards{{ShardID: 1, ReplicaID: 1, Records: c.records, Host: host}}}
			err := closer.RelocateSnapshots(context.Background(), "t1", root)
			if (err != nil) != c.wantErr {
				t.Fatalf("RelocateSnapshots error = %v, want error %v", err, c.wantErr)
			}
			if host.snapshots != c.snapshots || host.proposals != c.proposals {
				t.Fatalf("snapshots = %d, proposals = %d; want %d, %d", host.snapshots, host.proposals, c.snapshots, c.proposals)
			}
		})
	}
}

// raftSnapshotShards is a raft-mode shard lifecycle: the tenant's dedicated
// shards run on the shared node Host and are listed by the factory itself.
type raftSnapshotShards struct {
	fakeSnapshotShards
	tenants []string
}

func (raftSnapshotShards) Attach(context.Context, string, string) error { return nil }
func (raftSnapshotShards) Close(string, string) error                   { return nil }
func (raftSnapshotShards) ListStarted() []StartedShard                  { return nil }

func (r *raftSnapshotShards) SnapshotShards(tenantID string) ([]SnapshotShard, error) {
	r.tenants = append(r.tenants, tenantID)
	return r.fakeSnapshotShards, nil
}

// TestMoveStoreCloser_RelocateSnapshots_RaftShards: in raft mode no volume
// Host lists the tenant's shards, so a settle re-snapshots the dedicated
// shards the raft factory runs for it on the node Host.
func TestMoveStoreCloser_RelocateSnapshots_RaftShards(t *testing.T) {
	root := filepath.FromSlash("/pools/b/t1")
	outside := filepath.FromSlash("/pools/a/t1/databases/db1/snapshots/snapshot-1")
	host := &fakeSnapshotHost{}
	shards := &raftSnapshotShards{fakeSnapshotShards: fakeSnapshotShards{
		{ShardID: 7, ReplicaID: 2, Records: &fakeSnapshotRecords{rec: logdb.Snapshot{Filepath: outside}}, Host: host},
	}}
	closer := &MoveStoreCloser{shards: shards}
	if err := closer.RelocateSnapshots(context.Background(), "t1", root); err != nil {
		t.Fatalf("RelocateSnapshots: %v", err)
	}
	if host.snapshots != 1 {
		t.Fatalf("raft shard snapshots = %d, want 1", host.snapshots)
	}
	if len(shards.tenants) != 1 || shards.tenants[0] != "t1" {
		t.Fatalf("raft shards listed for %v, want [t1]", shards.tenants)
	}
}

// TestMoveStoreCloser_RelocateSnapshots_RaftShardsNoRecords: a raft factory
// that runs a started dedicated shard of the tenant but has no multiplex
// LogDB cannot read its snapshot record, so the settle fails naming the
// shard and the tenant instead of skipping it.
func TestMoveStoreCloser_RelocateSnapshots_RaftShardsNoRecords(t *testing.T) {
	f := &RaftBackendFactory{
		host: &quicraft.Host{},
		shards: map[raftShardKey]*raftShard{
			{tenantID: "t1", database: "db1"}: {tenantID: "t1", database: "db1", shardID: 7, replicaID: 2},
			{tenantID: "t2", database: "db1"}: {tenantID: "t2", database: "db1", shardID: 8, replicaID: 2},
		},
	}
	closer := &MoveStoreCloser{snapshots: f}
	err := closer.RelocateSnapshots(context.Background(), "t1", filepath.FromSlash("/pools/b/t1"))
	if err == nil {
		t.Fatal("RelocateSnapshots error = nil, want an error for the unreadable shard")
	}
	if msg := err.Error(); !strings.Contains(msg, "shard 7") || !strings.Contains(msg, "tenant t1") {
		t.Fatalf("RelocateSnapshots error = %q, want it to name shard 7 and tenant t1", msg)
	}
	if err := closer.RelocateSnapshots(context.Background(), "t3", filepath.FromSlash("/pools/b/t3")); err != nil {
		t.Fatalf("RelocateSnapshots of a tenant with no shard: %v", err)
	}
}

// TestMoveStoreCloser_RelocateSnapshots_TenantHostNoRecords: a started
// volume tenant Host with no LogDB cannot report its snapshot record, so the
// settle fails naming the shard and the tenant.
func TestMoveStoreCloser_RelocateSnapshots_TenantHostNoRecords(t *testing.T) {
	r := &TenantHostRegistry{hosts: map[string]*tenantHost{
		"t1": {tenantID: "t1", host: &quicraft.Host{}},
	}}
	closer := &MoveStoreCloser{snapshots: r}
	err := closer.RelocateSnapshots(context.Background(), "t1", filepath.FromSlash("/pools/b/t1"))
	if err == nil {
		t.Fatal("RelocateSnapshots error = nil, want an error for the unreadable tenant Host shard")
	}
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("shard %d", tenantHostShardID)) || !strings.Contains(msg, "tenant t1") {
		t.Fatalf("RelocateSnapshots error = %q, want it to name shard %d and tenant t1", msg, tenantHostShardID)
	}
	if err := closer.RelocateSnapshots(context.Background(), "t2", filepath.FromSlash("/pools/b/t2")); err != nil {
		t.Fatalf("RelocateSnapshots of a tenant with no Host: %v", err)
	}
}
