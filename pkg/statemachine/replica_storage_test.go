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

package statemachine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// mustOpenStorage opens one replica's storage through f, failing the test on
// an error.
func mustOpenStorage(t *testing.T, f StorageFactory, shardID, replicaID uint64) storage.Storage {
	t.Helper()
	s, err := f(shardID, replicaID)
	if err != nil {
		t.Fatalf("open storage for shard %d replica %d: %v", shardID, replicaID, err)
	}
	return s
}

// replicaCall is one call a recordingReplicaFactory saw.
type replicaCall struct {
	shardID, replicaID uint64
	dir                string
}

// recordingReplicaFactory is a composer's ReplicaStorageOpener that opens
// real memory storage and records what it was handed.
type recordingReplicaFactory struct {
	calls []replicaCall
	err   error
}

func (f *recordingReplicaFactory) open(shardID, replicaID uint64, dir string) (storage.Storage, error) {
	f.calls = append(f.calls, replicaCall{shardID: shardID, replicaID: replicaID, dir: dir})
	if f.err != nil {
		return nil, f.err
	}
	return memstore.NewStorage(), nil
}

func TestNewStorageFactory_ReplicaFactoryOpensEachReplica(t *testing.T) {
	root := t.TempDir()
	rf := &recordingReplicaFactory{}
	// Kind is ignored once Replica is set: the composer's factory decides.
	f := newStorageFactory(StorageConfig{Root: root, Kind: "file", Replica: rf.open})
	for r := uint64(1); r <= 3; r++ {
		s := mustOpenStorage(t, f, 4, r)
		if _, ok := s.(*memstore.MemoryStorage); !ok {
			t.Fatalf("replica %d: storage %T, want the composer's", r, s)
		}
	}
	if len(rf.calls) != 3 {
		t.Fatalf("factory called %d times, want 3", len(rf.calls))
	}
	seen := map[string]bool{}
	for i, c := range rf.calls {
		want := filepath.Join(root, "sm", "shard-4-replica-"+strconv.Itoa(i+1))
		if c.shardID != 4 || c.replicaID != uint64(i+1) || c.dir != want {
			t.Fatalf("call %d = (%d, %d, %q), want (4, %d, %q)", i, c.shardID, c.replicaID, c.dir, i+1, want)
		}
		if fi, err := os.Stat(c.dir); err != nil || !fi.IsDir() {
			t.Fatalf("replica directory %q not created: %v", c.dir, err)
		}
		seen[c.dir] = true
	}
	if len(seen) != 3 {
		t.Fatalf("replicas shared a directory: %v", seen)
	}
}

func TestNewStorageFactory_ReplicaFactoryWithoutRoot(t *testing.T) {
	rf := &recordingReplicaFactory{}
	s := mustOpenStorage(t, newStorageFactory(StorageConfig{Replica: rf.open}), 2, 5)
	_ = s.Close()
	if len(rf.calls) != 1 {
		t.Fatalf("factory called %d times, want 1", len(rf.calls))
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(rf.calls[0].dir)) })
	if fi, err := os.Stat(rf.calls[0].dir); err != nil || !fi.IsDir() {
		t.Fatalf("temporary replica directory %q not created: %v", rf.calls[0].dir, err)
	}
}

