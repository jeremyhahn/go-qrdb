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

package store

// A key belongs to the lease of its LATEST write (go-qrdb#581). These tests
// drive both lease-capable state machines -- the Pebble one (sm_type: pebble)
// and the MVCC one (sm_type: mvcc) -- through committed commands only, the
// way Raft drives them, and assert on the stored value rather than on the
// lessor: what matters is whether a revoke deleted data it should not have.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// leaseSM is what these tests need from a lease-capable state machine.
type leaseSM interface {
	sm.DiskStateMachine
	GetHash() (uint64, error)
}

// leaseSMKind builds one kind of lease-capable state machine, unopened, with
// lr attached.
type leaseSMKind struct {
	name  string
	build func(lr *lease.Lessor) leaseSM
}

var leaseSMKinds = []leaseSMKind{
	{"pebble", func(lr *lease.Lessor) leaseSM {
		s := NewPebbleStateMachine(nil)
		s.SetLessor(lr)
		return s
	}},
	{"mvcc", func(lr *lease.Lessor) leaseSM {
		s := NewMVCCStateMachine(nil)
		s.SetLessor(lr)
		return s
	}},
}

// open builds kind over dir with lr attached and opens it.
func (k leaseSMKind) open(t *testing.T, dir string, lr *lease.Lessor) leaseSM {
	t.Helper()
	s := k.build(lr)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// leaseHarness is one state machine under test plus the log position the
// next applied command takes.
type leaseHarness struct {
	t     *testing.T
	kind  leaseSMKind
	dir   string
	lr    *lease.Lessor
	sm    leaseSM
	index uint64
}

// newLeaseHarness opens kind in a fresh directory.
func newLeaseHarness(t *testing.T, kind leaseSMKind) *leaseHarness {
	t.Helper()
	h := &leaseHarness{t: t, kind: kind, dir: filepath.Join(t.TempDir(), "db"), lr: lease.NewLessor()}
	h.sm = kind.open(t, h.dir, h.lr)
	t.Cleanup(func() { h.close() })
	return h
}

// close closes the state machine if it is open.
func (h *leaseHarness) close() {
	if h.sm == nil {
		return
	}
	if err := h.sm.Close(context.Background()); err != nil {
		h.t.Errorf("Close: %v", err)
	}
	h.sm = nil
}

// restart closes the state machine and reopens the same directory with a
// brand-new lessor, as a node process restart does.
func (h *leaseHarness) restart() {
	h.t.Helper()
	h.close()
	h.lr = lease.NewLessor()
	h.sm = h.kind.open(h.t, h.dir, h.lr)
}

// apply commits one command and returns its result.
func (h *leaseHarness) apply(cmd []byte, err error) sm.Result {
	h.t.Helper()
	if err != nil {
		h.t.Fatalf("encode: %v", err)
	}
	h.index++
	results := make([]sm.Result, 1)
	if uerr := h.sm.Update(context.Background(), []sm.Entry{{Index: h.index, Cmd: cmd}}, results); uerr != nil {
		h.t.Fatalf("Update: %v", uerr)
	}
	return results[0]
}

// applyOK commits one command and fails the test if it was refused.
func (h *leaseHarness) applyOK(cmd []byte, err error) sm.Result {
	h.t.Helper()
	res := h.apply(cmd, err)
	if msg, refused := protocol.DecodeAppError(res.Data); refused {
		h.t.Fatalf("command refused: %s", msg)
	}
	return res
}

// grant grants a lease of ttl seconds through the log.
func (h *leaseHarness) grant(ttl int64) int64 {
	h.t.Helper()
	return int64(h.applyOK(protocol.EncodeLeaseGrant(ttl)).Value)
}

// revoke revokes a lease through the log.
func (h *leaseHarness) revoke(id int64) {
	h.t.Helper()
	h.applyOK(protocol.EncodeLeaseRevoke(id))
}

// put writes key = value under leaseID (0 for none).
func (h *leaseHarness) put(key, value string, leaseID int64) {
	h.t.Helper()
	h.applyOK(protocol.EncodeMVCCPut([]byte(key), []byte(value), leaseID))
}

// value reads key; ok is false when the key is absent. The Pebble state
// machine answers an absent key with an error and the MVCC one with a nil
// value; both mean absent here.
func (h *leaseHarness) value(key string) (string, bool) {
	h.t.Helper()
	v, err := h.sm.Lookup(context.Background(), []byte(key))
	if err != nil || v == nil {
		return "", false
	}
	b, isBytes := v.([]byte)
	if !isBytes {
		h.t.Fatalf("Lookup(%q) returned %T, want []byte", key, v)
	}
	return string(b), true
}

// mustHold fails the test unless key holds want.
func (h *leaseHarness) mustHold(key, want, why string) {
	h.t.Helper()
	got, ok := h.value(key)
	if !ok {
		h.t.Fatalf("%q was deleted; want %q: %s", key, want, why)
	}
	if got != want {
		h.t.Fatalf("%q = %q, want %q: %s", key, got, want, why)
	}
}

// mustBeAbsent fails the test if key is present.
func (h *leaseHarness) mustBeAbsent(key, why string) {
	h.t.Helper()
	if got, ok := h.value(key); ok {
		h.t.Fatalf("%q = %q, want it deleted: %s", key, got, why)
	}
}

// forEachLeaseSM runs fn as a subtest against every lease-capable state
// machine.
func forEachLeaseSM(t *testing.T, fn func(t *testing.T, h *leaseHarness)) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			fn(t, newLeaseHarness(t, kind))
		})
	}
}

