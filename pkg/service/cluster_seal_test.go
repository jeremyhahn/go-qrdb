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
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func opsCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "ops", TenantID: "system"})
}

// newClusterBarrierService returns an initialized, unsealed software barrier
// service wired to sealer, whose Seal enforces by sealing the barrier, as the
// node enforcer does.
func newClusterBarrierService(t *testing.T, sealer *clustersealtest.Sealer) (BarrierServicer, func() bool) {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	sealer.OnSeal(func(clusterseal.Barrier) error { return barrier.Seal() })
	svc := NewBarrierService(barrier, WithClusterSealer(sealer))
	if err := svc.BarrierInitialize(context.Background(), []byte("my-secret")); err != nil {
		t.Fatal(err)
	}
	return svc, barrier.IsSealed
}

func TestBarrierSeal_InactiveSealsThisNodeOnly(t *testing.T) {
	sealer := clustersealtest.New(false)
	svc, sealed := newClusterBarrierService(t, sealer)
	if err := svc.BarrierSeal(opsCtx()); err != nil {
		t.Fatal(err)
	}
	if !sealed() {
		t.Fatal("not sealed")
	}
	if len(sealer.Seals()) != 0 {
		t.Fatal("an inactive cluster seal was recorded")
	}
}

func TestBarrierSeal_ActiveRecordsTheNodeSealFirst(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, sealed := newClusterBarrierService(t, sealer)
	if err := svc.BarrierSeal(opsCtx()); err != nil {
		t.Fatal(err)
	}
	if got := sealer.Seals(); len(got) != 1 || got[0] != clusterseal.NodeBarrier() {
		t.Fatalf("seals = %v, want the node barrier", got)
	}
	if got := sealer.Principals()[0]; got != (clusterseal.Principal{Name: "ops", Tenant: "system"}) {
		t.Fatalf("principal = %+v", got)
	}
	if !sealed() {
		t.Fatal("the node was not sealed by the enforcement")
	}
}

func TestBarrierSeal_ActiveReportsTheSealerError(t *testing.T) {
	sealer := clustersealtest.New(true)
	boom := &clusterseal.RecordNotConfirmedError{Barrier: clusterseal.NodeBarrier(), Op: "seal", Err: errors.New("no quorum")}
	sealer.SetSealErr(boom)
	svc, sealed := newClusterBarrierService(t, sealer)
	if err := svc.BarrierSeal(opsCtx()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !sealed() {
		t.Fatal("this node must be sealed even when the record is unconfirmed")
	}
}

func TestBarrierStatus_CarriesTheClusterSeal(t *testing.T) {
	t.Run("no sealer", func(t *testing.T) {
		svc := initAndUnsealSoftwareBarrier(t)
		st, err := svc.BarrierStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.ClusterSeal == nil || st.ClusterSeal.Active {
			t.Fatalf("ClusterSeal = %+v, want present and inactive", st.ClusterSeal)
		}
	})
	t.Run("active", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		sealer.Put(clusterseal.Record{
			Barrier: clusterseal.NodeBarrier(), Epoch: 7, Sealed: true,
			Principal: "ops", PrincipalTenant: "system", Node: "192.0.2.1:5000", At: at,
		})
		sealer.SetLocal(clusterseal.LocalHeld)
		svc, _ := newClusterBarrierService(t, sealer)
		st, err := svc.BarrierStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := clusterseal.Report{Active: true, Sealed: true, Epoch: 7, Principal: "ops",
			PrincipalTenant: "system", Node: "192.0.2.1:5000", At: at, Local: clusterseal.LocalHeld}
		if *st.ClusterSeal != want {
			t.Fatalf("ClusterSeal = %+v, want %+v", *st.ClusterSeal, want)
		}
	})
}

// fakeClusterMigrations answers Report with a fixed report or error and
// records each call's scope and whether its context was bounded.
type fakeClusterMigrations struct {
	report  *clustermig.Report
	err     error
	scopes  []string
	bounded bool
}

