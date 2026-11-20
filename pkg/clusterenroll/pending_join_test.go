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

package clusterenroll

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestHandleJoinRequest_NoToken_Queues confirms a tokenless CSR is queued
// for approval rather than refused, and is visible to
// ListPendingJoinRequests.
func TestHandleJoinRequest_NoToken_Queues(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := "spiffe://" + testTrustDomain + "/node/pending-1"
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: nodeURI}, nil)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest with no token = error %q, want none", resp.Error)
	}
	if !resp.Pending || resp.RequestID == "" {
		t.Fatalf("HandleJoinRequest with no token = %+v, want Pending=true with a RequestID", resp)
	}

	pending, err := ListPendingJoinRequests(context.Background(), store)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != resp.RequestID {
		t.Fatalf("ListPendingJoinRequests = %+v, want exactly one entry with ID %q", pending, resp.RequestID)
	}

	// Retrying the identical CSR must not create a second queue entry.
	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: nodeURI}, nil)
	if resp2.RequestID != resp.RequestID {
		t.Fatalf("retry got a different RequestID: %q, want %q", resp2.RequestID, resp.RequestID)
	}
	pending, err = ListPendingJoinRequests(context.Background(), store)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests after retry: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("ListPendingJoinRequests after retry = %d entries, want exactly 1", len(pending))
	}
}

// TestApproveJoinRequest_ThenPoll_IssuesLeaf confirms an approved request's
// next poll signs and returns a leaf chained to the cluster CA, bound to
// the operator-chosen identity rather than the joiner's self-reported
// NodeURI, and that a repeated poll after delivery returns the identical
// cached certificate rather than re-signing.
func TestApproveJoinRequest_ThenPoll_IssuesLeaf(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	claimedNodeURI := "spiffe://attacker.example/node/spoofed"
	nodeKey, csrDER := buildMatchingJoinCSR(t, claimedNodeURI)

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: claimedNodeURI}, nil)
	if !resp.Pending {
		t.Fatalf("HandleJoinRequest with no token = %+v, want Pending", resp)
	}

	rec, err := ApproveJoinRequest(context.Background(), store, testTrustDomain, resp.RequestID, "approved-node", "operator-1")
	if err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	wantURI := "spiffe://" + testTrustDomain + "/node/approved-node"
	if rec.BoundNodeURI != wantURI {
		t.Fatalf("ApproveJoinRequest bound %q, want %q", rec.BoundNodeURI, wantURI)
	}

	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: claimedNodeURI}, nil)
	if resp2.Error != "" {
		t.Fatalf("poll after approval = error %q, want a signed leaf", resp2.Error)
	}
	if len(resp2.CertPEM) == 0 {
		t.Fatal("poll after approval returned no certificate")
	}
	assertLeafChainsToCA(t, provider, resp2.CertPEM, nodeKey)

	// Re-polling (the first delivery never reaching the joiner) must
	// return the SAME cached certificate, not fail or re-sign.
	resp3 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: claimedNodeURI}, nil)
	if resp3.Error != "" {
		t.Fatalf("re-poll after delivery = error %q, want the cached certificate", resp3.Error)
	}
	if string(resp3.CertPEM) != string(resp2.CertPEM) {
		t.Fatal("re-poll after delivery returned a different certificate than the cached one")
	}
}

// TestRejectJoinRequest_ThenPoll_Refused confirms a rejected request's next
// poll is refused, terminally, and the request no longer appears in the
// pending list.
func TestRejectJoinRequest_ThenPoll_Refused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := "spiffe://" + testTrustDomain + "/node/rejected-1"
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: nodeURI}, nil)
	if !resp.Pending {
		t.Fatalf("HandleJoinRequest with no token = %+v, want Pending", resp)
	}

	if _, err := RejectJoinRequest(context.Background(), store, resp.RequestID, "not recognized", "operator-1"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}

	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: nodeURI}, nil)
	if resp2.Error == "" || resp2.Pending {
		t.Fatalf("poll after rejection = %+v, want a terminal refusal", resp2)
	}

	pending, err := ListPendingJoinRequests(context.Background(), store)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("ListPendingJoinRequests after rejection = %+v, want none", pending)
	}
}

