package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
)

// admissionEnts is a quota.EntitlementChecker granting FeatureSQLiteVFS to
// the tenants in granted, answering as tenant.Service.CheckFeature does.
type admissionEnts struct {
	granted map[string]bool
}

func (e *admissionEnts) CheckIsolation(context.Context, string, string) error { return nil }

func (e *admissionEnts) CheckFeature(_ context.Context, tenantID, feature string) error {
	ents := quota.Entitlements{AllowSQLiteVFS: e.granted[tenantID]}
	if ents.AllowsFeature(feature) {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
}

func (e *admissionEnts) CheckBarrierMode(context.Context, string, string) error { return nil }

func (e *admissionEnts) CheckVolumePool(context.Context, string, string) error { return nil }

// admissionQuotas is a quota.QuotaProvider giving every tenant the same
// MaxSQLiteDatabases.
type admissionQuotas struct{ max int64 }

func (p admissionQuotas) Quotas(context.Context, string) (*quota.Quotas, error) {
	return &quota.Quotas{MaxSQLiteDatabases: p.max}, nil
}

// admissionScene is one isolation scene's two tenant page stores, the
// entitlements, and a real quota.Service whose ResourceSQLiteDatabases
// counter is sqlitevfs.CountDatabases over the asking tenant's own Backend.
type admissionScene struct {
	a, b *wiringStore
	ents *admissionEnts
	lim  *quota.Service
}

func newAdmissionScene(t *testing.T, scene *lockScene, maxDBs int64) *admissionScene {
	t.Helper()
	s := &admissionScene{
		a:    newWiringStore(t, scene.registry, lockTenantA, lockHolderA),
		b:    newWiringStore(t, scene.registry, lockTenantB, lockHolderB),
		ents: &admissionEnts{granted: map[string]bool{}},
	}
	lim, err := quota.NewService(admissionQuotas{max: maxDBs}, nil)
	if err != nil {
		t.Fatalf("quota.NewService: %v", err)
	}
	backends := map[string]sqlitevfs.Backend{lockTenantA: s.a.backend, lockTenantB: s.b.backend}
	lim.RegisterCounter(quota.ResourceSQLiteDatabases, func(ctx context.Context, tenantID string) (int64, error) {
		b, ok := backends[tenantID]
		if !ok {
			return 0, errors.New("no page store for tenant " + tenantID)
		}
		return sqlitevfs.CountDatabases(ctx, b)
	})
	s.lim = lim
	return s
}

func (s *admissionScene) admit(t *testing.T, w *wiringStore, tenantID, db string) error {
	t.Helper()
	return sqlitevfs.Admit(t.Context(), w.backend, tenantID, db, s.ents, s.lim)
}

func admissionCount(t *testing.T, w *wiringStore) int64 {
	t.Helper()
	n, err := sqlitevfs.CountDatabases(t.Context(), w.backend)
	if err != nil {
		t.Fatalf("CountDatabases: %v", err)
	}
	return n
}

// createSecondDatabase commits one page to a second database of w's tenant
// through a Store over the same Backend.
func createSecondDatabase(t *testing.T, w *wiringStore, db string) {
	t.Helper()
	s, err := sqlitevfs.NewStore(w.backend, db)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", db, err)
	}
	txn, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin(%q): %v", db, err)
	}
	if _, err := txn.WriteAt(make([]byte, wiringPageSize), 0); err != nil {
		t.Fatalf("WriteAt(%q): %v", db, err)
	}
	if _, err := txn.Commit(t.Context()); err != nil {
		t.Fatalf("Commit(%q): %v", db, err)
	}
}

