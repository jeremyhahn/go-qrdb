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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// nodeSealDatabases is a database barrier manager over an initialized,
// unsealed system barrier, holding one database of every barrier mode, all
// unsealed: "pass" (owner passphrase), "vault" (2-of-2 Shamir, claimed by
// holders) and "auto" (system-managed, wrapped by the system barrier -- no
// product path creates one today, but the registry's rule must hold for it).
type nodeSealDatabases struct {
	bm      *DatabaseBarrierManager
	mgr     *Manager
	system  *seal.StorageBarrier
	holders []*dbHolder
}

func newNodeSealDatabases(t *testing.T) *nodeSealDatabases {
	t.Helper()
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm := holderBarrierManager(t, ca)
	system := bm.registry.SystemBarrier()
	if err := system.Initialize(ctx, testCreds()); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	t.Cleanup(func() { _ = mgr.Close() })

	pass := validDedicatedMemoryConfig(t, "pass")
	pass.TenantID = "acme"
	pass.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase}
	if _, err := mgr.CreateWithCredentials(ctx, pass, testCreds()); err != nil {
		t.Fatalf("create pass: %v", err)
	}
	if _, err := mgr.CreateWithCredentials(ctx, shamirDBConfig(t, "vault"), seal.Credentials{}); err != nil {
		t.Fatalf("create vault: %v", err)
	}
	holders := []*dbHolder{newDBHolder(t, "alice"), newDBHolder(t, "bob")}
	enrolAndClaim(t, bm, "vault", holders...)
	if _, err := bm.registry.RegisterTenant(ctx, "auto", seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "db/auto/root-key"}, seal.NewSystemBarrierStrategy(system)); err != nil {
		t.Fatalf("register auto: %v", err)
	}
	if err := bm.registry.InitializeTenant(ctx, "auto", seal.Credentials{}); err != nil {
		t.Fatalf("initialize auto: %v", err)
	}
	return &nodeSealDatabases{bm: bm, mgr: mgr, system: system, holders: holders}
}

// keyWiped reports whether name's barrier is sealed with its key wiped.
func (f *nodeSealDatabases) keyWiped(t *testing.T, name string) bool {
	t.Helper()
	tb, err := f.bm.registry.GetTenant(name)
	if err != nil {
		t.Fatal(err)
	}
	return tb.IsSealed() && tb.Barrier().EpochBarrier().IsSealed()
}

// nodeSeal seals the database half, then the system barrier, as
// service.SealWholeNode does; nodeUnseal unseals the system barrier.
func (f *nodeSealDatabases) nodeSeal(t *testing.T) {
	t.Helper()
	if err := f.mgr.SealBarrierDatabases(); err != nil {
		t.Fatalf("SealBarrierDatabases: %v", err)
	}
	if err := f.system.Seal(); err != nil {
		t.Fatal(err)
	}
}

func (f *nodeSealDatabases) nodeUnseal(t *testing.T) {
	t.Helper()
	if err := f.system.Unseal(context.Background(), testCreds()); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}
}

// TestNodeSeal_WipesEveryDatabaseKey: the database half of a node seal wipes
// the key of a database of every barrier mode and marks the databases sealed.
func TestNodeSeal_WipesEveryDatabaseKey(t *testing.T) {
	f := newNodeSealDatabases(t)
	f.nodeSeal(t)
	for _, name := range []string{"pass", "vault", "auto"} {
		if !f.keyWiped(t, name) {
			t.Fatalf("database %s kept its key through the node seal", name)
		}
	}
	for _, name := range []string{"pass", "vault"} {
		if db, _ := f.mgr.Get(name); db.Status() != StatusSealed {
			t.Fatalf("database %s status = %q, want sealed", name, db.Status())
		}
	}
}