// TestApproveJoinRequest_ExistingMember confirms approving into an
// identity that is already admitted is refused the same way MintJoinToken
// refuses minting for one.
func TestApproveJoinRequest_ExistingMember(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	admittedURI := "spiffe://" + testTrustDomain + "/node/already-in"
	if err := MarkJoinIdentityAdmitted(context.Background(), store, admittedURI, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}

	_, csrDER := generateTestCSR(t)
	req := &JoinRequest{CSRDER: csrDER}
	rec, err := SubmitPendingJoinRequest(context.Background(), store, req, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	_, err = ApproveJoinRequest(context.Background(), store, testTrustDomain, rec.ID, "already-in", "operator-1")
	var existing *ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("ApproveJoinRequest into an admitted identity = %v, want *ExistingMemberError", err)
	}
}

// TestApproveJoinRequest_NotFound and TestRejectJoinRequest_NotFound confirm
// a decision on an unknown, expired, or already-decided ID is refused
// rather than silently creating or re-deciding anything.
func TestApproveJoinRequest_NotFound(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := ApproveJoinRequest(context.Background(), store, testTrustDomain, "no-such-id", "some-node", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ApproveJoinRequest on unknown ID = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

func TestRejectJoinRequest_NotFound(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := RejectJoinRequest(context.Background(), store, "no-such-id", "reason", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("RejectJoinRequest on unknown ID = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

// TestApproveJoinRequest_AlreadyDecided confirms a SECOND decision on an
// already-approved (or rejected) request is refused rather than silently
// overwriting the first one.
func TestApproveJoinRequest_AlreadyDecided(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(context.Background(), store, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}
	if _, err := ApproveJoinRequest(context.Background(), store, testTrustDomain, rec.ID, "node-a", "operator-1"); err != nil {
		t.Fatalf("first ApproveJoinRequest: %v", err)
	}

	_, err = ApproveJoinRequest(context.Background(), store, testTrustDomain, rec.ID, "node-b", "operator-2")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("second ApproveJoinRequest on an already-decided entry = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

// TestSubmitPendingJoinRequest_ExpiryReclaimed confirms an undecided entry
// past its TTL is treated as absent: a retry starts a fresh entry rather
// than being blocked by the stale one forever.
// expirePendingJoin rewrites the stored entry id with an ExpiresAt in the
// past, so a test sees an expired entry without waiting out a TTL on the
// wall clock. Before the rewrite it asserts that the deadline
// SubmitPendingJoinRequest generated is exactly ttl past SubmittedAt, so
// overwriting ExpiresAt cannot hide a wrong generated deadline. It returns
// the rewritten record.
func expirePendingJoin(t *testing.T, store kvstore.AtomicKVStore, id string, ttl time.Duration) *pendingJoinRecord {
	t.Helper()
	ctx := context.Background()
	data, err := store.Get(ctx, pendingJoinKey(id))
	if err != nil || data == nil {
		t.Fatalf("Get pending entry %q: data=%v err=%v", id, data, err)
	}
	rec, err := unmarshalPendingJoin(data)
	if err != nil {
		t.Fatalf("unmarshalPendingJoin: %v", err)
	}
	if got := rec.ExpiresAt.Sub(rec.SubmittedAt); got != ttl {
		t.Fatalf("generated deadline ExpiresAt-SubmittedAt = %v, want the requested TTL %v", got, ttl)
	}
	rec.ExpiresAt = rec.SubmittedAt.Add(-time.Second)
	out, err := marshalPendingJoin(rec)
	if err != nil {
		t.Fatalf("marshalPendingJoin: %v", err)
	}
	if err := store.Put(ctx, pendingJoinKey(id), out); err != nil {
		t.Fatalf("Put expired pending entry: %v", err)
	}
	return rec
}

func TestSubmitPendingJoinRequest_ExpiryReclaimed(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	req := &JoinRequest{CSRDER: csrDER}

	submitted, err := SubmitPendingJoinRequest(context.Background(), store, req, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}
	first := expirePendingJoin(t, store, submitted.ID, time.Hour)

	second, err := SubmitPendingJoinRequest(context.Background(), store, req, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest (reclaim): %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("reclaimed entry ID = %q, want the same ID %q", second.ID, first.ID)
	}
	if !second.ExpiresAt.After(first.ExpiresAt) {
		t.Fatalf("reclaimed entry ExpiresAt = %v, want later than the stale one's %v", second.ExpiresAt, first.ExpiresAt)
	}
}

// TestSubmitPendingJoinRequest_QueueFull confirms a brand-new tokenless
// submission is refused once the queue is at MaxPendingJoinRequests,
// without needing to actually generate that many real CSRs: synthetic
// records are written directly, since SubmitPendingJoinRequest's cap check
// only counts keys under the prefix.
func TestSubmitPendingJoinRequest_QueueFull(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < MaxPendingJoinRequests; i++ {
		rec := &pendingJoinRecord{
			ID:          fmt.Sprintf("synthetic-%d", i),
			Status:      pendingStatusPending,
			SubmittedAt: now,
			ExpiresAt:   now.Add(time.Hour),
		}
		data, err := marshalPendingJoin(rec)
		if err != nil {
			t.Fatalf("marshalPendingJoin: %v", err)
		}
		if err := store.Put(ctx, pendingJoinKey(rec.ID), data); err != nil {
			t.Fatalf("Put synthetic record %d: %v", i, err)
		}
	}

	_, csrDER := generateTestCSR(t)
	_, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: csrDER}, 0)
	var full *PendingJoinQueueFullError
	if !errors.As(err, &full) {
		t.Fatalf("SubmitPendingJoinRequest at capacity = %v, want *PendingJoinQueueFullError", err)
	}
}

// TestListPendingJoinRequests_SkipsExpiredAndDecided confirms the operator
// view excludes expired-undecided entries and decided ones, surfacing only
// what is actually awaiting a decision right now.
func TestListPendingJoinRequests_SkipsExpiredAndDecided(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	ctx := context.Background()

	_, liveCSR := generateTestCSR(t)
	live, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: liveCSR}, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest (live): %v", err)
	}

	_, expiredCSR := generateTestCSR(t)
	expired, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: expiredCSR}, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest (expired): %v", err)
	}
	expirePendingJoin(t, store, expired.ID, time.Hour)

	_, decidedCSR := generateTestCSR(t)
	decided, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: decidedCSR}, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest (decided): %v", err)
	}
	if _, err := RejectJoinRequest(ctx, store, decided.ID, "no", "operator-1"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}

	got, err := ListPendingJoinRequests(ctx, store)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("ListPendingJoinRequests = %+v, want exactly the live entry %q", got, live.ID)
	}
}

