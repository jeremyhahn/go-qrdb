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

package audit

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// fixedEvent returns a deterministic AuditEvent: every field a caller could
// vary is fixed, so two independent Appends given the same sequence of these
// are expected to produce byte-identical chains -- the property
// docs/audit/architecture.md calls "assigned at apply time."
func fixedEvent(n int, tenantID string) AuditEvent {
	return AuditEvent{
		Timestamp: time.Date(2026, 10, 2, 12, 0, n, 0, time.UTC),
		Actor:     "operator",
		TenantID:  tenantID,
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   OutcomeSuccess,
	}
}

// TestChain_AppendIsDeterministicAcrossReplicas is the core correctness
// property: two independently constructed Chains, fed the identical sequence
// of commands (the way two Raft replicas apply the identical committed log),
// must compute byte-identical chains. Neither Chain ever reads from the
// other; the only shared input is the sequence of fixedEvent values, which is
// exactly what a committed Raft entry supplies in production.
func TestChain_AppendIsDeterministicAcrossReplicas(t *testing.T) {
	const tenantID = "acme"
	replicaA := NewChain()
	replicaB := NewChain()
	storeA := memory.NewStorage()
	storeB := memory.NewStorage()

	for i := 0; i < 5; i++ {
		seqA, err := replicaA.Append(storeA, tenantID, fixedEvent(i, tenantID))
		if err != nil {
			t.Fatalf("replica A append %d: %v", i, err)
		}
		seqB, err := replicaB.Append(storeB, tenantID, fixedEvent(i, tenantID))
		if err != nil {
			t.Fatalf("replica B append %d: %v", i, err)
		}
		if seqA != seqB {
			t.Fatalf("event %d: sequence diverged: A=%d B=%d", i, seqA, seqB)
		}
	}

	for i := uint64(1); i <= 5; i++ {
		va, err := storeA.Get(eventKey(tenantID, i))
		if err != nil {
			t.Fatalf("read A seq %d: %v", i, err)
		}
		vb, err := storeB.Get(eventKey(tenantID, i))
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

// TestChain_PrevHashLinksToThePriorDigest asserts the chain is actually
// linked, not merely sequential: each event's stored PrevHash must name the
// head digest the PRIOR append produced, and the first event's PrevHash must
// be empty (no predecessor), mirroring the file sink's documented genesis
// behavior.
func TestChain_PrevHashLinksToThePriorDigest(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	var prevHeadHex string
	for i := 0; i < 4; i++ {
		seq, err := c.Append(store, tenantID, fixedEvent(i, tenantID))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		raw, err := store.Get(eventKey(tenantID, seq))
		if err != nil {
			t.Fatalf("read seq %d: %v", seq, err)
		}
		var stored AuditEvent
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatalf("decode seq %d: %v", seq, err)
		}
		if i == 0 {
			if stored.PrevHash != "" {
				t.Fatalf("genesis event carries a PrevHash: %q", stored.PrevHash)
			}
		} else if stored.PrevHash != prevHeadHex {
			t.Fatalf("event %d: PrevHash = %q, want the prior head %q", i, stored.PrevHash, prevHeadHex)
		}
		headRaw, err := store.Get(headKey(tenantID))
		if err != nil {
			t.Fatalf("read head after append %d: %v", i, err)
		}
		_, _, digest, ok := decodeHead(headRaw)
		if !ok {
			t.Fatalf("head record after append %d does not decode", i)
		}
		prevHeadHex = "sha256:" + hex.EncodeToString(digest[:])
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
		if _, err := reference.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
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

	seq, err := restored.Append(store, tenantID, fixedEvent(5, tenantID))
	if err != nil {
		t.Fatalf("append after restore: %v", err)
	}
	if seq != 6 {
		t.Fatalf("sequence after restore = %d, want 6 (continuing from the restored head)", seq)
	}
	raw := mustGet(t, store, eventKey(tenantID, 6))
	var stored AuditEvent
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode seq 6: %v", err)
	}
	want := "sha256:" + hex.EncodeToString(referenceDigestAfter5[:])
	if stored.PrevHash != want {
		t.Fatalf("event after restore: PrevHash = %q, want %q (the pre-restore head)", stored.PrevHash, want)
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
	seq, err := c.Append(store, "acme", fixedEvent(0, "acme"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if seq != 1 {
		t.Fatalf("sequence = %d, want 1", seq)
	}
}

// TestChain_QueryConfinesToTheRequestedTenant is the critical regression
// test: two tenants' events are interleaved in the SAME physical table (the
// shared-system-shard case for prefix/barrier tenants), and Query for one
// tenant must never return the other's events, platform events included.
// This is exactly the class of bug docs/multi-tenancy/architecture.md
// documents for CollectionService and LockService.
func TestChain_QueryConfinesToTheRequestedTenant(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()

	for i := 0; i < 3; i++ {
		if _, err := c.Append(store, "acme", fixedEvent(i, "acme")); err != nil {
			t.Fatalf("append acme %d: %v", i, err)
		}
		if _, err := c.Append(store, "widgets", fixedEvent(i, "widgets")); err != nil {
			t.Fatalf("append widgets %d: %v", i, err)
		}
	}
	// A platform event: stored under the system tenant's own range, with no
	// TenantID of its own -- mirrors a node's inspection opt-out today.
	if _, err := c.Append(store, "system", AuditEvent{
		Timestamp: time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC),
		Actor:     "node",
		Resource:  "inspection",
		Action:    "opt-out",
		Outcome:   OutcomeSuccess,
	}); err != nil {
		t.Fatalf("append platform event: %v", err)
	}

	page, err := c.Query(store, "acme", Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query acme: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("acme query returned %d events, want 3", len(page.Events))
	}
	for _, ev := range page.Events {
		if ev.TenantID != "acme" {
			t.Fatalf("acme query returned a foreign event: %+v", ev)
		}
	}

	page, err = c.Query(store, "widgets", Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("widgets query returned %d events, want 3", len(page.Events))
	}

	// The system tenant without Untenanted sees only its own (zero) events,
	// never the platform event.
	page, err = c.Query(store, "system", Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query system (no Untenanted): %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("system query without Untenanted returned %d events, want 0", len(page.Events))
	}

	// The system tenant WITH Untenanted sees the platform event.
	page, err = c.Query(store, "system", Filter{Limit: 100, Untenanted: true})
	if err != nil {
		t.Fatalf("query system (Untenanted): %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].Action != "opt-out" {
		t.Fatalf("system query with Untenanted = %+v, want exactly the platform event", page.Events)
	}
}

// TestChain_Prune deletes a prefix of the chain without disturbing the head,
// so the surviving chain continues to extend correctly and a query over the
// pruned range finds nothing there -- the replicated equivalent of the file
// sink's retention window.
func TestChain_Prune(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()
	c := NewChain()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
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

	page, err := c.Query(store, tenantID, Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query after prune: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("query after prune returned %d events, want 3", len(page.Events))
	}

	// The chain still extends correctly: Prune never touches seq, so the
	// next Append continues from sequence 5.
	seq, err := c.Append(store, tenantID, fixedEvent(5, tenantID))
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
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
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

	page, err := c.Query(store, tenantID, Filter{Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("got %d surviving events, want exactly 1 (the anchor)", len(page.Events))
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

// NOTE on B1 (Prune non-determinism under Raft batch packing): Chain itself
// has no concept of "batch" at all -- Prune and Query both read ONLY
// c.heads, never a storage.Reader -- so there is no meaningful
// batched-vs-unbatched distinction to construct a regression test against
// AT THIS LAYER; the prior bug was that Prune used to take a storage.Reader
// and compute its delete range from a committed-storage READ, whose result
// depended on whether the reader could see a same-batch, not-yet-committed
// write. The real packing-variance regression test -- driving
// [append, append, append, prune(3)] through one Update call versus four,
// on a real PebbleStateMachine/MVCCEngine -- lives at the engine layer,
// where "batch" is a real, observable thing: see
// TestPebbleStateMachine_AuditPrune_IsIndependentOfBatchPacking and
// TestMVCCEngine_AuditPrune_IsIndependentOfBatchPacking.

// TestChain_Append_RefusesTenantMismatch asserts a conflicting ev.TenantID
// is refused rather than silently overwritten ("stamped"): see
// TenantMismatchError's doc comment for why overwriting would be worse.
func TestChain_Append_RefusesTenantMismatch(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	_, err := c.Append(store, "acme", fixedEvent(0, "widgets"))
	var mismatch *TenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *TenantMismatchError", err)
	}
}

// TestChain_Append_LeavesEmptyTenantIDAlone asserts the platform-event case
// is NOT refused and NOT stamped: ev.TenantID stays empty, which is what
// Query's Untenanted matching depends on.
func TestChain_Append_LeavesEmptyTenantIDAlone(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	ev := fixedEvent(0, "")
	if _, err := c.Append(store, "system", ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	raw, err := store.Get(eventKey("system", 1))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var stored AuditEvent
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stored.TenantID != "" {
		t.Fatalf("stored TenantID = %q, want empty (not stamped)", stored.TenantID)
	}
}

// TestChain_RefusesTenantIDContainingSlash covers Append, Prune and Query
// alike: a tenant id with '/' could carve a second segment inside another
// tenant's range in the raw table's concatenated keys.
func TestChain_RefusesTenantIDContainingSlash(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	const evil = "acme/evil"

	_, err := c.Append(store, evil, fixedEvent(0, evil))
	var invalid *InvalidTenantIDError
	if !errors.As(err, &invalid) {
		t.Fatalf("Append: err = %v, want *InvalidTenantIDError", err)
	}

	_, err = c.Prune(store, evil, 1)
	if !errors.As(err, &invalid) {
		t.Fatalf("Prune: err = %v, want *InvalidTenantIDError", err)
	}

	_, err = c.Query(store, evil, Filter{})
	if !errors.As(err, &invalid) {
		t.Fatalf("Query: err = %v, want *InvalidTenantIDError", err)
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
		if _, err := c.Append(store, tenantID, fixedEvent(i%1000, tenantID)); err != nil {
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

// TestChain_QueryPagination asserts the cursor returned by one page resumes
// exactly where the previous page stopped, with no gap and no overlap.
func TestChain_QueryPagination(t *testing.T) {
	const tenantID = "acme"
	store := memory.NewStorage()
	c := NewChain()
	for i := 0; i < 10; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	seen := make(map[time.Time]bool)
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page, err := c.Query(store, tenantID, Filter{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("query page %d: %v", pages, err)
		}
		for _, ev := range page.Events {
			if seen[ev.Timestamp] {
				t.Fatalf("event %v returned twice across pages", ev.Timestamp)
			}
			seen[ev.Timestamp] = true
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 10 {
		t.Fatalf("paginated through %d distinct events, want 10", len(seen))
	}
}

// TestChain_Append_RefusesZeroTimestamp is the regression test for the exact
// defect this design exists to prevent: Append must never invent a
// Timestamp from its own wall clock, because doing so would fold a
// different value into each replica's digest for the identical committed
// entry. This was caught by TestMVCCEngine_AuditAppend_DeterministicAcrossTwoEngines
// (pkg/engine/mvcc_audit_test.go) failing on a real time.Now() divergence
// before this refusal existed.
func TestChain_Append_RefusesZeroTimestamp(t *testing.T) {
	c := NewChain()
	store := memory.NewStorage()
	_, err := c.Append(store, "acme", AuditEvent{Actor: "operator", Resource: "barrier", Action: "unseal"})
	var missing *MissingTimestampError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *MissingTimestampError", err)
	}
}

// TestChain_QueryInvalidCursor asserts a cursor this package did not mint is
// refused rather than silently reinterpreted as "from the start."
func TestChain_QueryInvalidCursor(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	_, err := c.Query(store, "acme", Filter{Cursor: "not-a-sequence"})
	var invalid *InvalidCursorError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidCursorError", err)
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
