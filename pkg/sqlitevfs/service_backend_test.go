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

package sqlitevfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// serviceDouble answers the ServiceKV and ServiceTxn calls from a real
// MVCCBackend, with the service's error shapes: a key with no live value is
// a *service.KeyNotFoundError, a prefix move whose live count differs is a
// *service.CommandConflictError. getErr injects a read failure; strayUnder,
// when set, commits one extra key under the move's source prefix before the
// move, as a concurrent writer would.
type serviceDouble struct {
	b          *MVCCBackend
	getErr     error
	strayUnder bool
	moves      int
	// execErr and moveErr, when set, are answered by Execute and MovePrefix
	// before the double applies anything, as a refusing service would.
	execErr error
	moveErr error
	// seen records, per double method, the context its last call received.
	seen map[string]context.Context
}

func (d *serviceDouble) record(method string, ctx context.Context) {
	if d.seen == nil {
		d.seen = make(map[string]context.Context)
	}
	d.seen[method] = ctx
}

func (d *serviceDouble) GetAtRevision(ctx context.Context, key string, revision int64) (*service.KeyValue, error) {
	d.record("GetAtRevision", ctx)
	if d.getErr != nil {
		return nil, d.getErr
	}
	value, found, err := d.b.GetAt(ctx, []byte(key), revision)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &service.KeyValue{Key: []byte(key), Value: value}, nil
}

func (d *serviceDouble) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	d.record("Range", ctx)
	current := d.b.store.CurrentRevision()
	kvs, count, _, err := d.b.store.RangeFrom(d.b.store.Storage(), current, start, end, opts...)
	if err != nil {
		return nil, 0, 0, err
	}
	return kvs, count, current, nil
}

func (d *serviceDouble) CurrentRevision(ctx context.Context) (int64, error) {
	d.record("CurrentRevision", ctx)
	return d.b.Revision(ctx)
}

func (d *serviceDouble) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	d.record("MovePrefix", ctx)
	if err := service.ValidateMovePrefix(req); err != nil {
		return 0, err
	}
	d.moves++
	if d.moveErr != nil {
		return 0, d.moveErr
	}
	if d.strayUnder {
		if _, err := d.b.Commit(ctx, nil, map[string][]byte{req.Src + "\xff\xff\xff\xff": []byte("stray")}, nil); err != nil {
			return 0, err
		}
	}
	puts, deletes := splitOps(req.Ops)
	rev, err := d.b.Publish(ctx, []byte(req.Src), []byte(req.Dst), req.ExpectedKeys, nil, puts, deletes)
	if errors.Is(err, ErrPublishConflict) {
		return 0, &service.CommandConflictError{Message: err.Error()}
	}
	return rev, err
}

func (d *serviceDouble) Execute(ctx context.Context, compares []service.Compare, successOps, _ []service.Op) (*service.TransactionResponse, error) {
	d.record("Execute", ctx)
	if d.execErr != nil {
		return nil, d.execErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// As the service does, the compares and the success ops are one atomic
	// step: a failed compare fails the whole transaction and applies nothing.
	d.b.mu.Lock()
	defer d.b.mu.Unlock()
	now := d.b.store.CurrentRevision()
	for _, c := range compares {
		ok, err := d.compareHolds(c, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &service.TransactionResponse{Succeeded: false, Revision: now}, nil
		}
	}
	puts, deletes := splitOps(successOps)
	rev, err := d.b.commitLocked(puts, deletes)
	if err != nil {
		return nil, err
	}
	return &service.TransactionResponse{Succeeded: true, Revision: rev}, nil
}

// compareHolds evaluates one compare against the live state at now, the way
// the service does: an absent key has mod revision 0. Only the mod revision
// target is modelled, since that is all the backend sends.
func (d *serviceDouble) compareHolds(c service.Compare, now int64) (bool, error) {
	if c.Target != service.CompareModRevision {
		return false, fmt.Errorf("serviceDouble: compare target %v is not modelled", c.Target)
	}
	var modRev int64
	if now > 0 {
		kv, err := d.b.store.GetAtRevision(c.Key, now)
		switch {
		case err == nil:
			modRev = kv.ModRevision
		case isMVCCNotFound(err):
		default:
			return false, err
		}
	}
	switch c.Result {
	case service.CompareEqual:
		return modRev == c.ModRevision, nil
	case service.CompareNotEqual:
		return modRev != c.ModRevision, nil
	case service.CompareGreater:
		return modRev > c.ModRevision, nil
	case service.CompareLess:
		return modRev < c.ModRevision, nil
	default:
		return false, fmt.Errorf("serviceDouble: compare result %v is not modelled", c.Result)
	}
}

func isMVCCNotFound(err error) bool {
	var notFound *mvcc.KeyNotFoundError
	var noRevision *mvcc.RevisionNotFoundError
	return errors.As(err, &notFound) || errors.As(err, &noRevision)
}

func splitOps(ops []service.Op) (map[string][]byte, [][]byte) {
	puts := make(map[string][]byte)
	var deletes [][]byte
	for _, op := range ops {
		switch op.Type {
		case lock.OpPut:
			puts[string(op.Key)] = op.Value
		case lock.OpDelete:
			deletes = append(deletes, op.Key)
		}
	}
	return puts, deletes
}

func newServiceBackend(t *testing.T) (*ServiceBackend, *serviceDouble) {
	t.Helper()
	d := &serviceDouble{b: newTestBackend(t)}
	sb, err := NewServiceBackend(d, d)
	if err != nil {
		t.Fatalf("NewServiceBackend: %v", err)
	}
	return sb, d
}

func TestNewServiceBackendRefusesNil(t *testing.T) {
	d := &serviceDouble{b: newTestBackend(t)}
	if _, err := NewServiceBackend(nil, d); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("nil kv err = %v, want ErrNilBackend", err)
	}
	if _, err := NewServiceBackend(d, nil); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("nil txn err = %v, want ErrNilBackend", err)
	}
}