// ---------------------------------------------------------------------------
// unmarshalPendingJoin and the pending-queue error types
// ---------------------------------------------------------------------------

func TestUnmarshalPendingJoin_InvalidJSONRefused(t *testing.T) {
	_, err := unmarshalPendingJoin([]byte("not valid json"))
	if err == nil {
		t.Fatal("unmarshalPendingJoin with malformed JSON = nil error, want a decode error")
	}
}

func TestPendingJoinStoreError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	err := &PendingJoinStoreError{Op: "get", Detail: "k", Err: cause}
	want := `clusterenroll: pending join request store get failed (k): boom`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err.Unwrap(), cause) {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), cause)
	}
}

func TestPendingJoinQueueFullError_Error(t *testing.T) {
	err := &PendingJoinQueueFullError{}
	want := "clusterenroll: pending join request queue is full"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestPendingJoinRequestNotFoundError_Error(t *testing.T) {
	err := &PendingJoinRequestNotFoundError{ID: "abc"}
	want := "clusterenroll: no pending join request abc awaiting a decision"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// SubmitPendingJoinRequest: store-fault and race-loss branches
// ---------------------------------------------------------------------------

func TestSubmitPendingJoinRequest_GetErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failGetOnPrefix: pendingJoinPrefix}
	_, csrDER := generateTestCSR(t)

	_, err := SubmitPendingJoinRequest(context.Background(), store, &JoinRequest{CSRDER: csrDER}, 0)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "get" {
		t.Fatalf("SubmitPendingJoinRequest with a failing Get = %v, want *PendingJoinStoreError{Op: \"get\"}", err)
	}
}

