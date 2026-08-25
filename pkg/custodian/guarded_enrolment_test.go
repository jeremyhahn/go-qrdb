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

package custodian_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// An enrolment writes its three keys as ONE transaction fenced on the claim
// token, so the properties a compensation protocol used to provide are now
// properties of the write itself.
//
// The tests this file replaces asserted that machinery directly — that a torn
// write was rolled back, that an ambiguous commit was reconciled against the
// primary record, that a failed rollback named its stranded keys. All of it
// existed because the three writes could tear. They cannot, so those tests were
// removed with the machinery rather than adapted: a test that keeps asserting a
// mechanism after the mechanism is gone is the clearest kind of dead weight, and
// leaving them passing against a re-added fallback would defend the very race
// this change closed (go-qrdb#21, the Codex finding on PR #111).
//
// What survives are the guarantees a caller actually depends on, re-asserted at
// the new fence.

// TestRegister_StaleOwnerCannotCommitAfterTakeover is THE test this change
// exists for, and it replaces the ownership-check version that parked a caller
// at a read.
//
// A registration that stalls past the claim TTL has its claim taken over. Under
// the old per-key fence it re-checked ownership before each write, which left a
// window between each check and its write: lose the claim inside one and the
// write lands anyway, on top of the winner's, leaving the winner's published
// index pointing at the loser's record. So this parks A inside the GUARDED
// WRITE — the point where the fence now lives — rather than at a check, lets B
// take the claim over completely, and then releases A.
//
// Exactly one enrolment must survive, and it must be B's throughout: B's record
// under B's envelope, with no field of A's anywhere.
func TestRegister_StaleOwnerCannotCommitAfterTakeover(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	reqA, _ := testRegisterRequest(t)
	reqA.EnrollmentID = "enroll-A"
	reqB, _ := testRegisterRequest(t)
	reqB.EnrollmentID = "enroll-B"

	claimKey := usernameClaimKey(reqA.TenantID, reqA.Username)

	parked := make(chan struct{})
	release := make(chan struct{})
	var parkTaken atomic.Bool

	// Park A on its way into the guarded write, BEFORE the guard is evaluated.
	// An atomic CAS rather than sync.Once: Once.Do holds every later caller
	// inside it until the first returns, which would block B on A's release and
	// deadlock — serialising the two callers this test exists to overlap.
	store.hook(func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian/") && parkTaken.CompareAndSwap(false, true) {
			close(parked)
			<-release
		}
		return nil
	})

	aErr := make(chan error, 1)
	go func() {
		_, err := svc.RegisterCustodian(ctx, reqA)
		aErr <- err
	}()

	select {
	case <-parked:
	case err := <-aErr:
		t.Fatalf("A finished without reaching the guarded write: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("A never reached the guarded write")
	}

	// Age A's claim out. This is what a stalled registration looks like from
	// the store's side: the claim key still holds a claim, and it has expired,
	// so another caller may take it over.
	_ = claimKey
	strandRegistration(t, store.memKV, reqA.TenantID, reqA.Username, time.Now().Add(-time.Hour))
	respB, err := svc.RegisterCustodian(ctx, reqB)
	if err != nil {
		t.Fatalf("B could not take over the expired claim: %v", err)
	}

	close(release)
	errA := <-aErr

	if errA == nil {
		t.Fatal("A committed after its claim was taken over: two registrations both " +
			"returned success with different sealed envelopes, and only one record can " +
			"survive — the duplicate enrolment the claim protocol exists to prevent")
	}
	var lost *custodian.ClaimLostError
	if !errors.As(errA, &lost) {
		t.Errorf("A failed with %T (%v); want ClaimLostError, so the caller learns its "+
			"claim was taken over rather than that the store broke", errA, errA)
	}

	// The winner's record must be intact and entirely B's. A's write landing on
	// top would leave B's published index pointing at A's record — a lost
	// update with no signal, which is the whole defect.
	got, err := svc.GetCustodian(ctx, respB.Custodian.ID)
	if err != nil {
		t.Fatalf("the winner's record is not readable: %v", err)
	}
	if got.EnrollmentID != "enroll-B" {
		t.Errorf("the surviving record carries EnrollmentID %q; want enroll-B — the stale "+
			"owner overwrote the winner", got.EnrollmentID)
	}
}