// callKey is the context key the context tests tag each call with.
type callKey struct{}

func TestServiceBackendPassesCallContext(t *testing.T) {
	sb, d := newServiceBackend(t)
	if _, err := sb.Commit(t.Context(), nil, map[string][]byte{"src/a": []byte("1")}, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev := revOf(t, sb)
	tagged := func(v string) context.Context { return context.WithValue(t.Context(), callKey{}, v) }
	tests := []struct {
		name   string
		method string
		call   func(context.Context) error
	}{
		{"Revision", "CurrentRevision", func(ctx context.Context) error { _, err := sb.Revision(ctx); return err }},
		{"GetAt", "GetAtRevision", func(ctx context.Context) error { _, _, err := sb.GetAt(ctx, []byte("src/a"), rev); return err }},
		{"Keys", "Range", func(ctx context.Context) error { _, err := sb.Keys(ctx, []byte("src/"), rev); return err }},
		{"Commit", "Execute", func(ctx context.Context) error {
			_, err := sb.Commit(ctx, nil, map[string][]byte{"k": []byte("v")}, nil)
			return err
		}},
		{"Publish", "MovePrefix", func(ctx context.Context) error {
			_, err := sb.Publish(ctx, []byte("src/"), []byte("dst/"), 1, nil, nil, nil)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(tagged(tc.name)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			got := d.seen[tc.method]
			if got == nil {
				t.Fatalf("%s did not reach the double's %s", tc.name, tc.method)
			}
			if v := got.Value(callKey{}); v != tc.name {
				t.Fatalf("%s saw context value %v, want %q", tc.method, v, tc.name)
			}
		})
	}
}

func TestServiceBackendGetAt(t *testing.T) {
	sb, d := newServiceBackend(t)
	rev, err := sb.Commit(t.Context(), nil, map[string][]byte{"k": []byte("v")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	value, found, err := sb.GetAt(t.Context(), []byte("k"), rev)
	if err != nil || !found || string(value) != "v" {
		t.Fatalf("GetAt k = %q, %v, %v", value, found, err)
	}
	if _, found, err := sb.GetAt(t.Context(), []byte("absent"), rev); err != nil || found {
		t.Fatalf("GetAt absent: found=%v err=%v, want not found and no error", found, err)
	}
	d.getErr = errInjected
	if _, _, err := sb.GetAt(t.Context(), []byte("k"), rev); !errors.Is(err, errInjected) {
		t.Fatalf("GetAt with a read error = %v, want errInjected", err)
	}
}

func TestServiceBackendKeysPages(t *testing.T) {
	sb, _ := newServiceBackend(t)
	sb.keysPageSize = 2
	puts := map[string][]byte{"other/1": []byte("x")}
	var want []string
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("p/%d", i)
		puts[key] = []byte("v")
		want = append(want, key)
	}
	rev, err := sb.Commit(t.Context(), nil, puts, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	keys, err := sb.Keys(t.Context(), []byte("p/"), rev)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != len(want) {
		t.Fatalf("Keys = %q, want %q", keys, want)
	}
	for i, key := range keys {
		if string(key) != want[i] {
			t.Fatalf("Keys = %q, want %q", keys, want)
		}
	}
	// An exact multiple of the page size ends on an empty page.
	sb.keysPageSize = 5
	if keys, err := sb.Keys(t.Context(), []byte("p/"), rev); err != nil || len(keys) != 5 {
		t.Fatalf("Keys at a full page = %q, %v", keys, err)
	}
}

func TestServiceBackendCommitNothing(t *testing.T) {
	sb, _ := newServiceBackend(t)
	before, err := sb.Commit(t.Context(), nil, map[string][]byte{"k": []byte("v")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev, err := sb.Commit(t.Context(), nil, nil, nil)
	if err != nil || rev != before || revOf(t, sb) != before {
		t.Fatalf("empty Commit = %d, %v; backend at %d; want %d", rev, err, revOf(t, sb), before)
	}
}

func TestServiceBackendStoreCommits(t *testing.T) {
	sb, d := newServiceBackend(t)
	// A staged commit over the service publishes with MovePrefix, which
	// carries no compares, so it needs the writer lock (see
	// TestServiceBackendStagedReadSetRefused).
	s := newLockedStore(t, sb, newMemLocker(), "a")
	seed(t, s, 0x01, 0x02)
	if d.moves != 0 {
		t.Fatalf("a 2-page commit published %d times", d.moves)
	}
	old := begin(t, s)

	txn := beginReserved(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13)
	before := revOf(t, sb)
	rev := commit(t, txn)
	if d.moves != 1 {
		t.Fatalf("a 3-page commit published %d times, want 1", d.moves)
	}
	// Three staging commits of one page each, then one publish revision.
	if rev != before+4 || revOf(t, sb) != rev {
		t.Fatalf("staged commit revision %d (backend %d), want %d", rev, revOf(t, sb), before+4)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x11, 0x12, 0x13)) {
		t.Fatal("staged commit does not read back byte for byte")
	}
	if got := readAll(t, old); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a transaction pinned before the staged commit does not read the old bytes")
	}
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatalf("StageRoot: %v", err)
	}
	if keys, err := sb.Keys(t.Context(), root, rev); err != nil || len(keys) != 0 {
		t.Fatalf("stage keys after the publish = %q, %v", keys, err)
	}
}

func TestServiceBackendPublishConflict(t *testing.T) {
	sb, d := newServiceBackend(t)
	s := newLockedStore(t, sb, newMemLocker(), "a")
	seed(t, s, 0x01, 0x02)
	d.strayUnder = true
	txn := beginReserved(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13)
	if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrPublishConflict) {
		t.Fatalf("Commit with a stray staged key = %v, want ErrPublishConflict", err)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a conflicting publish changed the pages")
	}
}

// TestServiceBackendStagedReadSetRefused: without a writer lock every commit
// carries a read set, and MovePrefix cannot check one, so a staged publish is
// refused before anything is published and its stage keys are swept.
func TestServiceBackendStagedReadSetRefused(t *testing.T) {
	for name, s := range map[string]func(Backend) (*Store, error){
		"unlocked": func(b Backend) (*Store, error) {
			return NewStore(b, "app.db", WithStageThreshold(2*testPageSize), WithStageChunkBytes(testPageSize))
		},
		"optimistic": func(b Backend) (*Store, error) {
			return NewStore(b, "app.db", WithOptimistic(), WithStageThreshold(2*testPageSize), WithStageChunkBytes(testPageSize))
		},
	} {
		t.Run(name, func(t *testing.T) {
			sb, d := newServiceBackend(t)
			store, err := s(sb)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			seed(t, store, 0x01, 0x02)
			txn := begin(t, store)
			writePages(t, txn, 0x11, 0x12, 0x13)
			if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrReadCheckUnsupported) {
				t.Fatalf("staged Commit with a read set = %v, want ErrReadCheckUnsupported", err)
			}
			if d.moves != 0 {
				t.Fatalf("a refused publish moved %d times", d.moves)
			}
			if got := readAll(t, begin(t, store)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
				t.Fatal("a refused staged commit changed the pages")
			}
			root, err := StageRoot("app.db")
			if err != nil {
				t.Fatalf("StageRoot: %v", err)
			}
			if keys, err := sb.Keys(t.Context(), root, revOf(t, sb)); err != nil || len(keys) != 0 {
				t.Fatalf("stage keys after the refusal = %q, %v", keys, err)
			}
		})
	}
}

// TestOptimistic_ServiceConflict: over the service the read set travels as
// compares in the commit's own transaction, so a changed read fails it whole.
func TestOptimistic_ServiceConflict(t *testing.T) {
	sb, _ := newServiceBackend(t)
	s := newOptimisticStore(t, sb)
	seed(t, s, 0x01, 0x02)
	one, two := begin(t, s), begin(t, s)
	readPage(t, one, 1)
	writePage(t, one, 1, 0x11)
	readPage(t, two, 2)
	writePage(t, two, 2, 0x22)
	commit(t, one)
	commit(t, two)

	reader, writer := begin(t, s), begin(t, s)
	readPage(t, reader, 1)
	writePage(t, reader, 2, 0x32)
	writePage(t, writer, 1, 0x41)
	commit(t, writer)
	rev := revOf(t, sb)
	if _, err := reader.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("service Commit over a changed read = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, sb) != rev {
		t.Fatalf("a failed compare wrote: revision %d, want %d", revOf(t, sb), rev)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x41, 0x22)) {
		t.Fatal("service optimistic commits do not read back")
	}
}