func TestSubmitPendingJoinRequest_CorruptExistingRecordSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	id := csrDigestHex(csrDER)
	if err := store.Put(ctx, pendingJoinKey(id), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: csrDER}, 0)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "unmarshal" {
		t.Fatalf("SubmitPendingJoinRequest over a corrupt existing record = %v, want *PendingJoinStoreError{Op: \"unmarshal\"}", err)
	}
}

func TestSubmitPendingJoinRequest_ReclaimCompareAndSwapErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	req := &JoinRequest{CSRDER: csrDER}

	if _, err := SubmitPendingJoinRequest(ctx, real, req, time.Hour); err != nil {
		t.Fatalf("SubmitPendingJoinRequest (seed): %v", err)
	}

	id := csrDigestHex(csrDER)
	expirePendingJoin(t, real, id, time.Hour)
	faulty := &faultyJoinStore{AtomicKVStore: real, failCompareAndSwapOnPrefix: pendingJoinKey(id)}
	_, err := SubmitPendingJoinRequest(ctx, faulty, req, time.Hour)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "compareandswap" {
		t.Fatalf("SubmitPendingJoinRequest reclaim with a failing CompareAndSwap = %v, want *PendingJoinStoreError{Op: \"compareandswap\"}", err)
	}
}

func TestSubmitPendingJoinRequest_ReclaimLostRaceReturnsWhicheverWon(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	req := &JoinRequest{CSRDER: csrDER}

	if _, err := SubmitPendingJoinRequest(ctx, real, req, time.Hour); err != nil {
		t.Fatalf("SubmitPendingJoinRequest (seed): %v", err)
	}

	id := csrDigestHex(csrDER)
	stale := expirePendingJoin(t, real, id, time.Hour)
	faulty := &faultyJoinStore{AtomicKVStore: real, forceCompareAndSwapFalseOnPrefix: pendingJoinKey(id)}
	got, err := SubmitPendingJoinRequest(ctx, faulty, req, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest reclaim with a lost race: %v", err)
	}
	if got.ID != stale.ID || !got.ExpiresAt.Equal(stale.ExpiresAt) {
		t.Fatalf("SubmitPendingJoinRequest reclaim with a lost race = %+v, want the untouched stale record %+v", got, stale)
	}
}

func TestSubmitPendingJoinRequest_ListErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failListOnPrefix: pendingJoinPrefix}
	_, csrDER := generateTestCSR(t)

	_, err := SubmitPendingJoinRequest(context.Background(), store, &JoinRequest{CSRDER: csrDER}, 0)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "list" {
		t.Fatalf("SubmitPendingJoinRequest with a failing List = %v, want *PendingJoinStoreError{Op: \"list\"}", err)
	}
}

func TestSubmitPendingJoinRequest_PutIfAbsentErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: pendingJoinPrefix}
	_, csrDER := generateTestCSR(t)

	_, err := SubmitPendingJoinRequest(context.Background(), store, &JoinRequest{CSRDER: csrDER}, 0)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "putifabsent" {
		t.Fatalf("SubmitPendingJoinRequest with a failing PutIfAbsent = %v, want *PendingJoinStoreError{Op: \"putifabsent\"}", err)
	}
}

func TestSubmitPendingJoinRequest_NewEntryLostRaceWithNoWinnerToRead(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), forcePutIfAbsentFalseOnPrefix: pendingJoinPrefix}
	_, csrDER := generateTestCSR(t)

	rec, err := SubmitPendingJoinRequest(context.Background(), store, &JoinRequest{CSRDER: csrDER}, 0)
	if err == nil || rec != nil {
		t.Fatalf("SubmitPendingJoinRequest losing the create race with nothing actually written by the winner = (%v, %v), want a decode error and a nil record", rec, err)
	}
}

