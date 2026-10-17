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
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
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
		raw, err := store.Get(vlog.EventKey(tenantID, seq))
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
		_, _, digest := c.log.Head(tenantID)
		prevHeadHex = "sha256:" + hex.EncodeToString(digest[:])
	}
}

// TestChainOn_SharesTheRegistry asserts the audit Chain ChainOn returns
// writes through the registry it was given, so a state machine's audit
// instance and its named logs advance one in-memory head table.
func TestChainOn_SharesTheRegistry(t *testing.T) {
	const tenantID = "acme"
	logs := vlog.NewChain()
	c := ChainOn(logs)
	if _, err := c.Append(memory.NewStorage(), tenantID, fixedEvent(0, tenantID)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if seq, _, _ := logs.Head(tenantID); seq != 1 {
		t.Fatalf("registry head seq = %d, want 1", seq)
	}
}

// TestChainOn_NilRegistryIsNilChain asserts a state machine with no registry
// has no audit chain either, so the apply path refuses OpAuditAppend as
// unconfigured rather than dereferencing nil.
func TestChainOn_NilRegistryIsNilChain(t *testing.T) {
	if c := ChainOn(nil); c != nil {
		t.Fatalf("ChainOn(nil) = %v, want nil", c)
	}
}

// TestChain_RestoreContinuesTheChain asserts a Chain rebuilt from the table
// alone -- a restart, or a snapshot recovered on another replica -- carries on
// where the one that wrote it stopped: the next sequence number and a PrevHash
// naming the restored head, not a second genesis.
func TestChain_RestoreContinuesTheChain(t *testing.T) {
	const tenantID = "acme"
	writer := NewChain()
	store := memory.NewStorage()
	var lastSeq uint64
	for i := 0; i < 3; i++ {
		seq, err := writer.Append(store, tenantID, fixedEvent(i, tenantID))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		lastSeq = seq
	}
	_, _, digest := writer.log.Head(tenantID)

	restored := NewChain()
	if err := restored.Restore(store); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	seq, err := restored.Append(store, tenantID, fixedEvent(3, tenantID))
	if err != nil {
		t.Fatalf("append after restore: %v", err)
	}
	if seq != lastSeq+1 {
		t.Fatalf("seq after restore = %d, want %d", seq, lastSeq+1)
	}
	raw, err := store.Get(vlog.EventKey(tenantID, seq))
	if err != nil {
		t.Fatalf("read seq %d: %v", seq, err)
	}
	var stored AuditEvent
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode seq %d: %v", seq, err)
	}
	if want := "sha256:" + hex.EncodeToString(digest[:]); stored.PrevHash != want {
		t.Fatalf("PrevHash after restore = %q, want the restored head %q", stored.PrevHash, want)
	}
}

// TestChain_RestoreRefusesACorruptHead asserts Restore surfaces a malformed
// head record as vlog's typed corruption error rather than starting the
// tenant's chain over.
func TestChain_RestoreRefusesACorruptHead(t *testing.T) {
	store := memory.NewStorage()
	if err := store.Set([]byte(vlog.TablePrefix+"h/acme"), []byte{1, 2, 3}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var corrupt *vlog.CorruptTableError
	if err := NewChain().Restore(store); !errors.As(err, &corrupt) {
		t.Fatalf("Restore = %v, want *vlog.CorruptTableError", err)
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
	raw, err := store.Get(vlog.EventKey("system", 1))
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

// TestChain_RefusesTenantIDContainingSlash covers Append and Query alike: a
// tenant id with '/' could carve a second segment inside another tenant's
// range in the raw table's concatenated keys. Prune's refusal is pkg/vlog's.
func TestChain_RefusesTenantIDContainingSlash(t *testing.T) {
	store := memory.NewStorage()
	c := NewChain()
	const evil = "acme/evil"

	_, err := c.Append(store, evil, fixedEvent(0, evil))
	var invalid *vlog.InvalidTenantIDError
	if !errors.As(err, &invalid) {
		t.Fatalf("Append: err = %v, want *InvalidTenantIDError", err)
	}

	_, err = c.Query(store, evil, Filter{})
	if !errors.As(err, &invalid) {
		t.Fatalf("Query: err = %v, want *InvalidTenantIDError", err)
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
