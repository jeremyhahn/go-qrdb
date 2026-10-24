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
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// leaseBackends are the backends every KVStateMachine sm_type is built on.
// Each lease test runs on all of them: the lease table lives in the backend's
// keyspace and commits through its ApplyAtomic, which every backend
// implements differently.
func leaseBackends() map[string]func() Backend {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	return map[string]func() Backend{
		"standard":   func() Backend { return NewStandardBackend() },
		"concurrent": func() Backend { return NewConcurrentBackend() },
		"highperf":   func() Backend { return NewHighPerfBackend(nil) },
		"fast":       func() Backend { return NewFastBackend(1, 1, memstore.NewStorage(), discard) },
		"disk":       func() Backend { return NewDiskBackend(memstore.NewStorage()) },
	}
}

// leaseSM is a KVStateMachine with a log index it advances per entry.
type leaseSM struct {
	t     *testing.T
	k     *KVStateMachine
	index uint64
}

func newLeaseSM(t *testing.T, b Backend, opts ...KVOption) *leaseSM {
	t.Helper()
	k := NewKVStateMachine(b, opts...)
	k.resetLessor()
	t.Cleanup(func() { _ = k.Close() })
	return &leaseSM{t: t, k: k}
}

// apply applies one command and returns its result. A Go error from Update is
// a test failure: every case here is an application-level outcome.
func (s *leaseSM) apply(cmd []byte) sm.Result {
	s.t.Helper()
	s.index++
	results := make([]sm.Result, 1)
	if err := s.k.Update([]sm.Entry{{Index: s.index, Cmd: cmd}}, results); err != nil {
		s.t.Fatalf("Update: %v", err)
	}
	return results[0]
}

func (s *leaseSM) grant(ttl int64) int64 {
	s.t.Helper()
	res := s.apply(mustEncode(protocol.EncodeLeaseGrant(ttl)))
	if len(res.Data) != 0 || res.Value == 0 {
		s.t.Fatalf("grant(%d) = %+v; want a lease id", ttl, res)
	}
	return int64(res.Value)
}

func (s *leaseSM) putLeased(key, value string, id int64) sm.Result {
	s.t.Helper()
	return s.apply(mustEncode(protocol.EncodeMVCCPut([]byte(key), []byte(value), id)))
}

func (s *leaseSM) revoke(id int64) {
	s.t.Helper()
	if res := s.apply(mustEncode(protocol.EncodeLeaseRevoke(id))); len(res.Data) != 0 {
		s.t.Fatalf("revoke(%d) refused: %q", id, res.Data)
	}
}

func (s *leaseSM) get(key string) []byte {
	s.t.Helper()
	v, err := s.k.Lookup([]byte(key))
	if err != nil {
		s.t.Fatalf("Lookup(%q): %v", key, err)
	}
	if v == nil {
		return nil
	}
	return v.([]byte)
}

// TestKVLease_GrantBindRevoke: a granted lease is held and reported; keys put
// under it are deleted when it is revoked, and a key put without it is not.
func TestKVLease_GrantBindRevoke(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			id := s.grant(30)
			if second := s.grant(30); second != id+1 {
				t.Fatalf("second grant = %d; want %d -- ids are a sequence every replica allocates alike", second, id+1)
			}
			s.putLeased("a", "1", id)
			s.putLeased("b", "2", id)
			s.putLeased("free", "3", lease.NoLease)

			ttl, err := s.k.Lookup(lease.LeaseTimeToLiveQuery{ID: id})
			if err != nil {
				t.Fatalf("TimeToLive: %v", err)
			}
			info := ttl.(*lease.Lease)
			if info.TTL != 30 || len(info.Keys) != 2 {
				t.Fatalf("lease = %+v; want TTL 30 with 2 keys", info)
			}

			s.revoke(id)
			if s.get("a") != nil || s.get("b") != nil {
				t.Fatal("a key bound to the revoked lease survived it")
			}
			if string(s.get("free")) != "3" {
				t.Fatal("revoking a lease deleted a key never bound to it")
			}
			if _, err := s.k.Lookup(lease.LeaseTimeToLiveQuery{ID: id}); err == nil {
				t.Fatal("a revoked lease is still reported")
			}
			// A replayed or racing revoke changes nothing and halts nothing.
			s.revoke(id)
		})
	}
}