// ---------------------------------------------------------------------------
// ListPendingJoinRequests: store faults and unparsable records
// ---------------------------------------------------------------------------

func TestListPendingJoinRequests_ScanErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failScanOnPrefix: pendingJoinPrefix}

	_, err := ListPendingJoinRequests(context.Background(), store)
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "scan" {
		t.Fatalf("ListPendingJoinRequests with a failing Scan = %v, want *PendingJoinStoreError{Op: \"scan\"}", err)
	}
}

func TestListPendingJoinRequests_SkipsUnparsableRecords(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	if err := store.Put(ctx, pendingJoinKey("garbage"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, liveCSR := generateTestCSR(t)
	live, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: liveCSR}, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	got, err := ListPendingJoinRequests(ctx, store)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("ListPendingJoinRequests with an unparsable record present = %+v, want only the live entry %q", got, live.ID)
	}
}

// ---------------------------------------------------------------------------
// ApproveJoinRequest and RejectJoinRequest: loadDecidable and store faults
// ---------------------------------------------------------------------------

func TestApproveJoinRequest_ExpiredWhilePendingTreatedAsNotFound(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, store, &JoinRequest{CSRDER: csrDER}, time.Hour)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}
	expirePendingJoin(t, store, rec.ID, time.Hour)

	_, err = ApproveJoinRequest(ctx, store, testTrustDomain, rec.ID, "some-node", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ApproveJoinRequest on an expired-but-still-pending entry = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

func TestApproveJoinRequest_CorruptRecordTreatedAsNotFound(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	id := "corrupt-entry"
	if err := store.Put(ctx, pendingJoinKey(id), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := ApproveJoinRequest(ctx, store, testTrustDomain, id, "some-node", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ApproveJoinRequest on a corrupt record = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

func TestApproveJoinRequest_InvalidIdentityRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := ApproveJoinRequest(context.Background(), store, testTrustDomain, "some-id", "", "operator-1")
	var invalid *InvalidJoinIdentityError
	if !errors.As(err, &invalid) {
		t.Fatalf("ApproveJoinRequest with an empty node ID = %v, want *InvalidJoinIdentityError", err)
	}
}

func TestApproveJoinRequest_ClassifyIdentityStoreErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failGetOnPrefix: joinIdentityPrefix}
	_, err = ApproveJoinRequest(ctx, faulty, testTrustDomain, rec.ID, "some-node", "operator-1")
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ApproveJoinRequest with classifyIdentity's Get failing = %v, want *JoinTokenStoreError", err)
	}
}

func TestApproveJoinRequest_PendingRecordGetErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failGetOnPrefix: pendingJoinPrefix}
	_, err = ApproveJoinRequest(ctx, faulty, testTrustDomain, rec.ID, "some-node", "operator-1")
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "get" {
		t.Fatalf("ApproveJoinRequest with a failing pending-record Get = %v, want *PendingJoinStoreError{Op: \"get\"}", err)
	}
}

func TestApproveJoinRequest_CompareAndSwapErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failCompareAndSwapOnPrefix: pendingJoinKey(rec.ID)}
	_, err = ApproveJoinRequest(ctx, faulty, testTrustDomain, rec.ID, "some-node", "operator-1")
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "compareandswap" {
		t.Fatalf("ApproveJoinRequest with a failing CompareAndSwap = %v, want *PendingJoinStoreError{Op: \"compareandswap\"}", err)
	}
}

func TestApproveJoinRequest_LostRaceTreatedAsNotFound(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, forceCompareAndSwapFalseOnPrefix: pendingJoinKey(rec.ID)}
	_, err = ApproveJoinRequest(ctx, faulty, testTrustDomain, rec.ID, "some-node", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ApproveJoinRequest that loses the decision race = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

func TestRejectJoinRequest_PendingRecordGetErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failGetOnPrefix: pendingJoinPrefix}
	_, err = RejectJoinRequest(ctx, faulty, rec.ID, "no", "operator-1")
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "get" {
		t.Fatalf("RejectJoinRequest with a failing pending-record Get = %v, want *PendingJoinStoreError{Op: \"get\"}", err)
	}
}