func TestServiceBackendQuotaIsErrFull(t *testing.T) {
	exceeded := func() error {
		return fmt.Errorf("quota wrapper: %w", &quota.ExceededError{
			TenantID: "tenant-a",
			Resource: quota.ResourceStorageBytes,
			Current:  100,
			Limit:    64,
		})
	}
	puts := map[string][]byte{"k": []byte("v")}
	commit := func(sb *ServiceBackend, d *serviceDouble, err error) error {
		d.execErr = err
		_, got := sb.Commit(t.Context(), nil, puts, nil)
		return got
	}
	publish := func(sb *ServiceBackend, d *serviceDouble, err error) error {
		d.moveErr = err
		_, got := sb.Publish(t.Context(), []byte("src/"), []byte("dst/"), 1, nil, puts, nil)
		return got
	}
	tests := []struct {
		name         string
		call         func(*ServiceBackend, *serviceDouble, error) error
		err          error
		wantFull     bool
		wantConflict bool
	}{
		{name: "commit past quota", call: commit, err: exceeded(), wantFull: true},
		{name: "publish past quota", call: publish, err: exceeded(), wantFull: true},
		{name: "commit plain error", call: commit, err: errors.New("unavailable")},
		{name: "publish plain error", call: publish, err: errors.New("unavailable")},
		{name: "publish conflict", call: publish, err: &service.CommandConflictError{Message: "count moved"}, wantConflict: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sb, d := newServiceBackend(t)
			err := tc.call(sb, d, tc.err)
			if err == nil {
				t.Fatal("call succeeded, want an error")
			}
			if got := errors.Is(err, ErrFull); got != tc.wantFull {
				t.Fatalf("errors.Is(%v, ErrFull) = %v, want %v", err, got, tc.wantFull)
			}
			var ex *quota.ExceededError
			if got := errors.As(err, &ex); got != tc.wantFull {
				t.Fatalf("errors.As(%v, *quota.ExceededError) = %v, want %v", err, got, tc.wantFull)
			}
			if tc.wantFull && (ex.TenantID != "tenant-a" || ex.Resource != quota.ResourceStorageBytes) {
				t.Fatalf("ExceededError = %+v, want tenant-a storage bytes", ex)
			}
			if got := errors.Is(err, ErrPublishConflict); got != tc.wantConflict {
				t.Fatalf("errors.Is(%v, ErrPublishConflict) = %v, want %v", err, got, tc.wantConflict)
			}
		})
	}
}