// TestKVLease_KeepAlive: a keep-alive of a held lease is applied and writes
// nothing -- expiry is wall-clock state local to each replica, so the replica
// hash is unchanged -- and a keep-alive of an unknown lease is a no-op, so a
// duplicate never stalls apply. The renewal itself is pkg/lease's
// (TestKeepAliveResetsTTL); no test here waits on the clock.
func TestKVLease_KeepAlive(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			lr := lease.NewLessor()
			s := newLeaseSM(t, nb(), WithKVLessor(lr))
			id := s.grant(30)
			before, err := s.k.GetHash()
			if err != nil {
				t.Fatal(err)
			}
			res := s.apply(mustEncode(protocol.EncodeLeaseKeepAlive(id)))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("keep-alive refused: %q", res.Data)
			}
			gotID, grantedTTL, _, _, ok := protocol.DecodeLeaseInfo(res.Data)
			if !ok {
				t.Fatalf("keep-alive did not carry lease info: %q", res.Data)
			}
			if gotID != id {
				t.Fatalf("expected lease ID %d, got %d", id, gotID)
			}
			if grantedTTL != 30 {
				t.Fatalf("expected granted TTL 30, got %d", grantedTTL)
			}
			if after, _ := s.k.GetHash(); after != before {
				t.Fatal("a keep-alive changed the replicated state")
			}
			if !lr.Has(id) {
				t.Fatal("the lease is gone after its keep-alive")
			}
			res = s.apply(mustEncode(protocol.EncodeLeaseKeepAlive(id + 99)))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("keep-alive of an unknown lease refused: %q", res.Data)
			}
			if notFoundID, nf := protocol.DecodeLeaseKeepAliveNotFound(res.Data); !nf || notFoundID != id+99 {
				t.Fatalf("expected lease-keep-alive-not-found for unknown lease %d, got ok=%v id=%d", id+99, nf, notFoundID)
			}
		})
	}
}

// TestKVLease_KeepAliveQuery: the renewal query is answered by the replica's
// own Lessor -- not primary until its expiry loop runs, renewed after -- and
// changes no replicated state.
func TestKVLease_KeepAliveQuery(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			fc := clocktest.New()
			lr := lease.NewLessor(lease.WithClock(fc))
			s := newLeaseSM(t, nb(), WithKVLessor(lr))
			id := s.grant(30)
			before, err := s.k.GetHash()
			if err != nil {
				t.Fatal(err)
			}

			var np *lease.NotPrimaryError
			if _, err := s.k.Lookup(lease.LeaseKeepAliveQuery{ID: id}); !errors.As(err, &np) {
				t.Fatalf("query before RunExpiry error = %v, want *lease.NotPrimaryError", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				lr.RunExpiry(ctx, time.Hour, func() bool { return true },
					func(context.Context, int64) error { return nil }, nil)
			}()
			defer func() { cancel(); <-done }()

			// The expiry loop records itself as the primary before it arms
			// its ticker, so an armed ticker means the query may now renew.
			armCtx, armCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer armCancel()
			if err := fc.BlockUntil(armCtx, 1); err != nil {
				t.Fatalf("the expiry loop never armed its ticker: %v", err)
			}
			got, err := s.k.Lookup(&lease.LeaseKeepAliveQuery{ID: id})
			if err != nil {
				t.Fatalf("query never renewed: %v", err)
			}
			if l := got.(*lease.Lease); l.ID != id {
				t.Fatalf("lease ID = %d, want %d", l.ID, id)
			}
			if after, _ := s.k.GetHash(); after != before {
				t.Fatal("a renewal query changed the replicated state")
			}
			var nf *lease.NotFoundError
			if _, err := s.k.Lookup(lease.LeaseKeepAliveQuery{ID: id + 99}); !errors.As(err, &nf) {
				t.Fatalf("unknown lease error = %v, want *lease.NotFoundError", err)
			}
		})
	}
}