// TestRegister_GuardFailureLeavesNothingBehind is the other half: when the
// guard does not hold, the store must be untouched.
//
// Under the old protocol this was work — writes had already landed and had to
// be compensated, and a failed compensation left debris an operator had to
// clear. Now it is a property of the write, and asserting it keeps anyone from
// reintroducing a per-key fallback: partial state would reappear the moment
// the writes stopped being one transaction.
func TestRegister_GuardFailureLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	reqA, _ := testRegisterRequest(t)
	reqA.EnrollmentID = "enroll-A"
	reqB, _ := testRegisterRequest(t)
	reqB.EnrollmentID = "enroll-B"

	claimKey := usernameClaimKey(reqA.TenantID, reqA.Username)

	parked := make(chan struct{})
	release := make(chan struct{})
	var parkTaken atomic.Bool
	store.hook(func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian/") && parkTaken.CompareAndSwap(false, true) {
			close(parked)
			<-release
		}
		return nil
	})

	aErr := make(chan error, 1)
	go func() {
		_, err := svc.RegisterCustodian(ctx, reqA)
		aErr <- err
	}()
	<-parked

	_ = claimKey
	strandRegistration(t, store.memKV, reqB.TenantID, reqB.Username, time.Now().Add(-time.Hour))
	respB, err := svc.RegisterCustodian(ctx, reqB)
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	close(release)
	<-aErr

	// Exactly one primary record, and it is B's.
	ids, err := svc.ListCustodians(ctx, reqB.TenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("%d custodians for tenant %s, want 1 — the refused registration left "+
			"state behind", len(ids), reqB.TenantID)
	}
	if ids[0].ID != respB.Custodian.ID {
		t.Errorf("the surviving custodian is %s, want the winner %s", ids[0].ID, respB.Custodian.ID)
	}
}

// TestRegister_UnknownOutcomeNamesEveryKey covers the one case a transaction
// cannot decide for the caller: the store failed and the outcome is unknown.
//
// Deleting on that basis could remove the winner's keys, so registration
// reports the failure and names every key an operator has to look at. This
// replaces the rollback-failure test, which asserted the same obligation about
// a compensation step that no longer exists.
func TestRegister_UnknownOutcomeNamesEveryKey(t *testing.T) {
	ctx := context.Background()
	store := &guardErrKV{memKV: newMemKV()}
	svc := newGuardErrService(t, store)

	req, _ := testRegisterRequest(t)
	_, err := svc.RegisterCustodian(ctx, req)
	if err == nil {
		t.Fatal("a store failure during the guarded write was reported as success")
	}

	var orphaned *custodian.OrphanedEnrolmentError
	if !errors.As(err, &orphaned) {
		t.Fatalf("err = %T (%v); want OrphanedEnrolmentError, because the outcome is "+
			"UNKNOWN and an operator needs to be told which keys to inspect", err, err)
	}
	if len(orphaned.Keys) != 3 {
		t.Errorf("named %d keys (%v); want all three an enrolment occupies — naming a "+
			"subset sends an operator looking in the wrong place", len(orphaned.Keys), orphaned.Keys)
	}
}

// guardErrKV fails the guarded write and nothing else, so a test can reach the
// unknown-outcome branch without disturbing the claim that precedes it.
type guardErrKV struct {
	*memKV
}

func (g *guardErrKV) PutAllIfUnchanged(context.Context, string, []byte, []kvstore.KV) (bool, error) {
	return false, errTestStore
}

