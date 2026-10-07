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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// TestChain_Verify_EmptyTenantVerifiesTrivially asserts a tenant with no
// chain at all reports Verified: true, Genesis: true, Records: 0 -- the same
// convention the file-based chain uses for a trail with no records.
func TestChain_Verify_EmptyTenantVerifiesTrivially(t *testing.T) {
	c := NewChain()
	store := memory.NewStorage()

	report, err := c.Verify(store, "acme")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Verified || !report.Genesis || report.Records != 0 {
		t.Fatalf("empty tenant: got %+v", report)
	}
}

// TestChain_Verify_HonestChainVerifies asserts a chain built entirely
// through Append verifies clean, with Genesis true (nothing pruned) and
// Records equal to the number appended.
func TestChain_Verify_HonestChainVerifies(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	report, err := c.Verify(store, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Verified {
		t.Fatalf("want Verified, got %+v", report)
	}
	if report.Records != 5 || report.Head != 5 || report.Low != 1 || !report.Genesis {
		t.Fatalf("got %+v", report)
	}
}

// TestChain_Verify_DetectsTamperedRecord asserts that editing a stored
// event's bytes directly in storage -- the hash-chain equivalent of editing
// a line in the node-local NDJSON file -- is caught: the terminal fold no
// longer reaches the chain's own authoritative head digest.
func TestChain_Verify_DetectsTamperedRecord(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	var ev AuditEvent
	raw, err := store.Get(eventKey(tenantID, 3))
	if err != nil {
		t.Fatalf("read seq 3: %v", err)
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal seq 3: %v", err)
	}
	ev.Actor = "attacker"
	tampered, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal tampered event: %v", err)
	}
	if err := store.Set(eventKey(tenantID, 3), tampered); err != nil {
		t.Fatalf("write tampered event: %v", err)
	}

	report, err := c.Verify(store, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Verified {
		t.Fatal("tampered chain reported Verified: true")
	}
	if report.Reason == "" {
		t.Fatal("tampered chain reported no reason")
	}
}

// TestChain_Verify_PrunedChainVerifiesWithGenesisFalse asserts that
// retention pruning -- which deletes a prefix of events and leaves the head
// digest untouched -- is reported as a clean verification with Genesis:
// false, exactly as the node-local file chain reports a retention-pruned
// trail rather than refusing it.
func TestChain_Verify_PrunedChainVerifiesWithGenesisFalse(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if _, err := c.Prune(store, tenantID, 3); err != nil {
		t.Fatalf("prune: %v", err)
	}

	report, err := c.Verify(store, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Verified {
		t.Fatalf("pruned chain should still verify: %+v", report)
	}
	if report.Genesis {
		t.Fatal("pruned chain reported Genesis: true")
	}
	if report.Low != 3 || report.Head != 5 || report.Records != 3 {
		t.Fatalf("got %+v", report)
	}
}

// TestChain_Verify_DetectsMissingSequence asserts a deleted (not pruned --
// the head's lo untouched) event in the middle of the surviving range is
// caught as a gap, distinct from a hash break.
func TestChain_Verify_DetectsMissingSequence(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()

	for i := 0; i < 5; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := store.Delete(eventKey(tenantID, 3)); err != nil {
		t.Fatalf("delete seq 3: %v", err)
	}

	report, err := c.Verify(store, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Verified {
		t.Fatal("chain with a missing sequence reported Verified: true")
	}
}

// TestChain_Verify_RefusesInvalidTenantID mirrors Query/Append/Prune's own
// '/'-rejection, since Verify shares the same raw-table key space.
func TestChain_Verify_RefusesInvalidTenantID(t *testing.T) {
	c := NewChain()
	store := memory.NewStorage()

	_, err := c.Verify(store, "a/b")
	var invalidErr *InvalidTenantIDError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("want *InvalidTenantIDError, got %v (%T)", err, err)
	}
}