// TestKVLease_Checkpoint: a checkpoint of a held lease writes its record into
// the replicated state, a checkpoint of a lease the replica no longer holds
// writes nothing, and a replicated keep-alive deletes the record again. A
// truncated checkpoint command is an apply fault, never a silent no-op.
func TestKVLease_Checkpoint(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			lr := lease.NewLessor()
			s := newLeaseSM(t, nb(), WithKVLessor(lr))
			id := s.grant(30)
			uncheckpointed, err := s.k.GetHash()
			if err != nil {
				t.Fatal(err)
			}

			// The group's first checkpoint records the renewal marker and
			// nothing else: it was sampled before the marker existed.
			res := s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index, []lease.Checkpoint{{ID: id, RemainingTTL: 10}})))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("the first checkpoint refused: %q", res.Data)
			}
			marked, _ := s.k.GetHash()
			if marked == uncheckpointed {
				t.Fatal("the first checkpoint did not record the renewal marker")
			}

			res = s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index, []lease.Checkpoint{{ID: id + 99, RemainingTTL: 10}})))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("checkpoint of an unknown lease refused: %q", res.Data)
			}
			if after, _ := s.k.GetHash(); after != marked {
				t.Fatal("a checkpoint of a lease the replica does not hold changed the replicated state")
			}

			res = s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index, []lease.Checkpoint{{ID: id, RemainingTTL: 10}})))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("checkpoint refused: %q", res.Data)
			}
			if after, _ := s.k.GetHash(); after == marked {
				t.Fatal("a checkpoint wrote no record into the replicated state")
			}

			// A checkpoint sampled before a renewal that applied ahead of it
			// is rejected on every replica (#681).
			s.apply(mustEncode(protocol.EncodeLeaseKeepAlive(id)))
			renewed, _ := s.k.GetHash()
			s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index-1, []lease.Checkpoint{{ID: id, RemainingTTL: 5}})))
			if after, _ := s.k.GetHash(); after != renewed {
				t.Fatal("a checkpoint a renewal superseded changed the replicated state")
			}
			s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index, []lease.Checkpoint{{ID: id, RemainingTTL: 5}})))
			if after, _ := s.k.GetHash(); after == renewed {
				t.Fatal("a checkpoint sampled after the renewal wrote no record")
			}

			s.index++
			results := make([]sm.Result, 1)
			var incomplete *IncompleteDataError
			truncated := []byte{protocol.OpLeaseCheckpoint, 0x01}
			if err := s.k.Update([]sm.Entry{{Index: s.index, Cmd: truncated}}, results); !errors.As(err, &incomplete) {
				t.Fatalf("truncated checkpoint error = %v, want *IncompleteDataError", err)
			}
		})
	}
}

// kvRecordingCheckpointer is a lease.Checkpointer that records every batch the
// checkpoint loop proposes and reports it applied.
type kvRecordingCheckpointer struct {
	mu  sync.Mutex
	cps []lease.Checkpoint
}

var _ lease.Checkpointer = (*kvRecordingCheckpointer)(nil)

func (r *kvRecordingCheckpointer) CheckpointEnabled() bool { return true }

func (r *kvRecordingCheckpointer) Checkpoint(_ context.Context, _, _ uint64, cps []lease.Checkpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cps = append(r.cps, cps...)
	return nil
}

func (r *kvRecordingCheckpointer) AwaitApplied(context.Context) error { return nil }

func (r *kvRecordingCheckpointer) recorded() []lease.Checkpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.cps)
}