func TestRejectJoinRequest_CompareAndSwapErrorSurfacesTyped(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failCompareAndSwapOnPrefix: pendingJoinKey(rec.ID)}
	_, err = RejectJoinRequest(ctx, faulty, rec.ID, "no", "operator-1")
	var storeErr *PendingJoinStoreError
	if !errors.As(err, &storeErr) || storeErr.Op != "compareandswap" {
		t.Fatalf("RejectJoinRequest with a failing CompareAndSwap = %v, want *PendingJoinStoreError{Op: \"compareandswap\"}", err)
	}
}

func TestRejectJoinRequest_LostRaceTreatedAsNotFound(t *testing.T) {
	ctx := context.Background()
	real := kvstore.NewMemoryKVStore()
	_, csrDER := generateTestCSR(t)
	rec, err := SubmitPendingJoinRequest(ctx, real, &JoinRequest{CSRDER: csrDER}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, forceCompareAndSwapFalseOnPrefix: pendingJoinKey(rec.ID)}
	_, err = RejectJoinRequest(ctx, faulty, rec.ID, "no", "operator-1")
	var notFound *PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("RejectJoinRequest that loses the decision race = %v, want *PendingJoinRequestNotFoundError", err)
	}
}

// ---------------------------------------------------------------------------
// handleTokenlessJoin: the Submit failure branch
// ---------------------------------------------------------------------------

func TestHandleJoinRequest_NoToken_QueueFullRefusesGenerically(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < MaxPendingJoinRequests; i++ {
		rec := &pendingJoinRecord{
			ID:          fmt.Sprintf("synthetic-%d", i),
			Status:      pendingStatusPending,
			SubmittedAt: now,
			ExpiresAt:   now.Add(time.Hour),
		}
		data, err := marshalPendingJoin(rec)
		if err != nil {
			t.Fatalf("marshalPendingJoin: %v", err)
		}
		if err := store.Put(ctx, pendingJoinKey(rec.ID), data); err != nil {
			t.Fatalf("Put synthetic record %d: %v", i, err)
		}
	}

	nodeURI := "spiffe://" + testTrustDomain + "/node/queue-full"
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)
	resp := HandleJoinRequest(ctx, provider, store, testTrustDomain,
		&JoinRequest{CSRDER: csrDER, NodeURI: nodeURI}, nil)
	if resp.Error != "join request could not be processed" || resp.Pending {
		t.Fatalf("HandleJoinRequest with no token against a full queue = %+v, want the generic refusal", resp)
	}
}

// ---------------------------------------------------------------------------
// deliverApprovedJoin: the cache-on-failure and cache-on-success branches
// ---------------------------------------------------------------------------