func TestNewStorageFactory_ReplicaFactoryErrorIsTyped(t *testing.T) {
	cause := errors.New("device offline")
	rf := &recordingReplicaFactory{err: cause}
	s, err := newStorageFactory(StorageConfig{Root: t.TempDir(), Replica: rf.open})(6, 2)
	if s != nil {
		t.Fatalf("storage = %v, want nil on failure", s)
	}
	var openErr *ReplicaStorageOpenError
	if !errors.As(err, &openErr) || openErr.ShardID != 6 || openErr.ReplicaID != 2 {
		t.Fatalf("err = %v, want ReplicaStorageOpenError for shard 6 replica 2", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v does not wrap the factory's error", err)
	}
	if !strings.Contains(err.Error(), "shard 6 replica 2") || !strings.Contains(err.Error(), "device offline") {
		t.Fatalf("message %q lacks the replica or the cause", err.Error())
	}
}

func TestNewStorageFactory_DirectoryFailureIsTyped(t *testing.T) {
	// Root is a file, so the replica directory cannot be created under it.
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sc := range []StorageConfig{{Root: root}, {Root: root, Replica: (&recordingReplicaFactory{}).open}} {
		_, err := newStorageFactory(sc)(1, 1)
		var openErr *ReplicaStorageOpenError
		var stErr *StorageError
		if !errors.As(err, &openErr) || !errors.As(err, &stErr) {
			t.Fatalf("err = %v, want ReplicaStorageOpenError over StorageError", err)
		}
	}
}

func TestResolveTypedWithStorage_ReplicaFactory(t *testing.T) {
	rf := &recordingReplicaFactory{}
	sc := StorageConfig{Root: t.TempDir(), Replica: rf.open}
	for _, smType := range []string{"disk", "fast"} {
		if _, err := ResolveTypedWithStorage(smType, "", sc); err != nil {
			t.Fatalf("%s honours the replica factory: %v", smType, err)
		}
	}
	for _, smType := range []string{"memory", "concurrent", "highperf", "regular"} {
		_, err := ResolveTypedWithStorage(smType, "", sc)
		var na *ReplicaStorageNotApplicableError
		if !errors.As(err, &na) || na.SMType != smType {
			t.Fatalf("%s: err = %v, want ReplicaStorageNotApplicableError", smType, err)
		}
		if !strings.Contains(err.Error(), strconv.Quote(smType)) {
			t.Fatalf("message %q does not name %s", err.Error(), smType)
		}
	}
}

func TestKVCreateFuncs_StorageFailureFailsOpen(t *testing.T) {
	cause := errors.New("no storage")
	failing := func(shardID, replicaID uint64) (storage.Storage, error) {
		return nil, &ReplicaStorageOpenError{ShardID: shardID, ReplicaID: replicaID, Err: cause}
	}
	for name, create := range map[string]sm.CreateFunc{
		"disk": NewKVDiskCreateFunc(failing),
		"fast": NewKVFastCreateFunc(failing),
	} {
		raw := create(3, 1)
		d, ok := raw.(sm.DiskStateMachine)
		if !ok {
			t.Fatalf("%s: a failed replica is %T, want one whose Open reports the failure", name, raw)
		}
		_, err := d.Open(context.Background(), "", nil)
		var openErr *ReplicaStorageOpenError
		if !errors.As(err, &openErr) || openErr.ShardID != 3 || !errors.Is(err, cause) {
			t.Fatalf("%s: Open err = %v, want the typed storage failure", name, err)
		}
		if err := d.Close(context.Background()); err != nil {
			t.Fatalf("%s: Close of an unopened replica: %v", name, err)
		}
	}
}

func TestUnopenedReplica_EveryCallReportsTheFailure(t *testing.T) {
	cause := errors.New("unopened")
	r := &unopenedReplica{err: cause}
	ctx := context.Background()
	if err := r.Update(ctx, nil, nil); !errors.Is(err, cause) {
		t.Fatalf("Update: %v", err)
	}
	if _, err := r.Lookup(ctx, nil); !errors.Is(err, cause) {
		t.Fatalf("Lookup: %v", err)
	}
	if err := r.Sync(); !errors.Is(err, cause) {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := r.PrepareSnapshot(); !errors.Is(err, cause) {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	if err := r.SaveSnapshot(ctx, nil, nil, nil); !errors.Is(err, cause) {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := r.RecoverFromSnapshot(ctx, nil, nil); !errors.Is(err, cause) {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}
}

func TestKVCreateFuncs_OpenThroughReplicaFactory(t *testing.T) {
	rf := &recordingReplicaFactory{}
	f := newStorageFactory(StorageConfig{Root: t.TempDir(), Replica: rf.open})
	ctx := context.Background()
	disk, ok := NewKVDiskCreateFunc(f)(1, 2).(sm.DiskStateMachine)
	if !ok {
		t.Fatal("disk create func did not return a disk state machine")
	}
	if _, err := disk.Open(ctx, "", nil); err != nil {
		t.Fatalf("disk Open: %v", err)
	}
	_ = disk.Close(ctx)
	fast, ok := NewKVFastCreateFunc(f)(1, 3).(*StandardAdapter)
	if !ok {
		t.Fatal("fast create func did not return a StandardAdapter")
	}
	_ = fast.Close(ctx)
	if len(rf.calls) != 2 || rf.calls[0].replicaID != 2 || rf.calls[1].replicaID != 3 {
		t.Fatalf("calls = %+v, want replicas 2 then 3", rf.calls)
	}
}
