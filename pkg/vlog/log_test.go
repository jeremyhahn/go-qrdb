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

package vlog

import (
	"crypto/sha256"
	"errors"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// fixedRecord returns an Encoder with the audit instance's shape: the body is
// fixed by n alone, and the digest folds the prior head digest and the body
// with SHA-256. Two independent Appends given the same sequence of these are
// expected to produce byte-identical chains -- the property
// docs/audit/architecture.md calls "assigned at apply time."
func fixedRecord(n int) Encoder {
	return func(prev [32]byte, _ bool) ([]byte, [32]byte, error) {
		body := []byte("record-" + strconv.Itoa(n))
		folded := make([]byte, 0, len(prev)+len(body))
		folded = append(folded, prev[:]...)
		folded = append(folded, body...)
		return body, sha256.Sum256(folded), nil
	}
}

// countEvents returns how many of tenantID's event records r holds.
func countEvents(t *testing.T, r storage.Reader, tenantID string) int {
	t.Helper()
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: EventRangeStart(tenantID, 0),
		UpperBound: EventRangeEnd(tenantID),
	})
	defer func() { _ = it.Close() }()
	n := 0
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		n++
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate %s's events: %v", tenantID, err)
	}
	return n
}

// TestChain_AppendIsDeterministicAcrossReplicas is the core correctness
// property: two independently constructed Chains, fed the identical sequence
// of commands (the way two Raft replicas apply the identical committed log),
// must compute byte-identical chains. Neither Chain ever reads from the
// other; the only shared input is the sequence of fixedRecord values, which is
// exactly what a committed Raft entry supplies in production.
func TestChain_AppendIsDeterministicAcrossReplicas(t *testing.T) {
	const tenantID = "acme"
	replicaA := NewChain()
	replicaB := NewChain()
	storeA := memory.NewStorage()
	storeB := memory.NewStorage()

	for i := 0; i < 5; i++ {
		seqA, err := replicaA.Append(storeA, tenantID, fixedRecord(i))
		if err != nil {
			t.Fatalf("replica A append %d: %v", i, err)
		}
		seqB, err := replicaB.Append(storeB, tenantID, fixedRecord(i))
		if err != nil {
			t.Fatalf("replica B append %d: %v", i, err)
		}
		if seqA != seqB {
			t.Fatalf("event %d: sequence diverged: A=%d B=%d", i, seqA, seqB)
		}
	}

	for i := uint64(1); i <= 5; i++ {
		va, err := storeA.Get(EventKey(tenantID, i))
		if err != nil {
			t.Fatalf("read A seq %d: %v", i, err)
		}
		vb, err := storeB.Get(EventKey(tenantID, i))
		if err != nil {
			t.Fatalf("read B seq %d: %v", i, err)
		}
		if string(va) != string(vb) {
			t.Fatalf("seq %d: replicas disagree on the stored record:\nA=%s\nB=%s", i, va, vb)
		}
	}

	ha, err := storeA.Get(headKey(tenantID))
	if err != nil {
		t.Fatalf("read A head: %v", err)
	}
	hb, err := storeB.Get(headKey(tenantID))
	if err != nil {
		t.Fatalf("read B head: %v", err)
	}
	if string(ha) != string(hb) {
		t.Fatal("replicas disagree on the final head record")
	}
}