// TestKVLease_CheckpointNowQuery: the checkpoint-now query is refused off the
// primary and, on it, answered once the loop has checkpointed every lease.
func TestKVLease_CheckpointNowQuery(t *testing.T) {
	fc := clocktest.New()
	lr := lease.NewLessor(lease.WithClock(fc))
	s := newLeaseSM(t, NewStandardBackend(), WithKVLessor(lr))
	id := s.grant(30)

	var np *lease.NotPrimaryError
	for _, q := range []any{lease.LeaseCheckpointNowQuery{}, &lease.LeaseCheckpointNowQuery{}} {
		if _, err := s.k.Lookup(q); !errors.As(err, &np) {
			t.Fatalf("%T before RunExpiry error = %v, want *lease.NotPrimaryError", q, err)
		}
	}

	cp := &kvRecordingCheckpointer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Hour, func() bool { return true },
			func(context.Context, int64) error { return nil }, nil,
			lease.WithCheckpoint(time.Hour, cp))
	}()
	defer func() { cancel(); <-done }()

	// The loop marks itself checkpointing before it arms the expiry ticker,
	// its second, so two armed tickers mean the query is accepted.
	armCtx, armCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer armCancel()
	if err := fc.BlockUntil(armCtx, 2); err != nil {
		t.Fatalf("the expiry loop never armed its tickers: %v", err)
	}
	got, err := s.k.Lookup(&lease.LeaseCheckpointNowQuery{})
	if err != nil {
		t.Fatalf("query on the primary: %v", err)
	}
	answer, ok := got.(<-chan error)
	if !ok {
		t.Fatalf("query answered %T, want <-chan error", got)
	}
	select {
	case err := <-answer:
		if err != nil {
			t.Fatalf("checkpoint-now answered %v", err)
		}
	case <-armCtx.Done():
		t.Fatal("checkpoint-now was never answered")
	}
	if rec := cp.recorded(); len(rec) != 1 || rec[0].ID != id || rec[0].RemainingTTL != 30 {
		t.Fatalf("checkpointed %+v, want lease %d at 30s", rec, id)
	}
}

// TestKVLease_LatestWriteDecidesTheLease: a key belongs to the lease of its
// latest write. Re-put without a lease, deleted, or moved to another lease, it
// has left the first one, and revoking that lease leaves it alone. A
// compare-and-swap edits the value and keeps the lease, as on pebble.
func TestKVLease_LatestWriteDecidesTheLease(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			first, second := s.grant(30), s.grant(30)
			for _, key := range []string{"plain", "cas", "moved", "deleted"} {
				s.putLeased(key, "v", first)
			}
			s.apply(mustEncode(kv.EncodePut([]byte("plain"), []byte("v2"))))
			s.apply(mustEncode(protocol.EncodeCompareAndSwap([]byte("cas"), []byte("v"), []byte("v2"))))
			s.putLeased("moved", "v2", second)
			s.apply(mustEncode(kv.EncodeDelete([]byte("deleted"))))
			if s.k.lessor.LeaseOf([]byte("deleted")) != lease.NoLease {
				t.Fatal("a deleted key is still bound to its lease")
			}

			s.revoke(first)
			for _, key := range []string{"plain", "moved"} {
				if string(s.get(key)) != "v2" {
					t.Fatalf("%q: revoking a lease it had left deleted it", key)
				}
			}
			if s.get("cas") != nil {
				t.Fatal("a compare-and-swap moved its key out of its lease")
			}
			s.revoke(second)
			if s.get("moved") != nil {
				t.Fatal("the key's current lease did not delete it")
			}
		})
	}
}

// TestKVLease_BatchAndTransactionBind: a lease named by a batch or
// transaction put binds its key in the same commit; an unknown lease refuses
// the whole set with the lease-not-found error and writes nothing.
func TestKVLease_BatchAndTransactionBind(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			id := s.grant(30)

			op := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("batched"), []byte("v"), nil, id))
			if res := s.apply(mustEncode(protocol.EncodeBatch([][]byte{op}))); len(res.Data) != 0 {
				t.Fatalf("batch refused: %q", res.Data)
			}
			txOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn"), []byte("v"), nil, id))
			if res := s.apply(mustEncode(protocol.EncodeTransaction(nil, [][]byte{txOp}, nil))); len(res.Data) != 1 || res.Data[0] != 1 {
				t.Fatalf("transaction = %+v; want the success branch", res)
			}

			bad := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("never"), []byte("v"), nil, id+99))
			res := s.apply(mustEncode(protocol.EncodeBatch([][]byte{op, bad})))
			if _, isLNF := protocol.DecodeLeaseNotFoundError(res.Data); !isLNF {
				t.Fatalf("a batch naming an unknown lease = %+v; want lease not found", res)
			}
			if s.get("never") != nil {
				t.Fatal("a refused batch wrote a key")
			}
			if res := s.putLeased("never", "v", id+99); len(res.Data) == 0 {
				t.Fatal("a put naming an unknown lease was applied")
			}

			s.revoke(id)
			if s.get("batched") != nil || s.get("txn") != nil {
				t.Fatal("a key bound by a batch or transaction survived its lease")
			}
		})
	}
}