func TestDeliverApprovedJoin_FailureWithNoCachedDeliveryReturnsFailure(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-fail-1"
	if err := MarkJoinIdentityAdmitted(ctx, store, boundNodeURI, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}

	record := &pendingJoinRecord{ID: "deliver-fail-1", BoundNodeURI: boundNodeURI, Status: pendingStatusApproved}
	data, err := marshalPendingJoin(record)
	if err != nil {
		t.Fatalf("marshalPendingJoin: %v", err)
	}
	if err := store.Put(ctx, pendingJoinKey(record.ID), data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	resp := deliverApprovedJoin(ctx, provider, store, record, csr, nil)
	if resp.Error == "" {
		t.Fatalf("deliverApprovedJoin against an already-admitted identity = %+v, want a failure", resp)
	}
}

func TestDeliverApprovedJoin_FailureAfterPriorDeliveryReturnsCachedCert(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-fail-2"
	if err := MarkJoinIdentityAdmitted(ctx, store, boundNodeURI, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}

	record := &pendingJoinRecord{ID: "deliver-fail-2", BoundNodeURI: boundNodeURI, Status: pendingStatusDelivered, CertPEM: []byte("cached-cert")}
	data, err := marshalPendingJoin(record)
	if err != nil {
		t.Fatalf("marshalPendingJoin: %v", err)
	}
	if err := store.Put(ctx, pendingJoinKey(record.ID), data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	resp := deliverApprovedJoin(ctx, provider, store, record, csr, nil)
	if resp.Error != "" || string(resp.CertPEM) != "cached-cert" {
		t.Fatalf("deliverApprovedJoin re-polling a request already delivered by a concurrent poll = %+v, want the cached certificate", resp)
	}
}

func TestDeliverApprovedJoin_PostSuccessCacheGetErrorStillReturnsTheSignedLeaf(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	real := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-cache-get-err"
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	record := &pendingJoinRecord{ID: "deliver-cache-get-err", BoundNodeURI: boundNodeURI, Status: pendingStatusApproved}

	faulty := &faultyJoinStore{AtomicKVStore: real, failGetOnPrefix: pendingJoinKey(record.ID)}
	resp := deliverApprovedJoin(ctx, provider, faulty, record, csr, nil)
	if resp.Error != "" || len(resp.CertPEM) == 0 {
		t.Fatalf("deliverApprovedJoin whose post-success cache Get fails = %+v, want the signed leaf anyway", resp)
	}
}

func TestDeliverApprovedJoin_PostSuccessCacheUnmarshalErrorStillReturnsTheSignedLeaf(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-cache-unmarshal-err"
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	record := &pendingJoinRecord{ID: "deliver-cache-unmarshal-err", BoundNodeURI: boundNodeURI, Status: pendingStatusApproved}
	if err := store.Put(ctx, pendingJoinKey(record.ID), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	resp := deliverApprovedJoin(ctx, provider, store, record, csr, nil)
	if resp.Error != "" || len(resp.CertPEM) == 0 {
		t.Fatalf("deliverApprovedJoin whose post-success cache record is unparsable = %+v, want the signed leaf anyway", resp)
	}
}

func TestDeliverApprovedJoin_PostSuccessCacheCompareAndSwapErrorStillReturnsTheSignedLeaf(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	real := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-cache-cas-err"
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	record := &pendingJoinRecord{ID: "deliver-cache-cas-err", BoundNodeURI: boundNodeURI, Status: pendingStatusApproved}
	data, err := marshalPendingJoin(record)
	if err != nil {
		t.Fatalf("marshalPendingJoin: %v", err)
	}
	if err := real.Put(ctx, pendingJoinKey(record.ID), data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	faulty := &faultyJoinStore{AtomicKVStore: real, failCompareAndSwapOnPrefix: pendingJoinKey(record.ID)}
	resp := deliverApprovedJoin(ctx, provider, faulty, record, csr, nil)
	if resp.Error != "" || len(resp.CertPEM) == 0 {
		t.Fatalf("deliverApprovedJoin whose post-success cache CompareAndSwap fails = %+v, want the signed leaf anyway", resp)
	}
}

func TestDeliverApprovedJoin_AdvertisesTheJoinerOnSuccess(t *testing.T) {
	ctx := context.Background()
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/deliver-advertise"
	_, csrDER := buildMatchingJoinCSR(t, boundNodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	record := &pendingJoinRecord{ID: "deliver-advertise", BoundNodeURI: boundNodeURI, ReplicaID: 7, Features: []string{"vector"}}
	advertiser := &fakeFeatureAdvertiser{}

	resp := deliverApprovedJoin(ctx, provider, store, record, csr, advertiser)
	if resp.Error != "" || len(resp.CertPEM) == 0 {
		t.Fatalf("deliverApprovedJoin with an advertiser configured = %+v, want a signed leaf", resp)
	}
	if advertiser.callCount() != 1 || advertiser.replicaID != 7 {
		t.Fatalf("advertiser calls = %d replicaID = %d, want exactly one call for replica 7", advertiser.callCount(), advertiser.replicaID)
	}
	if len(advertiser.features) != 1 || advertiser.features[0] != "vector" {
		t.Fatalf("advertiser features = %v, want [vector]", advertiser.features)
	}
}
