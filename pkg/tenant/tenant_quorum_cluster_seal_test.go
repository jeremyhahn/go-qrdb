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

package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// clusterQuorumTenant is a Shamir tenant barrier with two claimed holders
// (2-of-2) behind a tenant service wired to sealer, whose enforcement seals
// the tenant through the registry as SealEnforcer.SealTenant does.
type clusterQuorumTenant struct {
	svc     *TenantService
	reg     *seal.BarrierRegistry
	holders []*tenantHolder
}

const clusterQuorumTenantID = "acme"

func newClusterQuorumTenant(t *testing.T, sealer *clustersealtest.Sealer) *clusterQuorumTenant {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(),
		WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t)}), WithClusterSealer(sealer))
	if err != nil {
		t.Fatal(err)
	}
	sealer.OnSeal(func(b clusterseal.Barrier) error { return reg.SealTenant(b.Tenant()) })
	if _, err := svc.CreateTenant(context.Background(), clusterQuorumTenantID, "Acme", ""); err != nil {
		t.Fatal(err)
	}
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob")}
	shamirTenantWithClaimedHolders(t, svc, tenantAdminCtx(clusterQuorumTenantID), clusterQuorumTenantID, holders)
	return &clusterQuorumTenant{svc: svc, reg: reg, holders: holders}
}

// quorum submits op from both holders and returns the threshold result.
func (c *clusterQuorumTenant) quorum(t *testing.T, op seal.QuorumOp) (*seal.QuorumOutcome, error) {
	t.Helper()
	ctx := tenantAdminCtx(clusterQuorumTenantID)
	if out, err := submitTenant(t, c.svc, ctx, clusterQuorumTenantID, op, c.holders[0]); err != nil || out.Complete {
		t.Fatalf("first %s share = %+v, %v", op, out, err)
	}
	return submitTenant(t, c.svc, ctx, clusterQuorumTenantID, op, c.holders[1])
}

func (c *clusterQuorumTenant) sealed(t *testing.T) bool {
	t.Helper()
	tb, err := c.reg.GetTenant(clusterQuorumTenantID)
	if err != nil {
		t.Fatal(err)
	}
	return tb.IsSealed() || tb.Barrier().EpochBarrier().IsSealed()
}

// TestTenantQuorumSeal_ActiveRecordsTheTenantSeal: a tenant seal quorum writes
// the tenant's cluster seal record, then the enforcement seals it here; the
// unseal quorum that follows clears the record.
func TestTenantQuorumSeal_ActiveRecordsTheTenantSeal(t *testing.T) {
	sealer := clustersealtest.New(true)
	c := newClusterQuorumTenant(t, sealer)

	out, err := c.quorum(t, seal.OpSeal)
	if err != nil || !out.Complete {
		t.Fatalf("seal quorum = %+v, %v", out, err)
	}
	b := clusterseal.TenantBarrier(clusterQuorumTenantID)
	if got := sealer.Seals(); len(got) != 1 || got[0] != b {
		t.Fatalf("seals = %v, want the tenant barrier once", got)
	}
	if !c.sealed(t) {
		t.Fatal("the tenant kept its key after the seal quorum")
	}

	out, err = c.quorum(t, seal.OpUnseal)
	if err != nil || !out.Complete {
		t.Fatalf("unseal quorum = %+v, %v", out, err)
	}
	if got := sealer.Clears(); len(got) != 1 || got[0] != b {
		t.Fatalf("clears = %v, want the tenant barrier once", got)
	}
	if rec := sealer.Record(b); rec.Sealed {
		t.Fatalf("record after the unseal quorum = %+v, want clear", rec)
	}
}

// TestTenantQuorumSeal_RecordRefusedLeavesTheTenantUnsealed: a seal quorum
// whose record definitely was not written reports the typed error and wipes no
// key.
func TestTenantQuorumSeal_RecordRefusedLeavesTheTenantUnsealed(t *testing.T) {
	sealer := clustersealtest.New(true)
	b := clusterseal.TenantBarrier(clusterQuorumTenantID)
	sealer.SetSealRefusal(&clusterseal.SealEpochConflictError{Barrier: b})
	c := newClusterQuorumTenant(t, sealer)

	_, err := c.quorum(t, seal.OpSeal)
	var conflict *clusterseal.SealEpochConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *clusterseal.SealEpochConflictError", err)
	}
	if c.sealed(t) {
		t.Fatal("the tenant lost its key although the record was not written")
	}
}

// TestTenantQuorumUnseal_ClearFailureSealsAgain: an unseal quorum whose clear
// record cannot be written leaves the tenant sealed here, so this node never
// serves a tenant its record still says is sealed.
func TestTenantQuorumUnseal_ClearFailureSealsAgain(t *testing.T) {
	sealer := clustersealtest.New(true)
	c := newClusterQuorumTenant(t, sealer)
	if out, err := c.quorum(t, seal.OpSeal); err != nil || !out.Complete {
		t.Fatalf("seal quorum = %+v, %v", out, err)
	}
	refused := errors.New("no quorum")
	sealer.SetClearErr(refused)

	_, err := c.quorum(t, seal.OpUnseal)
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the clear's error", err)
	}
	if !c.sealed(t) {
		t.Fatal("the tenant serves although its record could not be cleared")
	}
}