func TestLeaseBinding_RePutUnderAnotherLease_RevokeOldKeepsNewValue(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a, b := h.grant(60), h.grant(60)
		h.put("svc/instance-1", "old", a)
		h.put("svc/instance-1", "new", b)

		h.revoke(a)
		h.mustHold("svc/instance-1", "new", "the key was re-put under another lease, so revoking the old one must not delete it")

		h.revoke(b)
		h.mustBeAbsent("svc/instance-1", "revoking the lease the latest value names deletes the key")
	})
}

func TestLeaseBinding_RePutWithNoLease_RevokeOldKeepsNewValue(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("config/flag", "leased", a)
		h.put("config/flag", "permanent", 0)

		h.revoke(a)
		h.mustHold("config/flag", "permanent", "the key was re-put without a lease, so it no longer belongs to any")
	})
}

func TestLeaseBinding_BaseKVPutReleasesLease(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("k", "leased", a)
		h.applyOK(kv.EncodePut([]byte("k"), []byte("plain")))

		h.revoke(a)
		h.mustHold("k", "plain", "a base KV put carries no lease and releases the key")
	})
}

func TestLeaseBinding_CompareAndSwapKeepsLease(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("k", "v1", a)
		res := h.applyOK(protocol.EncodeCompareAndSwap([]byte("k"), []byte("v1"), []byte("v2")))
		if res.Value != protocol.ResultSwapped {
			t.Fatalf("CompareAndSwap result = %d, want swapped", res.Value)
		}
		h.mustHold("k", "v2", "the swap applied")

		h.revoke(a)
		h.mustBeAbsent("k", "a swap edits the value and keeps the key's lease, so revoking it deletes the key")
	})
}

func TestLeaseBinding_DeleteThenPlainRecreate_RevokeKeepsIt(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("k", "leased", a)
		h.applyOK(protocol.EncodeMVCCDelete([]byte("k")))
		h.applyOK(protocol.EncodeMVCCPutIfAbsent([]byte("k"), []byte("recreated"), 0))

		h.revoke(a)
		h.mustHold("k", "recreated", "a delete releases the key; the recreated key never belonged to the lease")
	})
}

func TestLeaseBinding_TransactionPutMovesKey(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a, b := h.grant(60), h.grant(60)
		h.put("k", "old", a)

		cmp, err := protocol.EncodeCompare([]byte("k"), byte(service.CompareValue), 0, []byte("old"), 0, 0, 0)
		if err != nil {
			t.Fatalf("EncodeCompare: %v", err)
		}
		putB, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("new"), nil, b)
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		res := h.applyOK(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{putB}, nil))
		if len(res.Data) != 1 || res.Data[0] != 1 {
			t.Fatalf("transaction did not take its success branch: %v", res.Data)
		}

		h.revoke(a)
		h.mustHold("k", "new", "the transaction re-put the key under another lease")
		h.revoke(b)
		h.mustBeAbsent("k", "the transaction bound the key to its new lease")
	})
}

func TestLeaseBinding_BatchPutWithoutLeaseAndDelete(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("kept", "leased", a)
		h.put("gone", "leased", a)

		plain, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("kept"), []byte("plain"), nil, 0)
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		del, err := protocol.EncodeOp(protocol.OpMVCCDelete, []byte("gone"), nil, nil, 0)
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		h.applyOK(protocol.EncodeBatch([][]byte{plain, del}))
		h.applyOK(protocol.EncodeMVCCPutIfAbsent([]byte("gone"), []byte("recreated"), 0))

		h.revoke(a)
		h.mustHold("kept", "plain", "the batch re-put the key without a lease")
		h.mustHold("gone", "recreated", "the batch deleted the key, releasing it from the lease")
	})
}

