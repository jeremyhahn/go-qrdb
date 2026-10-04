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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// quorumSealNode is a Shamir node barrier with two claimed holders (2-of-3),
// a barrier registry holding one system-managed tenant ("auto") and one
// self-managed tenant ("self"), and a service wired to sealer, the way a
// composer wires them. sealer's enforcement seals the whole node, as
// SealEnforcer.SealNode does.
type quorumSealNode struct {
	env     *holderSvcEnv
	reg     *seal.BarrierRegistry
	dbs     *database.Manager
	holders []*svcHolder
}

func newQuorumSealNode(t *testing.T, sealer *clustersealtest.Sealer) *quorumSealNode {
	t.Helper()
	ctx := context.Background()
	e := newHolderSvcEnv(t, 2, 3)
	hs := []*svcHolder{newSvcHolder(t, "alice"), newSvcHolder(t, "bob"), newSvcHolder(t, "carol")}
	e.register(seal.DomainUnseal, hs...)
	// A plain, non-ceremony BarrierService over the same barrier: e.svc's
	// ceremony now refuses BarrierInitializeShamir (go-qrdb#560 review
	// item 8), the same way it already refused BarrierInitialize.
	_, err := NewBarrierService(e.barrier).BarrierInitializeShamir(ctx, 2, 3, seal.FactorPolicyDesignated)
	require.NoError(t, err)
	for _, h := range hs {
		e.claim(seal.DomainUnseal, h)
	}

	reg, err := seal.NewBarrierRegistry(testLogger(), e.barrier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	_, err = reg.RegisterTenant(ctx, "auto", seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "tenants/auto/root"}, seal.NewSystemBarrierStrategy(e.barrier))
	require.NoError(t, err)
	require.NoError(t, reg.InitializeTenant(ctx, "auto", seal.Credentials{}))
	_, err = reg.RegisterTenant(ctx, "self", seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "tenants/self/root"}, seal.NewDeferredSoftwareStrategy())
	require.NoError(t, err)
	require.NoError(t, reg.InitializeTenant(ctx, "self", seal.MustCredentials(context.Background(), []byte("self-pass"))))

	dbs := newPassphraseDatabases(t, e.barrier)

	sealer.OnSeal(func(clusterseal.Barrier) error { return SealWholeNode(reg, nil, dbs) })
	e.svc = NewBarrierServiceWithCeremony(e.barrier, e.core, WithClusterSealer(sealer), WithBarrierRegistry(reg), WithBarrierDatabases(dbs))
	return &quorumSealNode{env: e, reg: reg, dbs: dbs, holders: hs}
}

// sealQuorum submits a seal share from the first two holders and returns the
// threshold submission's result.
func (n *quorumSealNode) sealQuorum(t *testing.T) (*QuorumOutcomeInfo, error) {
	t.Helper()
	out, err := n.env.submit(seal.DomainUnseal, seal.OpSeal, n.holders[0])
	require.NoError(t, err)
	require.False(t, out.Complete)
	return n.env.submit(seal.DomainUnseal, seal.OpSeal, n.holders[1])
}

func (n *quorumSealNode) tenant(t *testing.T, id string) *seal.TenantBarrier {
	t.Helper()
	tb, err := n.reg.GetTenant(id)
	require.NoError(t, err)
	return tb
}

// TestBarrierQuorumSeal_ActiveRecordsAndSealsTheWholeNode: a completed seal
// quorum writes the node's cluster seal record and, through its enforcement,
// seals the whole node -- every system-managed tenant's keys wiped, and
// recorded as the node seal's to bring back.
func TestBarrierQuorumSeal_ActiveRecordsAndSealsTheWholeNode(t *testing.T) {
	sealer := clustersealtest.New(true)
	n := newQuorumSealNode(t, sealer)

	out, err := n.sealQuorum(t)
	require.NoError(t, err)
	require.True(t, out.Complete)

	if got := sealer.Seals(); len(got) != 1 || got[0] != clusterseal.NodeBarrier() {
		t.Fatalf("seals = %v, want the node barrier once", got)
	}
	if rec := sealer.Record(clusterseal.NodeBarrier()); !rec.Sealed {
		t.Fatalf("node record = %+v, want sealed", rec)
	}
	if !n.env.barrier.IsSealed() || !n.env.barrier.EpochBarrier().IsSealed() {
		t.Fatal("the node barrier kept its key after the seal quorum")
	}
	for _, id := range []string{"auto", "self"} {
		tb := n.tenant(t, id)
		if !tb.IsSealed() || !tb.Barrier().EpochBarrier().IsSealed() {
			t.Fatalf("tenant %s kept its keys after the seal quorum", id)
		}
	}
	if _, pending := n.reg.PendingTenantUnseals()["auto"]; !pending {
		t.Fatal("the system-managed tenant is not recorded as the node seal's to bring back")
	}
	if !databaseKeyWiped(t, n.dbs, "secure") {
		t.Fatal("the database kept its key after the seal quorum")
	}
}