// TestSQLiteVFSAdmission_EntitlementOffRefusesAndWritesNothing: a tenant
// without the sqlite-vfs entitlement is refused at Admit with the
// *quota.EntitlementError unchanged, and its page store stays empty.
func TestSQLiteVFSAdmission_EntitlementOffRefusesAndWritesNothing(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			s := newAdmissionScene(t, build(t), 5)
			before, err := s.a.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			err = s.admit(t, s.a, lockTenantA, wiringDB)
			var denied *quota.EntitlementError
			if !errors.As(err, &denied) || denied.Feature != quota.FeatureSQLiteVFS || denied.TenantID != lockTenantA {
				t.Fatalf("Admit without entitlement = %v, want *quota.EntitlementError for %s", err, quota.FeatureSQLiteVFS)
			}
			if n := admissionCount(t, s.a); n != 0 {
				t.Errorf("CountDatabases after refusal = %d, want 0", n)
			}
			after, err := s.a.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			if after != before {
				t.Errorf("revision moved from %d to %d on a refused Admit", before, after)
			}
		})
	}
}

// TestSQLiteVFSAdmission_CapCountsOnlyTheTenantsOwnDatabases: granted and
// under the cap a tenant is admitted, its first commit creates the
// database, and at the cap a second database is refused while the first
// still admits for reopening; the other tenant's databases never count.
func TestSQLiteVFSAdmission_CapCountsOnlyTheTenantsOwnDatabases(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			s := newAdmissionScene(t, scene, 1)
			s.ents.granted[lockTenantA] = true
			s.ents.granted[lockTenantB] = true

			// Tenant B fills its own cap first.
			if err := s.admit(t, s.b, lockTenantB, wiringDB); err != nil {
				t.Fatalf("Admit B: %v", err)
			}
			s.b.commitPages(t, 0x61)
			if n := admissionCount(t, s.b); n != 1 {
				t.Fatalf("tenant B CountDatabases = %d, want 1", n)
			}

			// A's count is its own: B's full cap does not refuse A.
			if n := admissionCount(t, s.a); n != 0 {
				t.Fatalf("tenant A CountDatabases = %d before any commit, want 0 (B's database counted)", n)
			}
			if err := s.admit(t, s.a, lockTenantA, wiringDB); err != nil {
				t.Fatalf("Admit A under cap: %v", err)
			}
			if n := admissionCount(t, s.a); n != 0 {
				t.Fatalf("Admit created a database: count %d, want 0", n)
			}
			s.a.commitPages(t, 0x51)
			if n := admissionCount(t, s.a); n != 1 {
				t.Fatalf("tenant A CountDatabases after first commit = %d, want 1", n)
			}

			// At the cap a second database is refused, the first reopens.
			err := s.admit(t, s.a, lockTenantA, "second")
			var exceeded *quota.ExceededError
			if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceSQLiteDatabases {
				t.Fatalf("Admit A at cap = %v, want *quota.ExceededError for %s", err, quota.ResourceSQLiteDatabases)
			}
			if err := s.admit(t, s.a, lockTenantA, wiringDB); err != nil {
				t.Fatalf("Admit A reopening its database at cap: %v", err)
			}

			// Revoking the entitlement does not stop reopening.
			s.ents.granted[lockTenantA] = false
			if err := s.admit(t, s.a, lockTenantA, wiringDB); err != nil {
				t.Fatalf("Admit A reopening without the entitlement: %v", err)
			}

			assertWiringKeysSeparated(t, name, scene.rawKeys(t))
		})
	}
}

// TestSQLiteVFSAdmission_CountsEachDatabaseOnce: a tenant's second database
// raises its count to two, read off the tenant's own keyspace.
func TestSQLiteVFSAdmission_CountsEachDatabaseOnce(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			s := newAdmissionScene(t, build(t), 2)
			s.ents.granted[lockTenantA] = true
			s.a.commitPages(t, 0x51, 0x52)
			if err := s.admit(t, s.a, lockTenantA, "second"); err != nil {
				t.Fatalf("Admit second under cap: %v", err)
			}
			createSecondDatabase(t, s.a, "second")
			if n := admissionCount(t, s.a); n != 2 {
				t.Fatalf("CountDatabases = %d, want 2", n)
			}
			if n := admissionCount(t, s.b); n != 0 {
				t.Fatalf("tenant B CountDatabases = %d, want 0", n)
			}
			var exceeded *quota.ExceededError
			if err := s.admit(t, s.a, lockTenantA, "third"); !errors.As(err, &exceeded) {
				t.Fatalf("Admit third at cap = %v, want *quota.ExceededError", err)
			}
		})
	}
}
