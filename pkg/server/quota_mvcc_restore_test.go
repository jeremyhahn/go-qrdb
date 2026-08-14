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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// restorePricingMVCC is an inner MVCCKVServicer that returns a fixed restore
// plan and records how it was called, so the quota wrapper's price-then-apply
// sequence can be observed.
type restorePricingMVCC struct {
	*mvccRec
	plan       []service.RestoreKeyChange
	conflicts  []service.RestoreKeyChange
	calls      []bool // DryRun flag of each RestoreToRevision call, in order
	forceFlags []bool // Force flag of each RestoreToRevision call, in order
	applyErr   error
	priceErr   error
}

func newRestorePricingMVCC(plan []service.RestoreKeyChange) *restorePricingMVCC {
	return &restorePricingMVCC{mvccRec: newMVCCRec(), plan: plan}
}

func (m *restorePricingMVCC) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	m.calls = append(m.calls, req.DryRun)
	m.forceFlags = append(m.forceFlags, req.Force)
	if req.DryRun && m.priceErr != nil {
		return nil, m.priceErr
	}
	if !req.DryRun && m.applyErr != nil {
		return nil, m.applyErr
	}
	result := &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes:  m.plan,
		DryRun:   req.DryRun,
	}
	if !req.DryRun {
		result.Applied = len(m.plan) - len(m.conflicts)
		result.Conflicts = m.conflicts
		result.Conflicted = len(m.conflicts)
	}
	return result, nil
}

// TestQuotaMVCCService_RestoreAccountsWrittenBytes proves a restore is not a
// route around the tenant's storage limit: the plan is priced with a dry run
// and the bytes it writes are reserved before any of it is applied.
func TestQuotaMVCCService_RestoreAccountsWrittenBytes(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRevert, ValueBytes: 30},
		{Key: "b", Action: service.RestoreActionRecreate, ValueBytes: 20},
		{Key: "c", Action: service.RestoreActionDelete},
	})
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	result, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Applied != 3 {
		t.Fatalf("Applied = %d, want 3", result.Applied)
	}

	if got := acct.Usage("t1"); got != 50 {
		t.Fatalf("usage = %d, want 50 (30 + 20 written; the delete is not credited)", got)
	}
	if len(inner.calls) != 2 || !inner.calls[0] || inner.calls[1] {
		t.Fatalf("expected a dry-run price then an apply, got DryRun flags %v", inner.calls)
	}
}

// TestQuotaMVCCService_RestoreRefusedOverLimit proves the reservation is
// enforced, not merely recorded: a restore whose plan does not fit is rejected
// before a single change is applied.
func TestQuotaMVCCService_RestoreRefusedOverLimit(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRecreate, ValueBytes: 80},
	})
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 50)

	_, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	})
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("expected *quota.ExceededError, got %#v", err)
	}
	for i, dryRun := range inner.calls {
		if !dryRun {
			t.Fatalf("call %d applied the restore despite the quota refusal", i)
		}
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 after a refused restore", got)
	}
}

// TestQuotaMVCCService_RestoreReleasesReservationOnApplyFailure checks the
// reservation is not leaked when the apply fails, which would inflate the
// tenant's usage permanently.
func TestQuotaMVCCService_RestoreReleasesReservationOnApplyFailure(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRevert, ValueBytes: 40},
	})
	inner.applyErr = errBoom
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	_, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("expected the inner error, got %#v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0: the reservation was not released", got)
	}
}

// TestQuotaMVCCService_RestoreDryRunIsUnaccounted checks a dry run is
// forwarded once and charges nothing: it writes nothing.
func TestQuotaMVCCService_RestoreDryRunIsUnaccounted(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRevert, ValueBytes: 40},
	})
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	result, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/", DryRun: true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if !result.DryRun || result.Applied != 0 {
		t.Fatalf("dry run returned DryRun=%v Applied=%d", result.DryRun, result.Applied)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 for a dry run", got)
	}
	if len(inner.calls) != 1 || !inner.calls[0] {
		t.Fatalf("expected a single dry-run forward, got %v", inner.calls)
	}
}