// TestKVLease_SnapshotRestore: a snapshot carries the lease table; a fresh
// state machine recovered from it holds the same leases and bindings, grants
// the same next id as the original, and its revoke deletes the bound keys.
func TestKVLease_SnapshotRestore(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			src := newLeaseSM(t, nb())
			id := src.grant(45)
			src.putLeased("held", "v", id)
			src.putLeased("free", "v", lease.NoLease)

			var snap bytes.Buffer
			if err := src.k.SaveSnapshot(&snap, nil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}

			lr := lease.NewLessor()
			dst := newLeaseSM(t, nb(), WithKVLessor(lr))
			if err := dst.k.RecoverSnapshot(&snap, nil); err != nil {
				t.Fatalf("RecoverSnapshot: %v", err)
			}
			got := lr.Lookup(id)
			if got == nil || got.TTL != 45 || lr.LeaseOf([]byte("held")) != id {
				t.Fatalf("recovered lessor = %+v (held bound to %d); want lease %d with its key", got, lr.LeaseOf([]byte("held")), id)
			}
			if next, want := dst.grant(10), src.grant(10); next != want {
				t.Fatalf("recovered replica grants %d, original %d -- they diverge", next, want)
			}
			dst.revoke(id)
			if dst.get("held") != nil || string(dst.get("free")) != "v" {
				t.Fatal("revoke on the recovered replica did not delete exactly the bound key")
			}
			srcHash, _ := src.k.GetHash()
			src.revoke(id)
			after, _ := src.k.GetHash()
			if srcHash == after {
				t.Fatal("the replica hash does not cover the lease table")
			}
		})
	}
}

// TestKVLease_DiskRestartRestore: the disk state machine reopens its storage
// with its leases: Open rebuilds the lessor from the table, without replaying
// the entries that granted and bound them.
func TestKVLease_DiskRestartRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	factory := newStorageFactory(StorageConfig{Root: root})

	first := newDiskAdapter(mustOpenStorage(t, factory, 1, 1))
	if _, err := first.Open(ctx, "", nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("held"), []byte("v"), 1))},
	}
	results := make([]sm.Result, len(entries))
	if err := first.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lr := lease.NewLessor()
	second := newDiskAdapter(mustOpenStorage(t, factory, 1, 1), WithKVLessor(lr))
	t.Cleanup(func() { _ = second.Close(ctx) })
	idx, err := second.Open(ctx, "", nil)
	if err != nil || idx != 2 {
		t.Fatalf("reopen = %d, %v; want applied index 2", idx, err)
	}
	if !lr.Has(1) || lr.LeaseOf([]byte("held")) != 1 {
		t.Fatal("the reopened disk state machine lost its lease")
	}
	revoke := []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseRevoke(1))}}
	if err := second.Update(ctx, revoke, make([]sm.Result, 1)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if v, _ := second.Lookup(ctx, []byte("held")); v != nil {
		t.Fatal("revoke after restart did not delete the bound key")
	}
}

// kvSupersededLog is the log of the #681 finding as entries 1..4: a 600s grant
// (lease 1), the first checkpoint, which starts the group recording renewals,
// a replicated keep-alive, and a 100s checkpoint the leader sampled before
// that keep-alive. kvUnsupersededLog ends in the same checkpoint with no
// keep-alive ahead of it.
func kvSupersededLog() []sm.Entry {
	return []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(600))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeLeaseCheckpoint(0, 1, []lease.Checkpoint{{ID: 1, RemainingTTL: 600}}))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseKeepAlive(1))},
		{Index: 4, Cmd: mustEncode(protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}}))},
	}
}

