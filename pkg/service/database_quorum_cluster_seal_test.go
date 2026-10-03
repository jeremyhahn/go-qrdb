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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestDatabaseQuorumSeal_ActiveRecordsTheDatabaseSeal: a Shamir database's
// seal quorum writes its cluster seal record and the enforcement -- which
// seals a quorum-sealed barrier, as every node holding it must -- seals it
// here; the unseal quorum clears the record. A single caller's seal is still
// refused, before anything is recorded.
func TestDatabaseQuorumSeal_ActiveRecordsTheDatabaseSeal(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	sealer := clustersealtest.New(true)
	svc.sealer = sealer
	sealer.OnSeal(func(b clusterseal.Barrier) error { return svc.SealDatabaseLocal(b.Tenant(), b.Database()) })
	ctx := dbAdminCtx("acme")
	holders := claimedDatabaseHolders(t, svc, ctx)
	b := clusterseal.DatabaseBarrier("acme", "vault")

	if err := svc.SealDatabase(ctx, "vault"); !errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		t.Fatalf("single-caller seal = %v, want ErrShamirBarrierRequiresQuorum", err)
	}
	if len(sealer.Seals()) != 0 {
		t.Fatal("a refused single-caller seal was recorded")
	}

	for _, h := range holders {
		if _, err := submitDBSvc(t, svc, ctx, seal.OpSeal, h); err != nil {
			t.Fatal(err)
		}
	}
	if got := sealer.Seals(); len(got) != 1 || got[0] != b {
		t.Fatalf("seals = %v, want the database barrier once", got)
	}
	if db, _ := mgr.Get("vault"); db.Status() != database.StatusSealed {
		t.Fatalf("status after the seal quorum = %q, want sealed", db.Status())
	}

	for _, h := range holders {
		if _, err := submitDBSvc(t, svc, ctx, seal.OpUnseal, h); err != nil {
			t.Fatal(err)
		}
	}
	if got := sealer.Clears(); len(got) != 1 || got[0] != b {
		t.Fatalf("clears = %v, want the database barrier once", got)
	}
	if db, _ := mgr.Get("vault"); db.Status() != database.StatusOpen {
		t.Fatalf("status after the unseal quorum = %q, want open", db.Status())
	}
}

// TestDatabaseQuorumSeal_RecordRefusedLeavesTheDatabaseOpen: a seal quorum
// whose record definitely was not written reports the typed error and leaves
// the database open.
func TestDatabaseQuorumSeal_RecordRefusedLeavesTheDatabaseOpen(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	sealer := clustersealtest.New(true)
	sealer.SetSealRefusal(&clusterseal.SealEpochConflictError{Barrier: clusterseal.DatabaseBarrier("acme", "vault")})
	svc.sealer = sealer
	ctx := dbAdminCtx("acme")
	holders := claimedDatabaseHolders(t, svc, ctx)

	if _, err := submitDBSvc(t, svc, ctx, seal.OpSeal, holders[0]); err != nil {
		t.Fatal(err)
	}
	_, err := submitDBSvc(t, svc, ctx, seal.OpSeal, holders[1])
	var conflict *clusterseal.SealEpochConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *clusterseal.SealEpochConflictError", err)
	}
	st, err := svc.DatabaseBarrierStatus(ctx, "vault")
	if err != nil || st.Sealed {
		t.Fatalf("status = %+v, %v; want unsealed", st, err)
	}
	if db, _ := mgr.Get("vault"); db.Status() == database.StatusSealed {
		t.Fatal("the database is marked sealed although the record was not written")
	}
}
