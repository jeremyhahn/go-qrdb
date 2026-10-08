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

package lease

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

func TestNewLessorReturnsNonNil(t *testing.T) {
	lr := NewLessor()
	if lr == nil {
		t.Fatal("NewLessor() returned nil")
	}
}

func TestNewLessorStartsEmpty(t *testing.T) {
	lr := NewLessor()
	if got := lr.Count(); got != 0 {
		t.Errorf("NewLessor().Count() = %d, want 0", got)
	}
}

func TestGrantCreatesLease(t *testing.T) {
	lr := NewLessor()
	info, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatalf("Grant(10) returned unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("Grant(10) returned nil info")
	}
	if info.ID <= 0 {
		t.Errorf("Grant(10) ID = %d, want > 0", info.ID)
	}
	if info.TTL != 10 {
		t.Errorf("Grant(10) TTL = %d, want 10", info.TTL)
	}
	if info.GrantedTTL != 10 {
		t.Errorf("Grant(10) GrantedTTL = %d, want 10", info.GrantedTTL)
	}
}

func TestGrantAutoIncrementsID(t *testing.T) {
	lr := NewLessor()
	info1, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatalf("Grant(10) first call returned unexpected error: %v", err)
	}
	info2, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatalf("Grant(10) second call returned unexpected error: %v", err)
	}
	if info1.ID == info2.ID {
		t.Errorf("Grant produced duplicate IDs: %d", info1.ID)
	}
	if info2.ID <= info1.ID {
		t.Errorf("Grant IDs not monotonically increasing: first=%d, second=%d", info1.ID, info2.ID)
	}
}

func TestGrantInvalidTTLZero(t *testing.T) {
	lr := NewLessor()
	_, err := lr.Grant(nopWriter{}, 0)
	if err == nil {
		t.Fatal("Grant(0) expected error, got nil")
	}
	if _, ok := err.(*InvalidTTLError); !ok {
		t.Errorf("Grant(0) error type = %T, want *InvalidTTLError", err)
	}
}

func TestGrantInvalidTTLNegative(t *testing.T) {
	lr := NewLessor()
	_, err := lr.Grant(nopWriter{}, -5)
	if err == nil {
		t.Fatal("Grant(-5) expected error, got nil")
	}
	ite, ok := err.(*InvalidTTLError)
	if !ok {
		t.Fatalf("Grant(-5) error type = %T, want *InvalidTTLError", err)
	}
	if ite.TTL != -5 {
		t.Errorf("InvalidTTLError.TTL = %d, want -5", ite.TTL)
	}
}

func TestGrantOnClosedLessor(t *testing.T) {
	lr := NewLessor()
	lr.Close()
	_, err := lr.Grant(nopWriter{}, 10)
	if err == nil {
		t.Fatal("Grant on closed lessor expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("Grant on closed lessor error type = %T, want *LessorClosedError", err)
	}
}

func TestGrantOnClosedLessorMessage(t *testing.T) {
	lr := NewLessor()
	lr.Close()
	_, err := lr.Grant(nopWriter{}, 10)
	if err == nil {
		t.Fatal("Grant on closed lessor expected error, got nil")
	}
	if err.Error() != "lessor is closed" {
		t.Errorf("error message = %q, want %q", err.Error(), "lessor is closed")
	}
}

func TestRevokeRemovesLease(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	_, err := lr.Revoke(nopWriter{}, info.ID)
	if err != nil {
		t.Fatalf("Revoke(%d) returned unexpected error: %v", info.ID, err)
	}
	if lr.Lookup(info.ID) != nil {
		t.Errorf("Lookup(%d) returned non-nil after Revoke", info.ID)
	}
}

func TestRevokeReturnsAttachedKeys(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("key1"))
	mustBind(t, lr, info.ID, []byte("key2"))

	keys, err := lr.Revoke(nopWriter{}, info.ID)
	if err != nil {
		t.Fatalf("Revoke(%d) returned unexpected error: %v", info.ID, err)
	}
	if len(keys) != 2 {
		t.Fatalf("Revoke(%d) returned %d keys, want 2", info.ID, len(keys))
	}

	keySet := make(map[string]struct{})
	for _, k := range keys {
		keySet[string(k)] = struct{}{}
	}
	if _, ok := keySet["key1"]; !ok {
		t.Error("Revoke did not return key1")
	}
	if _, ok := keySet["key2"]; !ok {
		t.Error("Revoke did not return key2")
	}
}