func (f *fakeClusterMigrations) Report(ctx context.Context, tenantID, database string) (*clustermig.Report, error) {
	f.scopes = append(f.scopes, tenantID+"/"+database)
	_, f.bounded = ctx.Deadline()
	return f.report, f.err
}

var _ ClusterMigrations = (*fakeClusterMigrations)(nil)

func newMigrationBarrierService(t *testing.T, m ClusterMigrations) BarrierServicer {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier, WithClusterMigrations(m))
	if err := svc.BarrierInitialize(context.Background(), []byte("my-secret")); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestBarrierStatus_CarriesTheClusterMigration(t *testing.T) {
	ctx := context.Background()
	t.Run("no migrations source", func(t *testing.T) {
		st, err := initAndUnsealSoftwareBarrier(t).BarrierStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.ClusterMigration != nil {
			t.Fatalf("ClusterMigration = %+v, want nil", st.ClusterMigration)
		}
	})
	t.Run("none in progress", func(t *testing.T) {
		m := &fakeClusterMigrations{}
		st, err := newMigrationBarrierService(t, m).BarrierStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.ClusterMigration != nil {
			t.Fatalf("ClusterMigration = %+v, want nil", st.ClusterMigration)
		}
	})
	t.Run("in progress reads the system scope", func(t *testing.T) {
		want := &clustermig.Report{
			Target: seal.StrategyPassphrase, Generation: 3, Acked: 1, Total: 2,
			Nodes: []clustermig.NodeReport{
				{NodeID: "n2", Step: clustermig.StepServing, Phase: seal.PhaseComplete},
				{NodeID: "n1", Step: clustermig.StepRecycling, Phase: seal.PhaseCopying},
			},
		}
		m := &fakeClusterMigrations{report: want}
		st, err := newMigrationBarrierService(t, m).BarrierStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.ClusterMigration != want {
			t.Fatalf("ClusterMigration = %+v, want the store's report %+v", st.ClusterMigration, want)
		}
		if !slices.Equal(m.scopes, []string{"/"}) || !m.bounded {
			t.Fatalf("Report calls = %q (bounded %v), want one bounded call for the system scope", m.scopes, m.bounded)
		}
	})
	t.Run("unreadable is unconfirmed, not absent", func(t *testing.T) {
		m := &fakeClusterMigrations{err: errors.New("no leader")}
		st, err := newMigrationBarrierService(t, m).BarrierStatus(ctx)
		if err != nil {
			t.Fatalf("BarrierStatus failed with the migration unreadable: %v", err)
		}
		if !reflect.DeepEqual(st.ClusterMigration, &clustermig.Report{Unconfirmed: true}) {
			t.Fatalf("ClusterMigration = %+v, want unconfirmed and otherwise empty", st.ClusterMigration)
		}
	})
	t.Run("sealed node does not read the record", func(t *testing.T) {
		m := &fakeClusterMigrations{report: &clustermig.Report{Total: 1}}
		svc := newMigrationBarrierService(t, m)
		if err := svc.BarrierSeal(ctx); err != nil {
			t.Fatal(err)
		}
		st, err := svc.BarrierStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Sealed || st.ClusterMigration != nil || len(m.scopes) != 0 {
			t.Fatalf("sealed status = %+v after %d reads, want sealed, no migration, no read", st, len(m.scopes))
		}
	})
}

