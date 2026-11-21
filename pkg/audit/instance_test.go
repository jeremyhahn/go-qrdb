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
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

func marshaledEvent(t *testing.T, n int, tenantID string) []byte {
	t.Helper()
	b, err := json.Marshal(fixedEvent(n, tenantID))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestChain_AppendToInstance_ContinuesTheChain switches a tenant's chain to
// its audit instance mid-stream: leaf 0 is the event at S₀ carrying the
// pre-switch digest, every later event is a leaf, Verify stays continuous
// across the switch and after a prune, and the leaves' proofs verify.
func TestChain_AppendToInstance_ContinuesTheChain(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()
	id := vlog.AuditLogID(tenantID)

	for i := range 3 {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_, _, pre := c.log.Head(tenantID)

	seq, err := c.AppendToInstance(store, tenantID, [][]byte{marshaledEvent(t, 3, tenantID), marshaledEvent(t, 4, tenantID)})
	if err != nil || seq != 5 {
		t.Fatalf("AppendToInstance = %d, %v, want 5", seq, err)
	}
	desc, err := vlog.ReadDescriptor(store, id)
	if err != nil || desc.TreeStart != 4 {
		t.Fatalf("descriptor = %+v, %v, want TreeStart 4", desc, err)
	}
	leaves, err := vlog.Get(store, id, 0, 2)
	if err != nil || len(leaves) != 2 {
		t.Fatalf("Get = %v, %v", leaves, err)
	}
	var first AuditEvent
	if err := json.Unmarshal(leaves[0].Body, &first); err != nil {
		t.Fatal(err)
	}
	if want := "sha256:" + hex.EncodeToString(pre[:]); first.PrevHash != want {
		t.Fatalf("leaf 0 PrevHash = %q, want the pre-switch head %q", first.PrevHash, want)
	}

	// An OpAuditAppend after the switch is the next leaf.
	for i := 5; i < 7; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	report, err := c.Verify(store, tenantID)
	if err != nil || !report.Verified || report.Records != 7 || !report.Genesis {
		t.Fatalf("Verify across the switch = %+v, %v", report, err)
	}

	head, err := vlog.ReadHead(store, id)
	if err != nil || head.Size != 4 {
		t.Fatalf("ReadHead = %+v, %v, want size 4", head, err)
	}
	all, err := vlog.Get(store, id, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		p, err := vlog.Inclusion(store, id, e.Index, head.Size)
		if err != nil {
			t.Fatalf("Inclusion(%d): %v", e.Index, err)
		}
		if err := proof.VerifyInclusion(rfc6962.DefaultHasher, e.Index, head.Size, rfc6962.DefaultHasher.HashLeaf(e.Body), p, head.Root); err != nil {
			t.Fatalf("VerifyInclusion(%d): %v", e.Index, err)
		}
	}
	// Prune past S₀ is the instance's ExpireBodies: Verify still holds over
	// what survives, and the tree is untouched.
	if _, err := c.Prune(store, tenantID, 6); err != nil {
		t.Fatal(err)
	}
	report, err = c.Verify(store, tenantID)
	if err != nil || !report.Verified || report.Low != 6 {
		t.Fatalf("Verify after prune = %+v, %v", report, err)
	}
	after, err := vlog.ReadHead(store, id)
	if err != nil || !bytes.Equal(after.Root, head.Root) || after.BodiesFrom != 2 {
		t.Fatalf("head after prune = %+v, %v, want the same root and BodiesFrom 2", after, err)
	}
}

// An onboarding emitter whose append outlived its timeout retries it; the
// delayed first proposal and the retry both apply. The trail holds one
// event, on a restored replica too.
func TestChain_OnboardEventAppliedOnceAcrossRetry(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()
	ev := fixedEvent(0, tenantID)
	ev.Action = ActionTenantOnboard
	ev.RequestID = "req-1"
	ev.AppendOnce = true
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	// The retry and the delayed first proposal, applied in one batch and
	// then once more through the other opcode.
	if _, err := c.AppendToInstance(store, tenantID, [][]byte{raw, raw}); err != nil {
		t.Fatalf("AppendToInstance: %v", err)
	}
	if _, err := c.Append(store, tenantID, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq, _, _ := c.log.Head(tenantID); seq != 1 {
		t.Fatalf("head seq = %d after three applies of one event, want 1", seq)
	}

	restored := NewChain()
	if err := restored.Restore(store); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := restored.Append(store, tenantID, ev); err != nil {
		t.Fatalf("Append after restore: %v", err)
	}
	if seq, _, _ := restored.log.Head(tenantID); seq != 1 {
		t.Fatalf("head seq = %d after restore and a repeat, want 1", seq)
	}
}

// TestChain_OnboardEventWithoutMarkerAppendsEachTime pins the old shape: an
// onboarding event proposed before audit-append-once is active carries no
// AppendOnce marker, and every binary appends it each time it is applied,
// through either opcode, so old and new replicas chain the same log alike;
// a marked event applied twice appends once.
func TestChain_OnboardEventWithoutMarkerAppendsEachTime(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()
	ev := fixedEvent(0, tenantID)
	ev.Action = ActionTenantOnboard
	ev.RequestID = "req-1"
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(store, tenantID, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	first, _, _ := c.log.Head(tenantID)
	if _, err := c.AppendToInstance(store, tenantID, [][]byte{raw}); err != nil {
		t.Fatalf("AppendToInstance: %v", err)
	}
	if _, err := c.Append(store, tenantID, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq, _, _ := c.log.Head(tenantID); seq != first+2 {
		t.Fatalf("head seq = %d after three applies of an unmarked event, want %d", seq, first+2)
	}

	marked := ev
	marked.RequestID = "req-2"
	marked.AppendOnce = true
	if _, err := c.Append(store, tenantID, marked); err != nil {
		t.Fatalf("Append marked: %v", err)
	}
	if _, err := c.Append(store, tenantID, marked); err != nil {
		t.Fatalf("Append marked again: %v", err)
	}
	if seq, _, _ := c.log.Head(tenantID); seq != first+3 {
		t.Fatalf("head seq = %d after a marked event applied twice, want %d", seq, first+3)
	}
}

// An onboarding emitter's unmarked proposal, made before audit-append-once
// was active, timed out but stayed in flight; its retry, made once the
// feature is active, is marked. Whichever applies first, through either
// opcode, the trail holds one event -- on a replica restored between the
// two as well. No time passes in between: the outcome does not rest on the
// delayed proposal being judged lost.
// rawTable returns every key and value of store, in key order.
func rawTable(t *testing.T, store *memory.MemoryStorage) [][2]string {
	t.Helper()
	it := store.NewIterator(&storage.IteratorOptions{})
	var kv [][2]string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		kv = append(kv, [2]string{string(it.Key()), string(it.Value())})
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	if err := it.Close(); err != nil {
		t.Fatal(err)
	}
	return kv
}

// sameTable fails t unless got and want hold exactly the same keys and
// values.
func sameTable(t *testing.T, got, want [][2]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("table has %d records, want %d:\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// onboardingEvents returns an unmarked onboarding event and its marked
// retry, the same identity.
func onboardingEvents(tenantID string) (unmarked, marked AuditEvent) {
	unmarked = fixedEvent(0, tenantID)
	unmarked.Action = ActionTenantOnboard
	unmarked.RequestID = "req-1"
	marked = unmarked
	marked.AppendOnce = true
	return unmarked, marked
}

// applyEvent applies ev to c as the audit-append opcode (instance false)
// or the verifiable-log opcode (instance true) does.
func applyEvent(t *testing.T, c *Chain, store *memory.MemoryStorage, tenantID string, ev AuditEvent, instance bool) {
	t.Helper()
	if !instance {
		if _, err := c.Append(store, tenantID, ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AppendToInstance(store, tenantID, [][]byte{raw}); err != nil {
		t.Fatalf("AppendToInstance: %v", err)
	}
}

// TestChain_UnmarkedOnboardingReplaysThePreActivationState replays the
// same unmarked onboarding commands a release without the append-once
// marker applied and requires the complete raw audit table to be the one
// that release wrote: the event, head and tree records only.
func TestChain_UnmarkedOnboardingReplaysThePreActivationState(t *testing.T) {
	const tenantID = "acme"
	unmarked, _ := onboardingEvents(tenantID)
	for _, instance := range []bool{false, true} {
		t.Run(map[bool]string{false: "audit append", true: "instance"}[instance], func(t *testing.T) {
			want := memory.NewStorage()
			legacy := NewChain()
			for range 2 {
				var err error
				if instance {
					_, err = legacy.log.AppendAudit(want, tenantID, linkEvent(unmarked))
				} else {
					_, err = legacy.log.Append(want, tenantID, linkEvent(unmarked))
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			got := memory.NewStorage()
			c := NewChain()
			for range 2 {
				applyEvent(t, c, got, tenantID, unmarked, instance)
			}
			sameTable(t, rawTable(t, got), rawTable(t, want))
		})
	}
}

// TestChain_MarkedRetryAfterUnmarkedOriginalIsReplicaIndependent applies an
// unmarked onboarding proposal and its marked retry on two replicas, one
// that applied the original before upgrading (the plain append, then a
// restore) and one that applied both after, and requires the same raw
// audit table on both, in either order of the two entries.
func TestChain_MarkedRetryAfterUnmarkedOriginalIsReplicaIndependent(t *testing.T) {
	const tenantID = "acme"
	unmarked, marked := onboardingEvents(tenantID)
	for _, instance := range []bool{false, true} {
		for _, order := range [][2]AuditEvent{{unmarked, marked}, {marked, unmarked}} {
			name := map[bool]string{false: "audit append", true: "instance"}[instance]
			if order[0].AppendOnce {
				name += ", marked first"
			}
			t.Run(name, func(t *testing.T) {
				upgraded := memory.NewStorage()
				c := NewChain()
				applyEvent(t, c, upgraded, tenantID, order[0], instance)
				applyEvent(t, c, upgraded, tenantID, order[1], instance)

				restarted := memory.NewStorage()
				before := NewChain()
				if order[0].AppendOnce {
					applyEvent(t, before, restarted, tenantID, order[0], instance)
				} else {
					var err error
					if instance {
						_, err = before.log.AppendAudit(restarted, tenantID, linkEvent(order[0]))
					} else {
						_, err = before.log.Append(restarted, tenantID, linkEvent(order[0]))
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				after := NewChain()
				if err := after.Restore(restarted); err != nil {
					t.Fatalf("Restore: %v", err)
				}
				applyEvent(t, after, restarted, tenantID, order[1], instance)

				sameTable(t, rawTable(t, restarted), rawTable(t, upgraded))
			})
		}
	}
}

// TestChain_MarkedRetryOfMarkedProposalAppendsOnce: a marked proposal and
// its marked retry leave one event, restored or not.
func TestChain_MarkedRetryOfMarkedProposalAppendsOnce(t *testing.T) {
	const tenantID = "acme"
	_, marked := onboardingEvents(tenantID)
	for _, restore := range []bool{false, true} {
		c := NewChain()
		store := memory.NewStorage()
		applyEvent(t, c, store, tenantID, marked, false)
		first, _, _ := c.log.Head(tenantID)
		if restore {
			c = NewChain()
			if err := c.Restore(store); err != nil {
				t.Fatalf("Restore: %v", err)
			}
		}
		applyEvent(t, c, store, tenantID, marked, false)
		if seq, _, _ := c.log.Head(tenantID); seq != first {
			t.Fatalf("restore=%v: head seq = %d after the marked retry, want %d", restore, seq, first)
		}
	}
}

func TestChain_AppendToInstance_Refusals(t *testing.T) {
	const tenantID = "acme"
	zero := fixedEvent(0, tenantID)
	zero.Timestamp = time.Time{}
	zeroRaw, _ := json.Marshal(zero)
	var (
		malformed *MalformedEventError
		missing   *MissingTimestampError
		mismatch  *TenantMismatchError
		batch     *vlog.AppendBatchError
		badTenant *vlog.InvalidTenantIDError
	)
	for name, tc := range map[string]struct {
		tenant  string
		entries [][]byte
		target  any
	}{
		"malformed":       {tenantID, [][]byte{[]byte("{")}, &malformed},
		"zero timestamp":  {tenantID, [][]byte{zeroRaw}, &missing},
		"tenant mismatch": {tenantID, [][]byte{marshaledEvent(t, 1, "other")}, &mismatch},
		"empty batch":     {tenantID, nil, &batch},
		"slash in tenant": {"a/b", [][]byte{marshaledEvent(t, 1, "")}, &badTenant},
		"one bad of two":  {tenantID, [][]byte{marshaledEvent(t, 1, tenantID), []byte("x")}, &malformed},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			store := memory.NewStorage()
			_, err := c.AppendToInstance(store, tc.tenant, tc.entries)
			if err == nil || !errors.As(err, tc.target) {
				t.Fatalf("AppendToInstance = %v, want %T", err, tc.target)
			}
			if seq, _, _ := c.log.Head(tc.tenant); seq != 0 {
				t.Fatalf("a refused append advanced the chain to %d", seq)
			}
		})
	}
	if (&MalformedEventError{Err: errors.New("x")}).Error() == "" {
		t.Fatal("empty error text")
	}
}