func TestRevokeUnknownIDReturnsNotFoundError(t *testing.T) {
	lr := NewLessor()
	_, err := lr.Revoke(nopWriter{}, 999)
	if err == nil {
		t.Fatal("Revoke(999) expected error, got nil")
	}
	if _, ok := err.(*NotFoundError); !ok {
		t.Errorf("Revoke(999) error type = %T, want *NotFoundError", err)
	}
}

func TestRevokeUnknownIDContainsID(t *testing.T) {
	lr := NewLessor()
	_, err := lr.Revoke(nopWriter{}, 12345)
	if err == nil {
		t.Fatal("expected error")
	}
	nfe, ok := err.(*NotFoundError)
	if !ok {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
	if nfe.ID != 12345 {
		t.Errorf("NotFoundError.ID = %d, want 12345", nfe.ID)
	}
}

func TestRevokeOnClosedLessor(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	lr.Close()
	_, err := lr.Revoke(nopWriter{}, info.ID)
	if err == nil {
		t.Fatal("Revoke on closed lessor expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}

func TestKeepAliveResetsTTL(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)

	updated, err := lr.KeepAlive(info.ID)
	if err != nil {
		t.Fatalf("KeepAlive(%d) returned unexpected error: %v", info.ID, err)
	}
	if updated == nil {
		t.Fatal("KeepAlive returned nil info")
	}
	if updated.TTL != 60 {
		t.Errorf("KeepAlive TTL = %d, want 60", updated.TTL)
	}
	if updated.RemainingTTL <= 0 {
		t.Errorf("KeepAlive RemainingTTL = %d, want > 0", updated.RemainingTTL)
	}
}

func TestKeepAlivePreservesID(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	updated, err := lr.KeepAlive(info.ID)
	if err != nil {
		t.Fatalf("KeepAlive returned unexpected error: %v", err)
	}
	if updated.ID != info.ID {
		t.Errorf("KeepAlive ID = %d, want %d", updated.ID, info.ID)
	}
}

func TestKeepAliveUnknownIDReturnsNotFoundError(t *testing.T) {
	lr := NewLessor()
	_, err := lr.KeepAlive(999)
	if err == nil {
		t.Fatal("KeepAlive(999) expected error, got nil")
	}
	if _, ok := err.(*NotFoundError); !ok {
		t.Errorf("KeepAlive(999) error type = %T, want *NotFoundError", err)
	}
}

func TestKeepAliveUnknownIDMessage(t *testing.T) {
	lr := NewLessor()
	_, err := lr.KeepAlive(888)
	if err == nil {
		t.Fatal("expected error")
	}
	nfe, ok := err.(*NotFoundError)
	if !ok {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
	if nfe.ID != 888 {
		t.Errorf("NotFoundError.ID = %d, want 888", nfe.ID)
	}
}

func TestKeepAliveOnClosedLessor(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	lr.Close()
	_, err := lr.KeepAlive(info.ID)
	if err == nil {
		t.Fatal("KeepAlive on closed lessor expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}

func TestLookupReturnsLeaseInfo(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 30)
	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil for existing lease")
	}
	if found.ID != info.ID {
		t.Errorf("Lookup ID = %d, want %d", found.ID, info.ID)
	}
	if found.TTL != 30 {
		t.Errorf("Lookup TTL = %d, want 30", found.TTL)
	}
}

func TestLookupUnknownIDReturnsNil(t *testing.T) {
	lr := NewLessor()
	found := lr.Lookup(999)
	if found != nil {
		t.Errorf("Lookup(999) = %v, want nil", found)
	}
}

func TestLookupAfterRevoke(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	_, _ = lr.Revoke(nopWriter{}, info.ID)
	found := lr.Lookup(info.ID)
	if found != nil {
		t.Errorf("Lookup after Revoke returned non-nil: %v", found)
	}
}

// ---------------------------------------------------------------------------
// LookupOrNotFound
// ---------------------------------------------------------------------------

func TestLookupOrNotFoundReturnsLeaseInfo(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 30)

	found, err := lr.LookupOrNotFound(info.ID)
	if err != nil {
		t.Fatalf("LookupOrNotFound: unexpected error: %v", err)
	}
	if found.ID != info.ID {
		t.Errorf("LookupOrNotFound ID = %d, want %d", found.ID, info.ID)
	}
}

func TestLookupOrNotFoundUnknownIDReturnsNotFoundError(t *testing.T) {
	lr := NewLessor()

	found, err := lr.LookupOrNotFound(999)
	if found != nil {
		t.Errorf("LookupOrNotFound(999) result = %v, want nil", found)
	}
	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
	if nfe.ID != 999 {
		t.Errorf("NotFoundError.ID = %d, want 999", nfe.ID)
	}
}

func TestLookupOrNotFoundAfterRevoke(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	_, _ = lr.Revoke(nopWriter{}, info.ID)

	found, err := lr.LookupOrNotFound(info.ID)
	if found != nil {
		t.Errorf("LookupOrNotFound after Revoke returned non-nil: %v", found)
	}
	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
}

func TestLeasesReturnsAll(t *testing.T) {
	lr := NewLessor()
	_, _ = lr.Grant(nopWriter{}, 10)
	_, _ = lr.Grant(nopWriter{}, 20)
	_, _ = lr.Grant(nopWriter{}, 30)

	leases := lr.Leases()
	if len(leases) != 3 {
		t.Errorf("Leases() returned %d leases, want 3", len(leases))
	}
}

func TestLeasesEmptyLessor(t *testing.T) {
	lr := NewLessor()
	leases := lr.Leases()
	if len(leases) != 0 {
		t.Errorf("Leases() on empty lessor returned %d leases, want 0", len(leases))
	}
}

func TestLeasesContainsGrantedIDs(t *testing.T) {
	lr := NewLessor()
	info1, _ := lr.Grant(nopWriter{}, 10)
	info2, _ := lr.Grant(nopWriter{}, 20)

	leases := lr.Leases()
	idSet := make(map[int64]struct{})
	for _, l := range leases {
		idSet[l.ID] = struct{}{}
	}
	if _, ok := idSet[info1.ID]; !ok {
		t.Errorf("Leases() missing ID %d", info1.ID)
	}
	if _, ok := idSet[info2.ID]; !ok {
		t.Errorf("Leases() missing ID %d", info2.ID)
	}
}

func TestAttachKeyAddsKey(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("mykey"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 1 {
		t.Fatalf("Keys count = %d, want 1", len(found.Keys))
	}
	if string(found.Keys[0]) != "mykey" {
		t.Errorf("Keys[0] = %q, want %q", string(found.Keys[0]), "mykey")
	}
}

func TestAttachKeyMultipleKeys(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("a"))
	mustBind(t, lr, info.ID, []byte("b"))
	mustBind(t, lr, info.ID, []byte("c"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 3 {
		t.Errorf("Keys count = %d, want 3", len(found.Keys))
	}
}

func TestBindUnknownLeaseReturnsNotFoundAndChangesNothing(t *testing.T) {
	lr := NewLessor()
	err := lr.Bind(nopWriter{}, []byte("orphan"), 999)
	var nfe *NotFoundError
	if !errors.As(err, &nfe) || nfe.ID != 999 {
		t.Fatalf("Bind to unknown lease error = %v, want *NotFoundError{ID: 999}", err)
	}
	if got := lr.LeaseOf([]byte("orphan")); got != NoLease {
		t.Errorf("LeaseOf after refused Bind = %d, want NoLease", got)
	}
	if lr.Count() != 0 {
		t.Errorf("Count() = %d after attach to unknown lease, want 0", lr.Count())
	}
}

func TestAttachKeyDuplicateKeyIdempotent(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("dup"))
	mustBind(t, lr, info.ID, []byte("dup"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 1 {
		t.Errorf("Keys count = %d after duplicate attach, want 1", len(found.Keys))
	}
}

func TestDetachKeyRemovesKey(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("key1"))
	mustBind(t, lr, info.ID, []byte("key2"))
	mustUnbind(t, lr, []byte("key1"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 1 {
		t.Fatalf("Keys count = %d after detach, want 1", len(found.Keys))
	}
	if string(found.Keys[0]) != "key2" {
		t.Errorf("remaining key = %q, want %q", string(found.Keys[0]), "key2")
	}
}

func TestUnbindUnboundKeyIsNoOp(t *testing.T) {
	lr := NewLessor()
	mustUnbind(t, lr, []byte("ghost"))
	if lr.Count() != 0 {
		t.Errorf("Count() = %d after unbinding an unbound key, want 0", lr.Count())
	}
}

func TestDetachKeyNonexistentKeyIsNoOp(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("exists"))
	mustUnbind(t, lr, []byte("does-not-exist"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 1 {
		t.Errorf("Keys count = %d, want 1", len(found.Keys))
	}
}

func TestExpiredLeaseIDsWithNoExpired(t *testing.T) {
	lr := NewLessor()
	_, _ = lr.Grant(nopWriter{}, 60)
	_, _ = lr.Grant(nopWriter{}, 120)
	expired := lr.ExpiredLeaseIDs()
	if len(expired) != 0 {
		t.Errorf("ExpiredLeaseIDs() returned %d IDs, want 0", len(expired))
	}
}

func TestExpiredLeaseIDsWithExpired(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	info, _ := lr.Grant(nopWriter{}, 1)
	fake.Advance(1100 * time.Millisecond)
	expired := lr.ExpiredLeaseIDs()
	if len(expired) != 1 {
		t.Fatalf("ExpiredLeaseIDs() returned %d IDs, want 1", len(expired))
	}
	if expired[0] != info.ID {
		t.Errorf("ExpiredLeaseIDs()[0] = %d, want %d", expired[0], info.ID)
	}
}

func TestExpiredLeaseIDsMixedExpiredAndActive(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	expiredInfo, _ := lr.Grant(nopWriter{}, 1)
	_, _ = lr.Grant(nopWriter{}, 600)
	fake.Advance(1100 * time.Millisecond)

	expired := lr.ExpiredLeaseIDs()
	if len(expired) != 1 {
		t.Fatalf("ExpiredLeaseIDs() returned %d IDs, want 1", len(expired))
	}
	if expired[0] != expiredInfo.ID {
		t.Errorf("ExpiredLeaseIDs()[0] = %d, want %d", expired[0], expiredInfo.ID)
	}
}

func TestCountReflectsGrants(t *testing.T) {
	lr := NewLessor()
	if lr.Count() != 0 {
		t.Errorf("Count() = %d, want 0", lr.Count())
	}
	_, _ = lr.Grant(nopWriter{}, 10)
	if lr.Count() != 1 {
		t.Errorf("Count() = %d after 1 grant, want 1", lr.Count())
	}
	_, _ = lr.Grant(nopWriter{}, 10)
	if lr.Count() != 2 {
		t.Errorf("Count() = %d after 2 grants, want 2", lr.Count())
	}
}

func TestCountReflectsRevokes(t *testing.T) {
	lr := NewLessor()
	info1, _ := lr.Grant(nopWriter{}, 60)
	info2, _ := lr.Grant(nopWriter{}, 60)
	if lr.Count() != 2 {
		t.Errorf("Count() = %d, want 2", lr.Count())
	}
	_, _ = lr.Revoke(nopWriter{}, info1.ID)
	if lr.Count() != 1 {
		t.Errorf("Count() after 1 revoke = %d, want 1", lr.Count())
	}
	_, _ = lr.Revoke(nopWriter{}, info2.ID)
	if lr.Count() != 0 {
		t.Errorf("Count() after 2 revokes = %d, want 0", lr.Count())
	}
}

func TestClosePreventsGrant(t *testing.T) {
	lr := NewLessor()
	lr.Close()
	_, err := lr.Grant(nopWriter{}, 10)
	if err == nil {
		t.Fatal("Grant after Close expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}

func TestClosePreventsRevoke(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	lr.Close()
	_, err := lr.Revoke(nopWriter{}, info.ID)
	if err == nil {
		t.Fatal("Revoke after Close expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}

func TestClosePreventsKeepAlive(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	lr.Close()
	_, err := lr.KeepAlive(info.ID)
	if err == nil {
		t.Fatal("KeepAlive after Close expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}

func TestLeaseInfoRemainingTTLApproximate(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	if info.RemainingTTL < 58 || info.RemainingTTL > 60 {
		t.Errorf("RemainingTTL = %d, want between 58 and 60", info.RemainingTTL)
	}
}

func TestLeaseInfoKeysInitiallyEmpty(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	if len(info.Keys) != 0 {
		t.Errorf("new lease Keys count = %d, want 0", len(info.Keys))
	}
}

func TestRevokeNoKeysReturnsEmptySlice(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	keys, err := lr.Revoke(nopWriter{}, info.ID)
	if err != nil {
		t.Fatalf("Revoke returned unexpected error: %v", err)
	}
	if keys == nil {
		t.Fatal("Revoke returned nil keys slice, want empty slice")
	}
	if len(keys) != 0 {
		t.Errorf("Revoke keys count = %d, want 0", len(keys))
	}
}

func TestExpiredLeaseIDsEmptyLessor(t *testing.T) {
	lr := NewLessor()
	expired := lr.ExpiredLeaseIDs()
	if len(expired) != 0 {
		t.Errorf("ExpiredLeaseIDs() on empty lessor returned %d IDs, want 0", len(expired))
	}
}

func TestLookupShowsAttachedKeys(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("k1"))
	mustBind(t, lr, info.ID, []byte("k2"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 2 {
		t.Errorf("Lookup Keys count = %d, want 2", len(found.Keys))
	}
}

func TestDetachAllKeysThenLookup(t *testing.T) {
	lr := NewLessor()
	info, _ := lr.Grant(nopWriter{}, 60)
	mustBind(t, lr, info.ID, []byte("a"))
	mustUnbind(t, lr, []byte("a"))

	found := lr.Lookup(info.ID)
	if found == nil {
		t.Fatal("Lookup returned nil")
	}
	if len(found.Keys) != 0 {
		t.Errorf("Keys count after detaching all = %d, want 0", len(found.Keys))
	}
}

func TestLeasesAfterRevoke(t *testing.T) {
	lr := NewLessor()
	info1, _ := lr.Grant(nopWriter{}, 60)
	_, _ = lr.Grant(nopWriter{}, 60)
	_, _ = lr.Revoke(nopWriter{}, info1.ID)

	leases := lr.Leases()
	if len(leases) != 1 {
		t.Errorf("Leases() after revoke returned %d, want 1", len(leases))
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	lr := NewLessor()
	lr.Close()
	lr.Close() // should not panic
	_, err := lr.Grant(nopWriter{}, 10)
	if err == nil {
		t.Fatal("Grant after double Close expected error, got nil")
	}
	if _, ok := err.(*LessorClosedError); !ok {
		t.Errorf("error type = %T, want *LessorClosedError", err)
	}
}