func TestBarrierClusterUnseal(t *testing.T) {
	t.Run("no sealer", func(t *testing.T) {
		svc := initAndUnsealSoftwareBarrier(t)
		if _, err := svc.BarrierClusterUnseal(opsCtx()); !errors.Is(err, clusterseal.ErrInactive) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("inactive", func(t *testing.T) {
		svc, _ := newClusterBarrierService(t, clustersealtest.New(false))
		if _, err := svc.BarrierClusterUnseal(opsCtx()); !errors.Is(err, clusterseal.ErrInactive) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refused while this node is sealed", func(t *testing.T) {
		// Clearing the node record is for an unsealed node: one still sealed
		// here would leave the cluster unsealed with this node holding no key.
		sealer := clustersealtest.New(true)
		sealer.Put(clusterseal.Record{Barrier: clusterseal.NodeBarrier(), Epoch: 3, Sealed: true})
		sealer.SetNodeSealed(true)
		svc, _ := newClusterBarrierService(t, sealer)
		if _, err := svc.BarrierClusterUnseal(opsCtx()); !errors.Is(err, seal.ErrSealed) {
			t.Fatalf("err = %v, want seal.ErrSealed", err)
		}
		if !sealer.Record(clusterseal.NodeBarrier()).Sealed {
			t.Fatal("the node record was cleared by a node still sealed")
		}
	})
	t.Run("clears the node record", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		sealer.Put(clusterseal.Record{Barrier: clusterseal.NodeBarrier(), Epoch: 3, Sealed: true})
		svc, _ := newClusterBarrierService(t, sealer)
		info, err := svc.BarrierClusterUnseal(opsCtx())
		if err != nil {
			t.Fatal(err)
		}
		if got := sealer.Clears(); len(got) != 1 || got[0] != clusterseal.NodeBarrier() {
			t.Fatalf("clears = %v", got)
		}
		if info.Sealed || info.Epoch != 4 || !info.Active || info.Principal != "ops" {
			t.Fatalf("info = %+v", info)
		}
	})
	t.Run("reports the sealer error", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		boom := &clusterseal.SealEpochConflictError{Barrier: clusterseal.NodeBarrier()}
		sealer.SetClearErr(boom)
		svc, _ := newClusterBarrierService(t, sealer)
		if _, err := svc.BarrierClusterUnseal(opsCtx()); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}

// newClusterDatabase creates a system-tenant database with its own barrier on
// a service wired to sealer; sealer's Seal enforces by sealing the database
// here, as the node enforcer does.
func newClusterDatabase(t *testing.T, sealer *clustersealtest.Sealer, name string) (*DatabaseService, []byte) {
	t.Helper()
	svc, mgr := newTestDatabaseServiceWithBarrier(t)
	svc.sealer = sealer
	sealer.OnSeal(func(b clusterseal.Barrier) error {
		if err := svc.barrierMgr.SealDatabase(b.Database(), b.Tenant()); err != nil {
			return err
		}
		db, err := mgr.Get(b.Database())
		if err != nil {
			return err
		}
		db.SetStatus(database.StatusSealed)
		return nil
	})
	cfg := testDBConfig(name)
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	secret := []byte("db-secret")
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), bytes.Clone(secret))); err != nil {
		t.Fatal(err)
	}
	return svc, secret
}

func dbStatus(t *testing.T, svc *DatabaseService, name string) database.StatusType {
	t.Helper()
	info, err := svc.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Status
}

func TestSealDatabase_ActiveRecordsTheDatabaseSeal(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, _ := newClusterDatabase(t, sealer, "secure")
	if err := svc.SealDatabase(opsCtx(), "secure"); err != nil {
		t.Fatal(err)
	}
	want := clusterseal.DatabaseBarrier(tenantid.System, "secure")
	if got := sealer.Seals(); len(got) != 1 || got[0] != want {
		t.Fatalf("seals = %v, want %v", got, want)
	}
	if dbStatus(t, svc, "secure") != database.StatusSealed {
		t.Fatal("not sealed on this node")
	}
}

func TestSealDatabase_InactiveSealsThisNodeOnly(t *testing.T) {
	sealer := clustersealtest.New(false)
	svc, _ := newClusterDatabase(t, sealer, "secure")
	if err := svc.SealDatabase(opsCtx(), "secure"); err != nil {
		t.Fatal(err)
	}
	if len(sealer.Seals()) != 0 || dbStatus(t, svc, "secure") != database.StatusSealed {
		t.Fatal("inactive seal recorded, or not sealed")
	}
}