// TestBarrierQuorumSeal_RecordRefusedLeavesTheNodeUnsealed: when the record
// definitely was not written, the seal quorum reports the sealer's typed error
// and wipes no key -- the cluster is not sealed, so neither is this node.
func TestBarrierQuorumSeal_RecordRefusedLeavesTheNodeUnsealed(t *testing.T) {
	sealer := clustersealtest.New(true)
	sealer.SetSealRefusal(&clusterseal.SealEpochConflictError{Barrier: clusterseal.NodeBarrier()})
	n := newQuorumSealNode(t, sealer)

	_, err := n.sealQuorum(t)
	var conflict *clusterseal.SealEpochConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *clusterseal.SealEpochConflictError", err)
	}
	if n.env.barrier.IsSealed() || n.env.barrier.EpochBarrier().IsSealed() {
		t.Fatal("the node barrier lost its key although the record was not written")
	}
	for _, id := range []string{"auto", "self"} {
		tb := n.tenant(t, id)
		if tb.IsSealed() || tb.Barrier().EpochBarrier().IsSealed() {
			t.Fatalf("tenant %s lost its keys although the record was not written", id)
		}
	}
	if rec := sealer.Record(clusterseal.NodeBarrier()); rec.Sealed {
		t.Fatalf("node record = %+v, want none", rec)
	}
	if databaseKeyWiped(t, n.dbs, "secure") {
		t.Fatal("the database lost its key although the record was not written")
	}
}

// TestBarrierQuorumSeal_InactiveSealsTheWholeNode: before activation a seal
// quorum seals this node whole, as the single-admin seal does, and records
// nothing.
func TestBarrierQuorumSeal_InactiveSealsTheWholeNode(t *testing.T) {
	sealer := clustersealtest.New(false)
	n := newQuorumSealNode(t, sealer)

	out, err := n.sealQuorum(t)
	require.NoError(t, err)
	require.True(t, out.Complete)
	if len(sealer.Seals()) != 0 {
		t.Fatal("an inactive cluster seal was recorded")
	}
	if !n.env.barrier.IsSealed() {
		t.Fatal("the node barrier is unsealed")
	}
	for _, id := range []string{"auto", "self"} {
		if !n.tenant(t, id).IsSealed() {
			t.Fatalf("tenant %s is unsealed after the seal quorum", id)
		}
	}
	if !databaseKeyWiped(t, n.dbs, "secure") {
		t.Fatal("the database kept its key after the seal quorum")
	}
	if db, _ := n.dbs.Get("secure"); db.Status() != database.StatusSealed {
		t.Fatalf("database status = %q, want sealed", db.Status())
	}
}

// newPassphraseDatabases is a database manager whose barrier manager shares
// system, holding one unsealed passphrase database "secure".
func newPassphraseDatabases(t *testing.T, system *seal.StorageBarrier) *database.Manager {
	t.Helper()
	bm, err := database.NewDatabaseBarrierManager(testLogger(), system)
	require.NoError(t, err)
	mgr := database.NewManager(nil, t.TempDir(), database.WithBarrierManager(bm))
	t.Cleanup(func() { _ = mgr.Close() })
	_, err = mgr.CreateWithCredentials(context.Background(), database.DatabaseConfig{
		TenantID: "acme", Name: "secure", Engine: database.EngineKV, Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
	}, seal.MustCredentials(context.Background(), []byte("db-pass")))
	require.NoError(t, err)
	return mgr
}

// databaseKeyWiped reports whether name's barrier holds no key.
func databaseKeyWiped(t *testing.T, mgr *database.Manager, name string) bool {
	t.Helper()
	bm := mgr.BarrierManager()
	st, err := bm.BarrierStatus(name)
	require.NoError(t, err)
	enc := bm.GetBarrier(name)
	return st.Sealed && enc != nil && enc.IsSealed()
}
