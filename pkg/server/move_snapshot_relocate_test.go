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
	"path/filepath"
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

func (f fakeSnapshotShards) SnapshotShards(string) []SnapshotShard { return f }

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