// TestChain_Append_HandsTheEncoderThePriorHead asserts the chain is actually
// linked, not merely sequential: each Append's Encoder receives the digest the
// PRIOR append produced, and the first receives the zero digest with genesis
// set.
func TestChain_Append_HandsTheEncoderThePriorHead(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	var want [32]byte
	for i := 0; i < 4; i++ {
		var gotPrev [32]byte
		var gotGenesis bool
		encode := fixedRecord(i)
		seq, err := c.Append(store, tenantID, func(prev [32]byte, genesis bool) ([]byte, [32]byte, error) {
			gotPrev, gotGenesis = prev, genesis
			return encode(prev, genesis)
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if gotGenesis != (i == 0) {
			t.Fatalf("append %d: genesis = %v, want %v", i, gotGenesis, i == 0)
		}
		if gotPrev != want {
			t.Fatalf("append %d: prev = %x, want the prior head %x", i, gotPrev, want)
		}
		headSeq, _, digest := c.Head(tenantID)
		if headSeq != seq {
			t.Fatalf("append %d: head seq = %d, want %d", i, headSeq, seq)
		}
		want = digest
	}
}

// TestChain_Append_EncoderErrorStagesNothing asserts an Encoder's error is
// returned unchanged and leaves both the table and the in-memory head as they
// were, so the next Append reuses the sequence the failed one never took.
func TestChain_Append_EncoderErrorStagesNothing(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()
	if _, err := c.Append(store, tenantID, fixedRecord(0)); err != nil {
		t.Fatalf("append: %v", err)
	}

	refused := errors.New("encoder refused")
	_, err := c.Append(store, tenantID, func([32]byte, bool) ([]byte, [32]byte, error) {
		return nil, [32]byte{}, refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the encoder's own error", err)
	}
	if v, err := store.Get(EventKey(tenantID, 2)); err != nil || v != nil {
		t.Fatalf("a refused Append staged an event record: (%q, %v)", v, err)
	}

	seq, err := c.Append(store, tenantID, fixedRecord(1))
	if err != nil {
		t.Fatalf("append after refusal: %v", err)
	}
	if seq != 2 {
		t.Fatalf("sequence after a refused Append = %d, want 2", seq)
	}
}

// TestChain_Restore rebuilds a fresh Chain from storage and asserts the next
// Append continues the same sequence and the same digest a single
// uninterrupted Chain would have produced -- the property a node restart or a
// snapshot install depends on.
func TestChain_Restore(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()

	reference := NewChain()
	var referenceDigestAfter5 [32]byte
	for i := 0; i < 5; i++ {
		if _, err := reference.Append(store, tenantID, fixedRecord(i)); err != nil {
			t.Fatalf("reference append %d: %v", i, err)
		}
	}
	if _, _, d, ok := decodeHead(mustGet(t, store, headKey(tenantID))); ok {
		referenceDigestAfter5 = d
	} else {
		t.Fatal("reference head does not decode")
	}

	restored := NewChain()
	if err := restored.Restore(store); err != nil {
		t.Fatalf("restore: %v", err)
	}

	var prev [32]byte
	encode := fixedRecord(5)
	seq, err := restored.Append(store, tenantID, func(p [32]byte, genesis bool) ([]byte, [32]byte, error) {
		prev = p
		return encode(p, genesis)
	})
	if err != nil {
		t.Fatalf("append after restore: %v", err)
	}
	if seq != 6 {
		t.Fatalf("sequence after restore = %d, want 6 (continuing from the restored head)", seq)
	}
	if prev != referenceDigestAfter5 {
		t.Fatalf("event after restore: prev = %x, want %x (the pre-restore head)", prev, referenceDigestAfter5)
	}
}

// TestChain_RestoreWithNoRecords leaves a fresh Chain's heads empty -- the
// zero value for an unknown tenant -- so the first Append after a restore on
// an empty table still starts the chain at sequence 1, exactly as a brand
// new tenant would.
func TestChain_RestoreWithNoRecords(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	if err := c.Restore(store); err != nil {
		t.Fatalf("restore: %v", err)
	}
	seq, err := c.Append(store, "acme", fixedRecord(0))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if seq != 1 {
		t.Fatalf("sequence = %d, want 1", seq)
	}
}

// TestChain_Prune deletes a prefix of the chain without disturbing the head,
// so the surviving chain continues to extend correctly and the pruned range
// holds nothing -- the replicated equivalent of the file sink's retention
// window.
func TestChain_Prune(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()
	c := NewChain()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedRecord(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	deleted, err := c.Prune(store, tenantID, 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("pruned %d events, want 2 (sequences 1 and 2)", deleted)
	}
	if n := countEvents(t, store, tenantID); n != 3 {
		t.Fatalf("%d events survive the prune, want 3", n)
	}
	if _, lo, _ := c.Head(tenantID); lo != 3 {
		t.Fatalf("low-watermark after prune = %d, want 3", lo)
	}

	// The chain still extends correctly: Prune never touches seq, so the
	// next Append continues from sequence 5.
	seq, err := c.Append(store, tenantID, fixedRecord(5))
	if err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	if seq != 6 {
		t.Fatalf("sequence after prune = %d, want 6", seq)
	}
}

// TestChain_Prune_NeverDeletesTheNewestEvent is the coordinator's ruling
// (N2): a cutoff at or beyond the current head is clamped to head.seq, so a
// chain always keeps at least one surviving record -- an anchor a verifier
// can check the surviving chain against. Pruning with a cutoff equal to
// seq+1 (every event, if taken literally) must still leave exactly one.
func TestChain_Prune_NeverDeletesTheNewestEvent(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()
	c := NewChain()
	for i := 0; i < 3; i++ {
		if _, err := c.Append(store, tenantID, fixedRecord(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	deleted, err := c.Prune(store, tenantID, 100) // far beyond the head
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("pruned %d events, want 2 (everything strictly before the newest)", deleted)
	}
	if n := countEvents(t, store, tenantID); n != 1 {
		t.Fatalf("got %d surviving events, want exactly 1 (the anchor)", n)
	}

	// Pruning again with the same oversized cutoff is a no-op: there is
	// nothing below the newest event left to delete.
	deleted, err = c.Prune(store, tenantID, 100)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second prune deleted %d, want 0 (the anchor is never deleted)", deleted)
	}
}

// TestChain_Prune_UnknownTenantIsANoOp asserts a tenant with no chain prunes
// nothing and stages nothing.
func TestChain_Prune_UnknownTenantIsANoOp(t *testing.T) {
	store := memory.NewStorage()
	deleted, err := NewChain().Prune(store, "acme", 10)
	if err != nil || deleted != 0 {
		t.Fatalf("Prune(unknown tenant) = (%d, %v), want (0, nil)", deleted, err)
	}
	if v, err := store.Get(headKey("acme")); err != nil || v != nil {
		t.Fatalf("pruning an unknown tenant staged a head record: (%q, %v)", v, err)
	}
}

// NOTE on B1 (Prune non-determinism under Raft batch packing): Chain itself
// has no concept of "batch" at all -- Prune reads ONLY c.heads, never a
// storage.Reader -- so there is no meaningful batched-vs-unbatched
// distinction to construct a regression test against AT THIS LAYER; the
// prior bug was that Prune used to take a storage.Reader and compute its
// delete range from a committed-storage READ, whose result depended on
// whether the reader could see a same-batch, not-yet-committed write. The
// real packing-variance regression test -- driving [append, append, append,
// prune(3)] through one Update call versus four, on a real
// PebbleStateMachine/MVCCEngine -- lives at the engine layer, where "batch"
// is a real, observable thing: see
// TestPebbleStateMachine_AuditPrune_IsIndependentOfBatchPacking and
// TestMVCCEngine_AuditPrune_IsIndependentOfBatchPacking.

// TestChain_RefusesTenantIDContainingSlash covers Append and Prune alike: a
// tenant id with '/' could carve a second segment inside another tenant's
// range in the raw table's concatenated keys.
func TestChain_RefusesTenantIDContainingSlash(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	const evil = "acme/evil"

	_, err := c.Append(store, evil, fixedRecord(0))
	var invalid *InvalidTenantIDError
	if !errors.As(err, &invalid) {
		t.Fatalf("Append: err = %v, want *InvalidTenantIDError", err)
	}

	_, err = c.Prune(store, evil, 1)
	if !errors.As(err, &invalid) {
		t.Fatalf("Prune: err = %v, want *InvalidTenantIDError", err)
	}

	if err := ValidateTenantID("acme"); err != nil {
		t.Fatalf("ValidateTenantID(acme) = %v, want nil", err)
	}
}

// TestChain_Prune_CapsPerCommand asserts N6's per-command bound: a cutoff
// far beyond the low-watermark deletes at most MaxPrunePerCommand events in
// one call, and a second call continues from where the first left off.
func TestChain_Prune_CapsPerCommand(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()
	c := NewChain()
	const total = MaxPrunePerCommand + 10
	for i := 0; i < total; i++ {
		if _, err := c.Append(store, tenantID, fixedRecord(i%1000)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	deleted, err := c.Prune(store, tenantID, total) // cutoff far beyond lo
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != MaxPrunePerCommand {
		t.Fatalf("first prune deleted %d, want the cap %d", deleted, MaxPrunePerCommand)
	}

	deleted, err = c.Prune(store, tenantID, total)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	// Remaining to delete: total - MaxPrunePerCommand, minus 1 for the
	// anchor (the newest event is never deleted).
	wantSecond := total - MaxPrunePerCommand - 1
	if deleted != wantSecond {
		t.Fatalf("second prune deleted %d, want %d", deleted, wantSecond)
	}
}

// --- small local helpers kept out of the production path ---

func mustGet(t *testing.T, r interface {
	Get(key []byte) ([]byte, error)
}, key []byte) []byte {
	t.Helper()
	v, err := r.Get(key)
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	return v
}