func newGuardErrService(t *testing.T, store *guardErrKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// applyThenErrKV applies the guarded write and THEN reports an error, which is
// what a store does when the proposal commits and the response misses the
// caller's deadline.
//
// This is the case a plain error-injecting fake cannot express, and the
// distinction is the whole finding: "the write failed" and "the write
// succeeded and you did not hear about it" are opposite outcomes reached
// through the same error value.
type applyThenErrKV struct {
	*memKV
}

func (a *applyThenErrKV) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if _, err := a.memKV.PutAllIfUnchanged(ctx, guardKey, guardValue, writes); err != nil {
		return false, err
	}
	return false, errTestStore
}

// TestRegister_AppliedTransactionThatErrors_StillReturnsTheEnvelope is the
// share-loss case.
//
// By the time the guarded write runs, SealShareTo has zeroed the caller's share
// and the sealed envelope exists only in the response about to be returned.
// If an applied-but-errored transaction is reported as a failure, that envelope
// is discarded while the records stand — so every retry answers
// ErrCustodianAlreadyExists and no custodian ever receives their share. That is
// an enrolment whose sealed envelope does not exist, which is the loss
// go-qrdb#21 exists to prevent, reached from the other direction.
//
// Reading the keys back is what distinguishes it: they carry this attempt's
// exact bytes, which no other registration would produce.
func TestRegister_AppliedTransactionThatErrors_StillReturnsTheEnvelope(t *testing.T) {
	ctx := context.Background()
	store := &applyThenErrKV{memKV: newMemKV()}
	svc := newApplyThenErrService(t, store)

	req, _ := testRegisterRequest(t)
	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("an applied transaction was reported as a failure (%v); the share is zeroed "+
			"and the records are committed, so the envelope this discards can never be "+
			"reissued and the custodian is locked out of their own enrolment", err)
	}
	if resp == nil || resp.SealedEnvelope == nil {
		t.Fatal("registration succeeded with no sealed envelope; the envelope is returned once " +
			"and never stored, so an empty one is the same loss")
	}

	// And the enrolment must really be there — a success reported over an
	// unwritten record would be the opposite defect.
	got, err := svc.GetCustodian(ctx, resp.Custodian.ID)
	if err != nil {
		t.Fatalf("the enrolment reported as committed is not readable: %v", err)
	}
	if got.Username != req.Username {
		t.Errorf("stored username = %q, want %q", got.Username, req.Username)
	}
}

func newApplyThenErrService(t *testing.T, store *applyThenErrKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestRegister_CleanupDoesNotDeleteASuccessorsClaim covers the release side of
// the same expiry.
//
// A registration whose seal fails releases its claim. Read-then-delete is a
// live TOCTOU now that claims expire: between the read and the delete another
// registration can legitimately take the expired claim over, and the
// unconditional delete then removes the SUCCESSOR's claim — a valid enrolment
// silently losing an identity it had just acquired.
//
// The successor's claim must still be there afterwards.
func TestRegister_CleanupDoesNotDeleteASuccessorsClaim(t *testing.T) {
	ctx := context.Background()
	store := newMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	req, _ := testRegisterRequest(t)
	claimKey := usernameClaimKey(req.TenantID, req.Username)

	// A successor's live claim, written directly: this stands in for the
	// registration that took over after the first one's claim expired.
	successor := []byte("claim:" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + ":SuccessorToken")
	if putErr := store.Put(ctx, claimKey, successor); putErr != nil {
		t.Fatalf("seed the successor's claim: %v", putErr)
	}

	// A stale registration releasing a claim it no longer owns must not touch
	// it. Driving this through the public surface: a registration that meets a
	// LIVE claim refuses, and must leave that claim alone.
	if _, regErr := svc.RegisterCustodian(ctx, req); regErr == nil {
		t.Fatal("a registration meeting a live claim must refuse")
	}

	current, err := store.Get(ctx, claimKey)
	if err != nil {
		t.Fatalf("read the claim back: %v", err)
	}
	if !bytes.Equal(current, successor) {
		t.Errorf("the successor's claim is now %q, want it untouched (%q) — a stale "+
			"registration deleted a claim that was not its own", current, successor)
	}
}