// TestNodeUnseal_BringsBackOnlySystemManagedDatabases: after the node
// unseals, the system-managed database is back; the passphrase database stays
// sealed until its owner unseals it, and the Shamir database until its
// quorum does.
func TestNodeUnseal_BringsBackOnlySystemManagedDatabases(t *testing.T) {
	ctx := context.Background()
	f := newNodeSealDatabases(t)
	f.nodeSeal(t)
	f.nodeUnseal(t)

	if f.keyWiped(t, "auto") {
		t.Fatal("the system-managed database stayed sealed after the node unseal")
	}
	for _, name := range []string{"pass", "vault"} {
		if !f.keyWiped(t, name) {
			t.Fatalf("the node unseal unsealed %s, which its owner or quorum must unseal", name)
		}
	}
	if err := f.bm.UnsealDatabase(ctx, "pass", testCreds()); err != nil {
		t.Fatalf("owner unseal of pass: %v", err)
	}
	if f.keyWiped(t, "pass") {
		t.Fatal("pass is still sealed after its owner's unseal")
	}
	if _, err := submitDB(t, f.bm, "vault", seal.OpUnseal, f.holders[0]); err != nil {
		t.Fatal(err)
	}
	out, err := submitDB(t, f.bm, "vault", seal.OpUnseal, f.holders[1])
	if err != nil || !out.Complete {
		t.Fatalf("vault unseal quorum = %+v, %v", out, err)
	}
	if f.keyWiped(t, "vault") {
		t.Fatal("vault is still sealed after its quorum")
	}
	if pending := f.bm.PendingUnseals(); len(pending) != 0 {
		t.Fatalf("PendingUnseals = %v, want none", pending)
	}
}

// TestNodeUnseal_LeavesAnExplicitlySealedDatabaseSealed: a database sealed
// on its own authority after the node seal -- its owner, its quorum or its
// cluster seal record (SealAuthorized) -- is no longer the node seal's to
// undo; and one the gate refuses stays sealed too.
func TestNodeUnseal_LeavesAnExplicitlySealedDatabaseSealed(t *testing.T) {
	f := newNodeSealDatabases(t)
	f.nodeSeal(t)
	if err := f.bm.SealAuthorized("auto"); err != nil {
		t.Fatal(err)
	}
	f.nodeUnseal(t)
	if !f.keyWiped(t, "auto") {
		t.Fatal("the node unseal undid an explicit seal of the database")
	}

	g := newNodeSealDatabases(t)
	g.bm.SetUnsealGate(func(name string) bool { return name == "auto" })
	g.nodeSeal(t)
	g.nodeUnseal(t)
	if !g.keyWiped(t, "auto") {
		t.Fatal("the node unseal unsealed a database its record refuses")
	}
	if pending := g.bm.PendingUnseals(); len(pending) != 0 {
		t.Fatalf("PendingUnseals = %v after the gate refused, want none", pending)
	}
}

// TestDatabaseBarrierManager_SubmitHolderShareRefusesANonShamirDatabase: the
// holder path refuses a database without holders before it reads the share.
func TestDatabaseBarrierManager_SubmitHolderShareRefusesANonShamirDatabase(t *testing.T) {
	f := newNodeSealDatabases(t)
	share := []byte("share")
	_, err := f.bm.SubmitHolderShare(context.Background(), "pass", &seal.HolderSubmission{Op: seal.OpUnseal, Share: share}, nil)
	var notShamir *NotShamirBarrierError
	if !errors.As(err, &notShamir) {
		t.Fatalf("err = %v, want *NotShamirBarrierError", err)
	}
	for _, b := range share {
		if b != 0 {
			t.Fatal("the refused share was not wiped")
		}
	}
}

// TestManagerClose_WipesEveryDatabaseKey: closing the manager -- a shutdown,
// a failed boot, a data-plane teardown -- leaves no database key in memory,
// whatever the barrier's mode.
func TestManagerClose_WipesEveryDatabaseKey(t *testing.T) {
	f := newNodeSealDatabases(t)
	if err := f.mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, name := range []string{"pass", "vault", "auto"} {
		if !f.keyWiped(t, name) {
			t.Fatalf("database %s kept its key through the manager's close", name)
		}
	}
}

// TestManagerClose_WithoutABarrierManager closes a manager that holds no
// barrier at all.
func TestManagerClose_WithoutABarrierManager(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	if _, err := mgr.Create(validMemoryConfig("plain")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