func TestUnsealDatabase_ClearsASealedRecord(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, secret := newClusterDatabase(t, sealer, "secure")
	if err := svc.SealDatabase(opsCtx(), "secure"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnsealDatabase(opsCtx(), "secure", seal.MustCredentials(context.Background(), bytes.Clone(secret))); err != nil {
		t.Fatal(err)
	}
	b := clusterseal.DatabaseBarrier(tenantid.System, "secure")
	if rec := sealer.Record(b); rec.Sealed || sealer.Refuses(b) {
		t.Fatalf("record = %+v, want it cleared by the unseal", rec)
	}
	if dbStatus(t, svc, "secure") != database.StatusOpen {
		t.Fatal("not open after unseal")
	}
}

func TestUnsealDatabase_ClearFailureSealsAgain(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, secret := newClusterDatabase(t, sealer, "secure")
	if err := svc.SealDatabase(opsCtx(), "secure"); err != nil {
		t.Fatal(err)
	}
	boom := &clusterseal.RecordNotConfirmedError{Barrier: clusterseal.DatabaseBarrier(tenantid.System, "secure"), Op: "clear", Err: errors.New("no quorum")}
	sealer.SetClearErr(boom)
	if err := svc.UnsealDatabase(opsCtx(), "secure", seal.MustCredentials(context.Background(), bytes.Clone(secret))); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if dbStatus(t, svc, "secure") != database.StatusSealed {
		t.Fatal("left open while the record still says sealed")
	}
	st, err := svc.DatabaseBarrierStatus(opsCtx(), "secure")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Sealed {
		t.Fatal("barrier left unsealed")
	}
}

func TestDatabaseBarrierStatus_CarriesTheClusterSeal(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, _ := newClusterDatabase(t, sealer, "secure")
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier(tenantid.System, "secure"), Epoch: 2, Sealed: true})
	st, err := svc.DatabaseBarrierStatus(opsCtx(), "secure")
	if err != nil {
		t.Fatal(err)
	}
	if st.ClusterSeal == nil || !st.ClusterSeal.Sealed || st.ClusterSeal.Epoch != 2 {
		t.Fatalf("ClusterSeal = %+v", st.ClusterSeal)
	}
}

// TestDatabaseBarrierOps_OwnerOnly: the system tenant seals its own
// databases and no other tenant's, and a tenant cannot reach another's. Both
// are answered as if the database did not exist. Before, the system tenant
// skipped the ownership check altogether.
func TestDatabaseBarrierOps_OwnerOnly(t *testing.T) {
	svc, secret := newClusterDatabase(t, clustersealtest.New(true), "secure")
	acme := tenant.WithTenantID(context.Background(), "acme")
	if _, err := svc.CreateForTenant(acme, testDBConfig("acme-db")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		ctx  context.Context
		db   string
	}{
		{"system on a tenant's database", opsCtx(), "acme-db"},
		{"tenant on the system's database", acme, "secure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nf *database.NotFoundError
			if err := svc.SealDatabase(tc.ctx, tc.db); !errors.As(err, &nf) {
				t.Errorf("seal err = %v, want *database.NotFoundError", err)
			}
			if err := svc.UnsealDatabase(tc.ctx, tc.db, seal.MustCredentials(context.Background(), bytes.Clone(secret))); !errors.As(err, &nf) {
				t.Errorf("unseal err = %v, want *database.NotFoundError", err)
			}
			if _, err := svc.DatabaseBarrierStatus(tc.ctx, tc.db); !errors.As(err, &nf) {
				t.Errorf("status err = %v, want *database.NotFoundError", err)
			}
		})
	}
	if dbStatus(t, svc, "secure") != database.StatusOpen {
		t.Fatal("a refused seal sealed the database")
	}
}

