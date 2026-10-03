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

package seal

import (
	"context"
	"errors"
	"testing"
)

// submitWith sends holder's own share through SubmitHolderShareWith.
func (f *holderFixture) submitWith(op QuorumOp, holder string, seal func() error) (*QuorumOutcome, error) {
	f.t.Helper()
	_, digest, err := f.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil {
		return nil, err
	}
	return f.b.SubmitHolderShareWith(context.Background(), &HolderSubmission{
		Domain: DomainUnseal,
		Op:     op,
		Holder: holder,
		Proof:  f.verifier.issue(DomainUnseal, op, holder, digest),
		Share:  []byte(f.dist.share(DomainUnseal, holder)),
	}, seal)
}

// TestSubmitHolderShareWith_SealQuorumRunsTheCallersSeal: a completed seal
// round runs the supplied seal in place of Seal, once, and only at the
// threshold.
func TestSubmitHolderShareWith_SealQuorumRunsTheCallersSeal(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	calls := 0
	seal := func() error {
		calls++
		return f.b.Seal()
	}
	if o, err := f.submitWith(OpSeal, "h1", seal); err != nil || o.Complete {
		t.Fatalf("first share = %+v, %v", o, err)
	}
	if calls != 0 {
		t.Fatal("the seal ran below the threshold")
	}
	if o, err := f.submitWith(OpSeal, "h2", seal); err != nil || !o.Complete {
		t.Fatalf("threshold share = %+v, %v", o, err)
	}
	if calls != 1 || !f.b.IsSealed() {
		t.Fatalf("seal calls = %d, sealed = %v; want 1 and sealed", calls, f.b.IsSealed())
	}
}

// TestSubmitHolderShareWith_SealFailureLeavesTheBarrierUnsealed: when the
// supplied seal refuses -- a cluster seal whose record could not be written --
// the quorum reports its error and this barrier keeps its key.
func TestSubmitHolderShareWith_SealFailureLeavesTheBarrierUnsealed(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	refused := errors.New("record refused")
	seal := func() error { return refused }
	if _, err := f.submitWith(OpSeal, "h1", seal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.submitWith(OpSeal, "h2", seal); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the seal's error", err)
	}
	if f.b.IsSealed() {
		t.Fatal("the barrier was sealed although the supplied seal refused")
	}
}

// TestSubmitHolderShareWith_OtherOperationsNeverSeal: the supplied seal runs
// for a seal round only.
func TestSubmitHolderShareWith_OtherOperationsNeverSeal(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	seal := func() error {
		t.Fatal("the seal ran for a root-token round")
		return nil
	}
	for _, h := range []string{"h1", "h2"} {
		if _, err := f.submitWith(OpRootToken, h, seal); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSubmitHolderShareWith_NilSealIsSeal: a nil seal seals this barrier, as
// SubmitHolderShare does.
func TestSubmitHolderShareWith_NilSealIsSeal(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	for _, h := range []string{"h1", "h2"} {
		if _, err := f.submitWith(OpSeal, h, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !f.b.IsSealed() {
		t.Fatal("not sealed")
	}
}

// holderSubmission builds holder's own submission for op.
func (f *holderFixture) holderSubmission(op QuorumOp, holder string) *HolderSubmission {
	f.t.Helper()
	_, digest, err := f.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil {
		f.t.Fatal(err)
	}
	return &HolderSubmission{
		Domain: DomainUnseal,
		Op:     op,
		Holder: holder,
		Proof:  f.verifier.issue(DomainUnseal, op, holder, digest),
		Share:  []byte(f.dist.share(DomainUnseal, holder)),
	}
}

// TestSubmitTenantHolderShare_UnsealIsOrderedAgainstTheNodeSeal: a tenant's
// quorum unseal runs under sealMu, as UnsealTenant does, and the unseal it
// completes clears the tenant's pending node unseal.
func TestSubmitTenantHolderShare_UnsealIsOrderedAgainstTheNodeSeal(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	reg := newTestRegistry(t)
	reg.tenants.Store("shamir", WrapTenantBarrier("shamir", f.b))
	reg.sealMu.Lock()
	reg.nodeSealed["shamir"] = ErrSealed
	done := startInWindow(func() {
		if _, err := reg.SubmitTenantHolderShare(ctx, "shamir", f.holderSubmission(OpUnseal, "h1"), nil); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-done:
		reg.sealMu.Unlock()
		t.Fatal("an unseal submission completed while sealMu was held")
	default:
	}
	reg.sealMu.Unlock()
	<-done

	out, err := reg.SubmitTenantHolderShare(ctx, "shamir", f.holderSubmission(OpUnseal, "h2"), nil)
	if err != nil || !out.Complete {
		t.Fatalf("unseal quorum = %+v, %v", out, err)
	}
	if f.b.IsSealed() {
		t.Fatal("the tenant stayed sealed after its unseal quorum")
	}
	if _, pending := reg.PendingTenantUnseals()["shamir"]; pending {
		t.Fatal("the tenant's own unseal quorum did not clear its pending node unseal")
	}
}

// TestSubmitTenantHolderShare_SealRunsTheCallersSealWithoutTheLock: a seal
// submission takes no registry lock -- its seal takes sealMu itself -- and
// runs the supplied seal; an unknown tenant is refused.
func TestSubmitTenantHolderShare_SealRunsTheCallersSealWithoutTheLock(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	reg := newTestRegistry(t)
	reg.tenants.Store("shamir", WrapTenantBarrier("shamir", f.b))
	seal := func() error { return reg.SealTenant("shamir") }
	if _, err := reg.SubmitTenantHolderShare(ctx, "shamir", f.holderSubmission(OpSeal, "h1"), seal); err != nil {
		t.Fatal(err)
	}
	out, err := reg.SubmitTenantHolderShare(ctx, "shamir", f.holderSubmission(OpSeal, "h2"), seal)
	if err != nil || !out.Complete {
		t.Fatalf("seal quorum = %+v, %v", out, err)
	}
	if !f.b.IsSealed() {
		t.Fatal("not sealed")
	}
	if _, err := reg.SubmitTenantHolderShare(ctx, "ghost", f.holderSubmission(OpSeal, "h1"), seal); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("unknown tenant: err = %v, want ErrTenantNotFound", err)
	}
}