func TestLeaseBinding_PutNamingUnknownLeaseIsRefusedAndWritesNothing(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.revoke(a)

		res := h.apply(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), a))
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Fatal("a put naming a revoked lease was accepted; it would write a key no lease will ever delete")
		}
		h.mustBeAbsent("k", "a refused put writes nothing")

		good, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("k1"), []byte("v"), nil, 0)
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		bad, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("k2"), []byte("v"), nil, a)
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		res = h.apply(protocol.EncodeBatch([][]byte{good, bad}))
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Fatal("a batch naming a revoked lease was accepted")
		}
		h.mustBeAbsent("k1", "a refused batch stages none of its operations")
	})
}

func TestLeaseBinding_SurvivesRestart(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a, b := h.grant(60), h.grant(60)
		h.put("moved", "old", a)
		h.put("moved", "new", b)
		h.put("stays-with-a", "v", a)

		h.restart()

		h.revoke(a)
		h.mustHold("moved", "new", "the re-put's binding survived the restart")
		h.mustBeAbsent("stays-with-a", "lease A and its key survived the restart, so revoking it deletes the key")

		next := h.grant(60)
		if next != b+1 {
			t.Errorf("first grant after restart = %d, want %d: the ID sequence must survive the restart", next, b+1)
		}

		h.revoke(b)
		h.mustBeAbsent("moved", "lease B survived the restart, so revoking it deletes its key")
	})
}

func TestLeaseBinding_SurvivesSnapshotRecovery(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, src *leaseHarness) {
		a, b := src.grant(60), src.grant(60)
		src.put("moved", "old", a)
		src.put("moved", "new", b)
		src.put("stays-with-a", "v", a)

		snapCtx, err := src.sm.PrepareSnapshot()
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		var buf bytes.Buffer
		if err := src.sm.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}

		dst := newLeaseHarness(t, src.kind)
		// A replica that already held an unrelated lease: recovery must
		// replace the lessor's state, not merge into it.
		dst.grant(5)
		dst.put("dst-only", "v", 1)
		if err := dst.sm.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
			t.Fatalf("RecoverFromSnapshot: %v", err)
		}
		dst.index = src.index

		srcHash, err := src.sm.GetHash()
		if err != nil {
			t.Fatalf("source GetHash: %v", err)
		}
		dstHash, err := dst.sm.GetHash()
		if err != nil {
			t.Fatalf("recovered GetHash: %v", err)
		}
		if srcHash != dstHash {
			t.Fatalf("recovered hash %x != source hash %x", dstHash, srcHash)
		}
		if got := dst.lr.Lookup(1); got == nil || got.TTL != 60 {
			t.Fatalf("lease 1 after recovery = %+v, want the source's 60s lease, not the replica's own", got)
		}

		dst.revoke(a)
		dst.mustHold("moved", "new", "the snapshot carried the re-put's binding")
		dst.mustBeAbsent("stays-with-a", "the snapshot carried lease A and its key")
		dst.revoke(b)
		dst.mustBeAbsent("moved", "the snapshot carried lease B")
	})
}

// The expiry sweeper reaches the state machine through the same revoke
// command a client's revoke does, so expiry must leave a re-put key alone
// exactly as revoke does. This drives the real sweeper over a 1s lease.
func TestLeaseBinding_ExpiryOfOldLeaseKeepsNewValue(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		short, long := h.grant(1), h.grant(3600)
		h.put("svc/instance-1", "old", short)
		h.put("svc/instance-1", "new", long)

		revoked := make(chan int64, 1)
		ctx, cancel := context.WithCancel(context.Background())
		swept := make(chan struct{})
		go func() {
			defer close(swept)
			h.lr.RunExpiry(ctx, 20*time.Millisecond, func() bool { return true },
				func(_ context.Context, id int64) error {
					// The sweeper proposes; the test is the log, so hand the
					// proposal to the test goroutine to apply in order.
					select {
					case revoked <- id:
					case <-ctx.Done():
					}
					return nil
				}, nil)
		}()

		var id int64
		select {
		case id = <-revoked:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("the expiry sweeper never revoked the 1s lease")
		}
		cancel()
		<-swept
		if id != short {
			t.Fatalf("sweeper revoked lease %d, want the expired lease %d", id, short)
		}

		h.revoke(id)
		h.mustHold("svc/instance-1", "new", "the expired lease no longer holds the re-put key")
	})
}