// TestSealDatabaseLocal_SealsOnlyTheOwnersDatabase pins the enforcement side
// of a database seal record to the database's owner. A record names a tenant
// and a database; database names are reused once dropped, so a record left by
// one tenant must never seal a database another tenant created later under the
// same name. A database another tenant owns is not held here, exactly as a
// missing one is.
func TestSealDatabaseLocal_SealsOnlyTheOwnersDatabase(t *testing.T) {
	svc, _ := newClusterDatabase(t, clustersealtest.New(true), "secure")
	var nf *database.NotFoundError
	if err := svc.SealDatabaseLocal("acme", "secure"); !errors.As(err, &nf) {
		t.Fatalf("another tenant's record: err = %v, want *database.NotFoundError", err)
	}
	if dbStatus(t, svc, "secure") != database.StatusOpen {
		t.Fatal("another tenant's record sealed the database")
	}
	if err := svc.SealDatabaseLocal(tenantid.System, "secure"); err != nil {
		t.Fatal(err)
	}
	if dbStatus(t, svc, "secure") != database.StatusSealed {
		t.Fatal("the owner's record did not seal the database")
	}
	if err := svc.SealDatabaseLocal(tenantid.System, "missing"); !errors.As(err, &nf) {
		t.Fatalf("missing database: err = %v, want *database.NotFoundError", err)
	}
}

// TestCreateDatabase_ClearsTheStaleRecordOfADroppedDatabase pins that a
// database starts from a clear record. A database dropped while sealed leaves
// its record sealed, and records are never deleted, so a database created
// again under the same name by the same tenant would otherwise be refused from
// birth -- and could never be unsealed, its new barrier being unsealed
// already. Creating it clears the record its predecessor left.
func TestCreateDatabase_ClearsTheStaleRecordOfADroppedDatabase(t *testing.T) {
	sealer := clustersealtest.New(true)
	b := clusterseal.DatabaseBarrier(tenantid.System, "secure")
	sealer.Put(clusterseal.Record{Barrier: b, Epoch: 3, Sealed: true})
	svc, _ := newClusterDatabase(t, sealer, "secure")
	if got := sealer.Clears(); len(got) != 1 || got[0] != b {
		t.Fatalf("clears = %v, want [%v]", got, b)
	}
	if sealer.Refuses(b) {
		t.Fatal("a new database is refused by its predecessor's record")
	}
	if dbStatus(t, svc, "secure") != database.StatusOpen {
		t.Fatal("the new database is not open")
	}
}

// TestCreateDatabase_StaleRecordClearFailureRollsBack pins the failure side:
// when the stale record cannot be cleared the create is rolled back, so a
// database is never served under a sealed record and a failed create leaves
// nothing behind to retry around.
func TestCreateDatabase_StaleRecordClearFailureRollsBack(t *testing.T) {
	sealer := clustersealtest.New(true)
	b := clusterseal.DatabaseBarrier(tenantid.System, "secure")
	sealer.Put(clusterseal.Record{Barrier: b, Epoch: 3, Sealed: true})
	boom := &clusterseal.RecordNotConfirmedError{Barrier: b, Op: "clear", Err: errors.New("no quorum")}
	sealer.SetClearErr(boom)
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	svc.sealer = sealer
	cfg := testDBConfig("secure")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("db-secret"))); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	var nf *database.NotFoundError
	if _, err := svc.Get("secure"); !errors.As(err, &nf) {
		t.Fatalf("a create whose stale record could not be cleared left the database: %v", err)
	}
}