// TestQuotaMVCCService_RestorePlanWithNoWritesReservesNothing covers the
// delete-only plan: nothing is written, so nothing is reserved.
func TestQuotaMVCCService_RestorePlanWithNoWritesReservesNothing(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionDelete},
	})
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 0)

	if _, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	}); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

// TestQuotaMVCCService_RestoreSurfacesRefusalFromPricingRun proves a request
// the inner service refuses — an unscoped or internal-namespace restore, say —
// is reported as that refusal and never reaches the apply call, so the quota
// wrapper cannot turn a security refusal into a quota error or a second write
// attempt.
func TestQuotaMVCCService_RestoreSurfacesRefusalFromPricingRun(t *testing.T) {
	inner := newRestorePricingMVCC(nil)
	inner.priceErr = &service.RestoreScopeEmptyError{}
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	_, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{Revision: 5})
	var empty *service.RestoreScopeEmptyError
	if !errors.As(err, &empty) {
		t.Fatalf("expected *service.RestoreScopeEmptyError, got %#v", err)
	}
	if len(inner.calls) != 1 || !inner.calls[0] {
		t.Fatalf("expected only the pricing dry run, got DryRun flags %v", inner.calls)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 after a refused restore", got)
	}
}

func TestRestoreDryRunOf_CopiesRatherThanMutates(t *testing.T) {
	if restoreDryRunOf(nil) != nil {
		t.Fatal("restoreDryRunOf(nil) must be nil")
	}

	req := &service.RestoreRevisionRequest{Revision: 5, Prefix: "app/"}
	dry := restoreDryRunOf(req)
	if !dry.DryRun {
		t.Fatal("the copy is not a dry run")
	}
	if req.DryRun {
		t.Fatal("the caller's request was mutated")
	}
	if dry.Revision != req.Revision || dry.Prefix != req.Prefix {
		t.Fatalf("the copy lost the scope: %+v", dry)
	}
}

// TestQuotaMVCCService_RestoreReleasesConflictedBytes checks the accounting
// follows what was actually written. Changes the compare-and-swap guard
// refused produced no bytes, so charging for them would inflate the tenant's
// usage every time a restore raced a live writer.
func TestQuotaMVCCService_RestoreReleasesConflictedBytes(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRevert, ValueBytes: 30},
		{Key: "b", Action: service.RestoreActionRevert, ValueBytes: 20},
	})
	inner.conflicts = []service.RestoreKeyChange{
		{Key: "b", Action: service.RestoreActionRevert, ValueBytes: 20},
	}
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	result, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Conflicted != 1 {
		t.Fatalf("Conflicted = %d, want 1", result.Conflicted)
	}
	if got := acct.Usage("t1"); got != 30 {
		t.Fatalf("usage = %d, want 30 (only the applied change is charged)", got)
	}
}

// TestQuotaMVCCService_RestoreForwardsForce checks the override survives the
// quota wrapper, including on the pricing dry run, so the plan that is priced
// is the plan that is applied.
func TestQuotaMVCCService_RestoreForwardsForce(t *testing.T) {
	inner := newRestorePricingMVCC([]service.RestoreKeyChange{
		{Key: "a", Action: service.RestoreActionRevert, ValueBytes: 10},
	})
	acct, _ := newTestAccountant(t, nil)
	svc := newQuotaMVCCService(inner, acct, "t1", 100)

	if _, err := svc.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 5, Prefix: "app/", Force: true,
	}); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	for i, force := range inner.forceFlags {
		if !force {
			t.Fatalf("call %d lost the Force flag: %v", i, inner.forceFlags)
		}
	}
	if len(inner.forceFlags) != 2 {
		t.Fatalf("expected a pricing call and an apply call, got %d", len(inner.forceFlags))
	}
}