func TestServiceBackendSweepAndBump(t *testing.T) {
	ctx := t.Context()
	sb, _ := newServiceBackend(t)
	s := newLockedStore(t, sb, newMemLocker(), "a")
	seeded := beginReserved(t, s)
	writePages(t, seeded, 0x01)
	commit(t, seeded)

	// Stage keys a crashed writer left.
	leftovers := make(map[string][]byte)
	for pgno := uint32(1); pgno <= 3; pgno++ {
		key, err := StageKey("app.db", "0123456789abcdef0123456789abcdef", pgno)
		if err != nil {
			t.Fatalf("StageKey: %v", err)
		}
		leftovers[string(key)] = pageOf(0xEE)
	}
	if _, err := sb.Commit(t.Context(), nil, leftovers, nil); err != nil {
		t.Fatalf("Commit leftovers: %v", err)
	}
	rev := revOf(t, sb)
	txn := beginReserved(t, s)
	if revOf(t, sb) != rev+1 {
		t.Fatalf("sweep wrote revisions %d..%d, want one", rev+1, revOf(t, sb))
	}
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatalf("StageRoot: %v", err)
	}
	if keys, err := sb.Keys(t.Context(), root, revOf(t, sb)); err != nil || len(keys) != 0 {
		t.Fatalf("stage keys after the sweep = %q, %v", keys, err)
	}
	writePages(t, txn, 0x21)

	// A restore underneath the open writer refuses its commit.
	bumped, err := s.BumpGeneration(t.Context())
	if err != nil {
		t.Fatalf("BumpGeneration: %v", err)
	}
	if bumped != rev+2 || revOf(t, sb) != bumped {
		t.Fatalf("BumpGeneration = %d, backend at %d, want %d", bumped, revOf(t, sb), rev+2)
	}
	if _, err := txn.Commit(ctx); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit after a restore = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, sb) != bumped {
		t.Fatalf("refused Commit wrote: revision %d, want %d", revOf(t, sb), bumped)
	}
}