// TestCreateForTenant_ClearsTheStaleRecordOfADroppedDatabase is the tenant
// create path: the record cleared is the creating tenant's, and its failure
// rolls the create back.
func TestCreateForTenant_ClearsTheStaleRecordOfADroppedDatabase(t *testing.T) {
	acme := tenant.WithTenantID(context.Background(), "acme")
	b := clusterseal.DatabaseBarrier("acme", "orders")
	t.Run("cleared", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		sealer.Put(clusterseal.Record{Barrier: b, Epoch: 2, Sealed: true})
		svc, _ := newTestDatabaseServiceWithBarrier(t)
		svc.sealer = sealer
		if _, err := svc.CreateForTenant(acme, testDBConfig("orders")); err != nil {
			t.Fatal(err)
		}
		if got := sealer.Clears(); len(got) != 1 || got[0] != b {
			t.Fatalf("clears = %v, want [%v]", got, b)
		}
	})
	t.Run("clear fails", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		sealer.Put(clusterseal.Record{Barrier: b, Epoch: 2, Sealed: true})
		boom := &clusterseal.SealEpochConflictError{Barrier: b}
		sealer.SetClearErr(boom)
		svc, _ := newTestDatabaseServiceWithBarrier(t)
		svc.sealer = sealer
		if _, err := svc.CreateForTenant(acme, testDBConfig("orders")); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		var nf *database.NotFoundError
		if _, err := svc.Get("orders"); !errors.As(err, &nf) {
			t.Fatalf("the failed create left the database: %v", err)
		}
	})
	t.Run("another tenant's record is untouched", func(t *testing.T) {
		sealer := clustersealtest.New(true)
		other := clusterseal.DatabaseBarrier("globex", "orders")
		sealer.Put(clusterseal.Record{Barrier: other, Epoch: 2, Sealed: true})
		svc, _ := newTestDatabaseServiceWithBarrier(t)
		svc.sealer = sealer
		if _, err := svc.CreateForTenant(acme, testDBConfig("orders")); err != nil {
			t.Fatal(err)
		}
		if got := sealer.Clears(); slices.Contains(got, other) || !sealer.Refuses(other) {
			t.Fatalf("clears = %v; another tenant's record was touched", got)
		}
	})
}

// TestSealDatabase_NoBarrierWritesNoRecord pins that a database without its
// own barrier is never recorded sealed: there is no key to wipe and none to
// unseal with, so such a record could never be cleared.
func TestSealDatabase_NoBarrierWritesNoRecord(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	svc.sealer = sealer
	if _, err := svc.Create(testDBConfig("plain")); err != nil {
		t.Fatal(err)
	}
	if err := svc.SealDatabase(opsCtx(), "plain"); err == nil {
		t.Fatal("sealed a database that has no barrier")
	}
	if got := sealer.Seals(); len(got) != 0 {
		t.Fatalf("seals = %v; a record was written for a database with no barrier", got)
	}
}

// TestCreateDatabase_ClearsWhateverRecordTheNameHas pins that every created
// database clears its name's record through the sealer, whatever this node's
// view says: a sealed predecessor record may be one this node has not read
// yet, and a database with no barrier of its own could never be unsealed out
// of one. The sealer writes nothing for an absent or clear record.
func TestCreateDatabase_ClearsWhateverRecordTheNameHas(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	svc.sealer = sealer
	if _, err := svc.Create(testDBConfig("plain")); err != nil {
		t.Fatal(err)
	}
	b := clusterseal.DatabaseBarrier(tenantid.System, "plain")
	if got := sealer.Clears(); len(got) != 1 || got[0] != b {
		t.Fatalf("clears = %v, want [%v]", got, b)
	}
	if rec := sealer.Record(b); rec.Epoch != 0 {
		t.Fatalf("record = %+v, want nothing written for a name with no record", rec)
	}
}

// TestUnsealDatabase_ClearsARecordThisNodeHasNotSeen pins that an unseal
// always asks the sealer to clear the database's record: this node's view of
// the records can lag a seal written through another node, and an unseal that
// skipped the clear on that view would leave the record sealed, so the next
// read would seal the database here again.
func TestUnsealDatabase_ClearsARecordThisNodeHasNotSeen(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, secret := newClusterDatabase(t, sealer, "secure")
	if err := svc.SealDatabaseLocal(tenantid.System, "secure"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnsealDatabase(opsCtx(), "secure", seal.MustCredentials(context.Background(), bytes.Clone(secret))); err != nil {
		t.Fatal(err)
	}
	b := clusterseal.DatabaseBarrier(tenantid.System, "secure")
	if got := sealer.Clears(); len(got) < 1 || got[len(got)-1] != b {
		t.Fatalf("clears = %v, want the unseal to clear %v", got, b)
	}
}
