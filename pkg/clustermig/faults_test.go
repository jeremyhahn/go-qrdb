// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errKV is a KV whose operations fail with a fixed error, to exercise the
// store's error-wrapping branches.
type errKV struct{ err error }

func (k errKV) Put(context.Context, string, []byte) error   { return k.err }
func (k errKV) Get(context.Context, string) ([]byte, error) { return nil, k.err }
func (k errKV) PutAllIfUnchanged(context.Context, string, []byte, []kvstore.KV) (bool, error) {
	return false, k.err
}
func (k errKV) Scan(context.Context, string, func(string, []byte) error) error {
	return k.err
}

var errBoom = errors.New("kv unavailable")

// TestStoreError_ErrorAndUnwrap covers the StoreError string + Unwrap.
func TestStoreError_ErrorAndUnwrap(t *testing.T) {
	se := &StoreError{Op: "put-migration", Err: errBoom}
	if !strings.Contains(se.Error(), "put-migration") || !strings.Contains(se.Error(), "kv unavailable") {
		t.Fatalf("StoreError.Error() = %q", se.Error())
	}
	if !errors.Is(se, errBoom) {
		t.Fatal("StoreError must unwrap to its cause")
	}
}

// TestStore_KVErrorsWrapped proves every store read/write wraps a KV failure as a
// StoreError carrying the originating cause.
func TestStore_KVErrorsWrapped(t *testing.T) {
	ctx := context.Background()
	s := NewStore(errKV{err: errBoom})
	m := &ClusterMigration{TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Owners: []string{"n1"}, Generation: 1}

	checks := []struct {
		name string
		fn   func() error
	}{
		{"swapMigration", func() error { return s.swapMigration(ctx, nil, m) }},
		{"GetMigration", func() error { _, e := s.GetMigration(ctx, tid, db); return e }},
		{"PutNodeStatus", func() error {
			return s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{NodeID: "n1"})
		}},
		{"GetNodeStatus", func() error { _, e := s.GetNodeStatus(ctx, tid, db, "n1"); return e }},
		{"ListNodeStatuses", func() error { _, e := s.ListNodeStatuses(ctx, tid, db); return e }},
		{"ListMigrations", func() error { _, e := s.ListMigrations(ctx); return e }},
		{"IsComplete", func() error { _, e := s.IsComplete(ctx, tid, db); return e }},
		{"Progress", func() error { _, _, e := s.Progress(ctx, tid, db); return e }},
		{"Delete", func() error { return s.Delete(ctx, tid, db) }},
	}
	for _, c := range checks {
		if err := c.fn(); !errors.Is(err, errBoom) {
			t.Errorf("%s error = %v, want wrapped errBoom", c.name, err)
		}
	}
}

// TestCoordinator_Request_PrevReadError surfaces a generation-probe read failure.
func TestCoordinator_Request_PrevReadError(t *testing.T) {
	c := NewCoordinator(NewStore(errKV{err: errBoom}),
		fixedOwners{shardID: 1, owners: []string{"n1"}}, func() bool { return true }, nil)
	if _, err := c.Request(context.Background(), tid, db, validOpts()); !errors.Is(err, errBoom) {
		t.Fatalf("Request = %v, want wrapped errBoom from prev-read", err)
	}
}

// TestNewCoordinator_NilLeaderDefaultsLeader proves a nil isLeader defaults to
// acting as leader (single-node / embedded use).
func TestNewCoordinator_NilLeaderDefaultsLeader(t *testing.T) {
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 1, owners: []string{"n1"}}, nil, nil)
	if _, err := c.Request(context.Background(), tid, db, validOpts()); err != nil {
		t.Fatalf("Request with nil isLeader = %v, want nil (defaults leader)", err)
	}
}

// TestReconciler_ListError surfaces a list failure from a pass.
func TestReconciler_ListError(t *testing.T) {
	r := NewReconciler(NewStore(errKV{err: errBoom}), "n1", &recordingRecycler{}, nil, nil, nil)
	if _, err := r.ReconcileOnce(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("ReconcileOnce = %v, want wrapped errBoom", err)
	}
}

// TestReconciler_PutStatusErrorSwallowed proves a status-write failure does not
// abort the recycle: the recycle still ran and the pass reports the recycle.
func TestReconciler_PutStatusErrorSwallowed(t *testing.T) {
	// A KV that serves the intent list but fails status writes.
	mem := newMemKV()
	s := NewStore(mem)
	seedMigration(t, s, 1, []string{"n1"})

	// Swap to a KV that reads the seeded record but errors on Put.
	fk := &readOKWriteErrKV{inner: mem, putErr: errBoom}
	s2 := NewStore(fk)
	rec := &recordingRecycler{}
	r := NewReconciler(s2, "n1", rec, nil, func() int64 { return 42 }, nil)

	n, err := r.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce = %v, want nil (status errors swallowed)", err)
	}
	if n != 1 || rec.count() != 1 {
		t.Fatalf("recycled=%d calls=%d, want 1/1 despite status-write failure", n, rec.count())
	}
}

func TestStore_CorruptRecordsRejected(t *testing.T) {
	ctx := context.Background()
	kv := newMemKV()
	s := NewStore(kv)
	kv.m[migrationKey(tid, db)] = []byte("{not json")
	kv.m[nodeStatusKey(tid, db, "n1")] = []byte("{not json")

	checks := []struct {
		name string
		op   string
		fn   func() error
	}{
		{"GetMigration", "unmarshal-migration", func() error { _, e := s.GetMigration(ctx, tid, db); return e }},
		{"ListMigrations", "unmarshal-migration", func() error { _, e := s.ListMigrations(ctx); return e }},
		{"GetNodeStatus", "unmarshal-status", func() error { _, e := s.GetNodeStatus(ctx, tid, db, "n1"); return e }},
		{"ListNodeStatuses", "unmarshal-status", func() error { _, e := s.ListNodeStatuses(ctx, tid, db); return e }},
	}
	for _, c := range checks {
		var se *StoreError
		if err := c.fn(); !errors.As(err, &se) || se.Op != c.op {
			t.Errorf("%s error = %v, want StoreError op %q", c.name, err, c.op)
		}
	}
}

func TestStore_ListNodeStatuses_SkipsNestedKeys(t *testing.T) {
	ctx := context.Background()
	kv := newMemKV()
	s := NewStore(kv)
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{NodeID: "n1"}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	kv.m[nodeStatusKey(tid, db, "n1")+"/nested"] = []byte("{not json")

	got, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil {
		t.Fatalf("ListNodeStatuses: %v", err)
	}
	if len(got) != 1 || got[0].NodeID != "n1" {
		t.Fatalf("ListNodeStatuses = %+v, want only n1", got)
	}
}

// readOKWriteErrKV reads from an inner KV but fails all writes, to prove the
// reconciler swallows status-write errors after a successful recycle.
type readOKWriteErrKV struct {
	inner  KV
	putErr error
}

func (k *readOKWriteErrKV) Put(context.Context, string, []byte) error { return k.putErr }
func (k *readOKWriteErrKV) Get(ctx context.Context, key string) ([]byte, error) {
	return k.inner.Get(ctx, key)
}
func (k *readOKWriteErrKV) PutAllIfUnchanged(context.Context, string, []byte, []kvstore.KV) (bool, error) {
	return false, k.putErr
}
func (k *readOKWriteErrKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return k.inner.Scan(ctx, prefix, fn)
}