// grantLease grants a lease of ttl seconds by applying a grant command at
// index, and returns its ID.
func grantLease(t *testing.T, s interface {
	Update(context.Context, []sm.Entry, []sm.Result) error
}, index uint64, ttl int64) int64 {
	t.Helper()
	cmd, err := protocol.EncodeLeaseGrant(ttl)
	if err != nil {
		t.Fatalf("EncodeLeaseGrant: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: index, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if msg, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("grant refused: %s", msg)
	}
	return int64(results[0].Value)
}

// The Pebble state machine stores raw values, so the lease a key belonged to
// is known only from its binding. Watchers asking for the previous value must
// still see which lease that value was under -- on a move, on a delete, and
// on a revoke.
func TestPebbleLeaseBinding_WatchPrevKVCarriesLease(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	if _, err := s.Open(context.Background(), filepath.Join(t.TempDir(), "db"), nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	h := &leaseHarness{t: t, lr: lr, sm: s}

	a, b := h.grant(60), h.grant(60)
	h.put("k", "v1", a)
	h.put("other", "v", b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "k", watch.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil {
			t.Errorf("watch Close: %v", cerr)
		}
	}()

	next := func() *watch.Event {
		t.Helper()
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 {
				t.Fatalf("got %d events, want 1", len(resp.Events))
			}
			return resp.Events[0]
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a watch event")
		}
		return nil
	}

	h.put("k", "v2", b)
	if ev := next(); ev.PrevKv == nil || ev.PrevKv.LeaseID != a || ev.Kv.LeaseID != b {
		t.Fatalf("move event prev=%+v kv=%+v, want prev lease %d and new lease %d", ev.PrevKv, ev.Kv, a, b)
	}

	h.revoke(b)
	if ev := next(); ev.Type != watch.EventDelete || ev.PrevKv == nil || ev.PrevKv.LeaseID != b {
		t.Fatalf("revoke event = %+v prev=%+v, want a delete whose previous value was under lease %d", ev, ev.PrevKv, b)
	}
	h.mustBeAbsent("other", "revoking B deletes every key bound to it")
}

// corruptLeaseSequence overwrites the persisted lease-ID sequence in the
// closed database at dir with a value that does not decode.
func corruptLeaseSequence(t *testing.T, dir string) {
	t.Helper()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	if err := db.Set([]byte(lease.TablePrefix+"seq"), []byte{1}, pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A database whose lease table does not decode refuses to open. Opening
// anyway would mean guessing which keys each lease deletes.
func TestLeaseBinding_CorruptTableRefusesOpen(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)
		h.put("k", "v", a)
		h.close()
		corruptLeaseSequence(t, h.dir)

		s := h.kind.build(lease.NewLessor())
		_, err := s.Open(context.Background(), h.dir, nil)
		var cte *lease.CorruptTableError
		if !errors.As(err, &cte) {
			if err == nil {
				if cerr := s.Close(context.Background()); cerr != nil {
					t.Errorf("Close: %v", cerr)
				}
			}
			t.Fatalf("Open over a corrupt lease table error = %v, want *lease.CorruptTableError", err)
		}
	})
}

// A snapshot whose lease table does not decode fails recovery and leaves the
// lessor as it was.
func TestLeaseBinding_CorruptTableRefusesSnapshotRecovery(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		a := h.grant(60)

		var buf bytes.Buffer
		if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
			t.Fatalf("write count: %v", err)
		}
		if err := writeSnapshotEntry(&buf, []byte(lease.TablePrefix+"seq"), []byte{1}); err != nil {
			t.Fatalf("writeSnapshotEntry: %v", err)
		}

		err := h.sm.RecoverFromSnapshot(context.Background(), &buf, nil)
		var cte *lease.CorruptTableError
		if !errors.As(err, &cte) {
			t.Fatalf("RecoverFromSnapshot of a corrupt lease table error = %v, want *lease.CorruptTableError", err)
		}
		if !h.lr.Has(a) {
			t.Fatal("a refused recovery changed the lessor")
		}
	})
}

// TestLeaseBinding_GrantInvalidTTLRefused proves a lease grant naming a
// non-positive TTL is refused as a deterministic application error -- the
// caller's own request is wrong, not a replica fault -- on every
// lease-capable state machine, matching lease.Lessor.Grant's own contract.
func TestLeaseBinding_GrantInvalidTTLRefused(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		res := h.apply(protocol.EncodeLeaseGrant(0))
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Fatalf("Grant(ttl=0) was not refused (result=%q)", res.Data)
		}

		res = h.apply(protocol.EncodeLeaseGrant(-5))
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Fatalf("Grant(ttl=-5) was not refused (result=%q)", res.Data)
		}
	})
}

// TestLeaseBinding_KeepAliveUnknownLeaseIsIdempotentSuccess proves a
// KeepAlive naming a lease the lessor does not (or no longer) hold is a
// no-op success, not an error, so Raft Apply never stalls on a duplicate or
// late-replayed keepalive proposal -- on every lease-capable state machine.
func TestLeaseBinding_KeepAliveUnknownLeaseIsIdempotentSuccess(t *testing.T) {
	forEachLeaseSM(t, func(t *testing.T, h *leaseHarness) {
		res := h.applyOK(protocol.EncodeLeaseKeepAlive(999999))
		if res.Value != 0 {
			t.Fatalf("KeepAlive(unknown lease).Value = %d, want 0", res.Value)
		}
	})
}