func kvUnsupersededLog() []sm.Entry {
	log := kvSupersededLog()[:2]
	return append(log, sm.Entry{Index: 3, Cmd: mustEncode(protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}}))})
}

// applyLog applies entries to s one at a time, keeping its index in step.
func (s *leaseSM) applyLog(entries []sm.Entry) {
	s.t.Helper()
	for _, e := range entries {
		s.index = e.Index - 1
		s.apply(e.Cmd)
	}
}

// A checkpoint sampled before a replicated keep-alive that applied ahead of it
// is rejected, so the keep-alive's TTL stands on the replica that applied it,
// on one recovered from a snapshot of it, and on one recovered from a snapshot
// taken before the checkpoint that then applies it (#681).
func TestKVLease_ARenewalSupersedesAnEarlierCheckpoint(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			lr := lease.NewLessor()
			s := newLeaseSM(t, nb(), WithKVLessor(lr))
			s.applyLog(kvSupersededLog())
			if got := lr.Lookup(1).RemainingTTL; got < 598 {
				t.Fatalf("remaining after the superseded checkpoint = %d, want 600", got)
			}

			var after bytes.Buffer
			if err := s.k.SaveSnapshot(&after, nil); err != nil {
				t.Fatal(err)
			}
			afterLR := lease.NewLessor()
			if err := newLeaseSM(t, nb(), WithKVLessor(afterLR)).k.RecoverSnapshot(&after, nil); err != nil {
				t.Fatal(err)
			}
			if got := afterLR.Lookup(1).RemainingTTL; got < 598 {
				t.Fatalf("recovered remaining = %d, want the renewal's 600", got)
			}

			before := newLeaseSM(t, nb())
			before.applyLog(kvSupersededLog()[:3])
			var snap bytes.Buffer
			if err := before.k.SaveSnapshot(&snap, nil); err != nil {
				t.Fatal(err)
			}
			replayLR := lease.NewLessor()
			replayed := newLeaseSM(t, nb(), WithKVLessor(replayLR))
			if err := replayed.k.RecoverSnapshot(&snap, nil); err != nil {
				t.Fatal(err)
			}
			replayed.applyLog(kvSupersededLog()[3:])
			if got := replayLR.Lookup(1).RemainingTTL; got < 598 {
				t.Fatalf("replayed remaining = %d, want the renewal's 600", got)
			}
			if a, b := mustHash(t, s.k), mustHash(t, replayed.k); a != b {
				t.Fatal("the replica that replayed from the snapshot diverged")
			}
		})
	}
}

// A disk replica that restarts after the superseded checkpoint counts from the
// renewal; one whose checkpoint no renewal superseded counts from it.
func TestKVLease_DiskRestartJudgesTheCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		log      []sm.Entry
		min, max int64
	}{
		{"superseded", kvSupersededLog(), 598, 600},
		{"unsuperseded", kvUnsupersededLog(), 98, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			factory := newStorageFactory(StorageConfig{Root: t.TempDir()})
			first := newDiskAdapter(mustOpenStorage(t, factory, 1, 1), WithKVLessor(lease.NewLessor()))
			if _, err := first.Open(ctx, "", nil); err != nil {
				t.Fatal(err)
			}
			if err := first.Update(ctx, tc.log, make([]sm.Result, len(tc.log))); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(ctx); err != nil {
				t.Fatal(err)
			}

			lr := lease.NewLessor()
			second := newDiskAdapter(mustOpenStorage(t, factory, 1, 1), WithKVLessor(lr))
			t.Cleanup(func() { _ = second.Close(ctx) })
			if _, err := second.Open(ctx, "", nil); err != nil {
				t.Fatal(err)
			}
			if got := lr.Lookup(1).RemainingTTL; got < tc.min || got > tc.max {
				t.Fatalf("restarted remaining = %d, want %d..%d", got, tc.min, tc.max)
			}
		})
	}
}

// Until a group applies its first checkpoint -- with the lease-checkpoint gate
// off it never does -- a keep-alive writes nothing to the replicated state,
// exactly as before the rule; once one has, it records the renewal.
func TestKVLease_RenewalsAreRecordedOnlyOnceCheckpointing(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb(), WithKVLessor(lease.NewLessor()))
			id := s.grant(30)
			granted := mustHash(t, s.k)
			s.apply(mustEncode(protocol.EncodeLeaseKeepAlive(id)))
			if mustHash(t, s.k) != granted {
				t.Fatal("a keep-alive before any checkpoint changed the replicated state")
			}

			s.apply(mustEncode(protocol.EncodeLeaseCheckpoint(0, s.index, []lease.Checkpoint{{ID: id, RemainingTTL: 30}})))
			marked := mustHash(t, s.k)
			s.apply(mustEncode(protocol.EncodeLeaseKeepAlive(id)))
			if mustHash(t, s.k) == marked {
				t.Fatal("a keep-alive after the first checkpoint recorded no renewal")
			}
		})
	}
}

func mustHash(t *testing.T, k *KVStateMachine) uint64 {
	t.Helper()
	h, err := k.GetHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestKVLease_CorruptTableFailsOpen: a lease table that does not validate is
// refused at open rather than guessed past.
func TestKVLease_CorruptTableFailsOpen(t *testing.T) {
	store := memstore.NewStorage()
	if err := store.Set([]byte(lease.TablePrefix+"seq"), []byte("short")); err != nil {
		t.Fatal(err)
	}
	a := newDiskAdapter(store)
	_, err := a.Open(context.Background(), "", nil)
	var corrupt *lease.CorruptTableError
	if !errors.As(err, &corrupt) {
		t.Fatalf("Open over a corrupt lease table = %v; want *lease.CorruptTableError", err)
	}
}

// TestKVLease_NewStateMachineResetsTheLessor: an in-memory state machine
// starts empty and Raft rebuilds it, so the lessor it is given starts empty
// too -- a lessor reused across a restart in one process must not keep
// leases the log is about to replay.
func TestKVLease_NewStateMachineResetsTheLessor(t *testing.T) {
	lr := lease.NewLessor()
	if _, err := lr.Grant(mutationWriter{muts: new([]Mutation)}, 30); err != nil {
		t.Fatal(err)
	}
	for name, create := range map[string]func() interface{}{
		"standard":   func() interface{} { return NewKVStandardCreateFunc(WithKVLessor(lr))(1, 1) },
		"concurrent": func() interface{} { return NewKVConcurrentCreateFunc(WithKVLessor(lr))(1, 1) },
		"highperf":   func() interface{} { return NewKVHighPerfCreateFunc(nil, WithKVLessor(lr))(1, 1) },
		"fast": func() interface{} {
			return NewKVFastCreateFunc(func(_, _ uint64) (storage.Storage, error) { return memstore.NewStorage(), nil }, WithKVLessor(lr))(1, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lr.Grant(mutationWriter{muts: new([]Mutation)}, 30); err != nil {
				t.Fatal(err)
			}
			_ = create()
			if lr.Count() != 0 {
				t.Fatalf("a new state machine kept %d leases from before it", lr.Count())
			}
		})
	}
}

// TestKVLease_TableIsTheStateMachines: the lease table is neither writable by
// a client nor returned to one.
func TestKVLease_TableIsTheStateMachines(t *testing.T) {
	s := newLeaseSM(t, NewStandardBackend())
	id := s.grant(30)
	s.putLeased("user", "v", id)

	forged := mustEncode(kv.EncodePut([]byte(lease.TablePrefix+"seq"), []byte("x")))
	if res := s.apply(forged); len(res.Data) == 0 {
		t.Fatal("a client write to the lease table was applied")
	}
	if s.get(lease.TablePrefix+"seq") != nil {
		t.Fatal("a point read returned a lease-table record")
	}
	keys, err := s.k.Lookup(kv.ListQuery{Prefix: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys.([]string); len(got) != 1 || got[0] != "user" {
		t.Fatalf("List(\"\") = %q; want only the client key", got)
	}
	scan, err := s.k.Lookup(&PrefixQuery{Prefix: nil, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if m := scan.(map[string][]byte); len(m) != 1 || m["user"] == nil {
		t.Fatalf("Scan(limit 1) = %v; want the client key, not a lease record", m)
	}
	leases, err := s.k.Lookup(lease.LeasesQuery{})
	if err != nil || len(leases.([]*lease.Lease)) != 1 {
		t.Fatalf("Leases = %v, %v; want the one lease", leases, err)
	}
}

// TestKVLease_InvalidTTLIsARefusal: a non-positive TTL is refused alike on
// every replica, as a result, never a Go error.
func TestKVLease_InvalidTTLIsARefusal(t *testing.T) {
	s := newLeaseSM(t, NewStandardBackend())
	if res := s.apply(mustEncode(protocol.EncodeLeaseGrant(0))); len(res.Data) == 0 {
		t.Fatalf("grant(0) = %+v; want a refusal", res)
	}
	if s.k.lessor.Count() != 0 {
		t.Fatal("a refused grant created a lease")
	}
}

// TestKVLease_RegistryUsesTheInjectedLessor: every KVStateMachine sm_type
// resolved with WithKVLessor keeps its leases in that lessor, the one the
// composition root's expiry sweeper watches; an sm_type that is not a
// KVStateMachine refuses the option rather than ignoring it.
func TestKVLease_RegistryUsesTheInjectedLessor(t *testing.T) {
	for _, name := range []string{"memory", "standard", "concurrent", "highperf", "fast", "disk"} {
		t.Run(name, func(t *testing.T) {
			lr := lease.NewLessor()
			sc := StorageConfig{}
			if desc, _ := Describe(name); desc.StorageGeneric {
				sc = StorageConfig{Root: t.TempDir(), Kind: "memory"}
			}
			createFn, err := ResolveTypedWithStorage(name, "", sc, WithKVLessor(lr))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			machine := createFn(1, 1)
			if o, ok := machine.(interface {
				Open(context.Context, string, <-chan struct{}) (uint64, error)
			}); ok {
				if _, err := o.Open(context.Background(), "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if !applyProbe(t, machine, mustEncode(protocol.EncodeLeaseGrant(30))) || !lr.Has(1) {
				t.Fatal("the grant did not land in the injected lessor")
			}
		})
	}
	_, err := ResolveTypedWithStorage("regular", "", StorageConfig{}, WithKVLessor(lease.NewLessor()))
	var notKV *KVOptionsNotApplicableError
	if !errors.As(err, &notKV) || notKV.Error() == "" {
		t.Fatalf("regular with a lessor = %v; want *KVOptionsNotApplicableError", err)
	}
}

// TestKVLease_ReplayIsDeterministic: two replicas applying the same log hold
// the same leases, bindings and hash.
func TestKVLease_ReplayIsDeterministic(t *testing.T) {
	log := [][]byte{
		mustEncode(protocol.EncodeLeaseGrant(30)),
		mustEncode(protocol.EncodeLeaseGrant(60)),
		mustEncode(protocol.EncodeMVCCPut([]byte("a"), []byte("1"), 1)),
		mustEncode(protocol.EncodeMVCCPut([]byte("b"), []byte("2"), 2)),
		mustEncode(protocol.EncodeLeaseRevoke(1)),
	}
	hashes := make([]uint64, 2)
	for i := range hashes {
		s := newLeaseSM(t, NewConcurrentBackend())
		for _, cmd := range log {
			s.apply(cmd)
		}
		h, err := s.k.GetHash()
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = h
		ids := []int64{}
		for _, l := range s.k.lessor.Leases() {
			ids = append(ids, l.ID)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		if len(ids) != 1 || ids[0] != 2 {
			t.Fatalf("replica %d holds leases %v; want [2]", i, ids)
		}
	}
	if hashes[0] != hashes[1] {
		t.Fatal("two replicas of one log hash differently")
	}
}
