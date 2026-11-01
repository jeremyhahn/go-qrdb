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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newFilledRegistry returns a DatabaseServiceRegistry with a "default" database
// that has stub non-nil services so that service-presence checks pass.
func newFilledRegistry() *DatabaseServiceRegistry {
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", newFilledServices("default"))
	reg.Register("mydb", newFilledServices("mydb"))
	return reg
}

// newFilledServices builds a DatabaseServices where all service fields are
// set to a non-nil stub so that type assertions in the registry pass.
// The stubs are the same embedded-interface stubs defined in server_test.go.
func newFilledServices(name string) *DatabaseServices {
	return &DatabaseServices{
		Name:        name,
		KV:          newMockKVService(),
		MVCC:        &stubMVCCService{},
		Batch:       &stubBatchService{},
		Transaction: &stubTransactionService{},
		Lease:       &stubLeaseService{},
		TimeSeries:  &stubTimeSeriesService{},
		Index:       &stubIndexService{},
		Watch:       &stubWatchService{},
	}
}

// ---------------------------------------------------------------------------
// NewRESTServiceRegistry – construction
// ---------------------------------------------------------------------------

func TestNewRESTServiceRegistry_ReturnsNonNil(t *testing.T) {
	reg := NewRESTServiceRegistry(NewDatabaseServiceRegistry("default"), nil, "system")
	if reg == nil {
		t.Fatal("expected non-nil ServiceRegistry")
	}
}

// ---------------------------------------------------------------------------
// Database resolution
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_KnownDatabase_ReturnsService(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")

	svc, err := r.KVService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

func TestRESTServiceRegistry_UnknownDatabase_ReturnsDatabaseNotFoundError(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")

	_, err := r.KVService("system", "nonexistent")
	if err == nil {
		t.Fatal("expected DatabaseNotFoundError, got nil")
	}
	var dnf *rest.DatabaseNotFoundError
	if !isError(err, &dnf) {
		t.Errorf("expected *rest.DatabaseNotFoundError, got %T: %v", err, err)
	}
	if dnf.Database != "nonexistent" {
		t.Errorf("Database field: got %q, want %q", dnf.Database, "nonexistent")
	}
}

// isError is a local type-assert helper to avoid importing errors.As indirectly.
func isError(err error, target interface{}) bool {
	// Use errors.As-equivalent via interface matching.
	switch t := target.(type) {
	case **rest.DatabaseNotFoundError:
		if e, ok := err.(*rest.DatabaseNotFoundError); ok {
			*t = e
			return true
		}
	case **rest.TenantNotFoundError:
		if e, ok := err.(*rest.TenantNotFoundError); ok {
			*t = e
			return true
		}
	case **rest.BarrierSealedError:
		if e, ok := err.(*rest.BarrierSealedError); ok {
			*t = e
			return true
		}
	case **service.CapabilityUnsupportedError:
		if e, ok := err.(*service.CapabilityUnsupportedError); ok {
			*t = e
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Per-service resolution
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_MVCCService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.MVCCService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil MVCCKVServicer")
	}
}

func TestRESTServiceRegistry_WatchService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.WatchService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil WatchServicer")
	}
}

func TestRESTServiceRegistry_LeaseService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.LeaseService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil LeaseServicer")
	}
}

func TestRESTServiceRegistry_IndexService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.IndexService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil IndexServicer")
	}
}

func TestRESTServiceRegistry_TimeSeriesService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.TimeSeriesService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil TimeSeriesServicer")
	}
}

func TestRESTServiceRegistry_TransactionService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.TransactionService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil TransactionServicer")
	}
}

func TestRESTServiceRegistry_BatchService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.BatchService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil BatchServicer")
	}
}

func TestRESTServiceRegistry_VectorService_ReturnsNotImplemented(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	_, err := r.VectorService("system", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError, got nil")
	}
	var cue *service.CapabilityUnsupportedError
	if !isError(err, &cue) {
		t.Errorf("expected *service.CapabilityUnsupportedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Nil service fields
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NilService_ReturnsServiceNotImplementedError(t *testing.T) {
	reg := NewDatabaseServiceRegistry("default")
	// Register a database with all nil services.
	reg.Register("default", &DatabaseServices{Name: "default"})
	r := NewRESTServiceRegistry(reg, nil, "system")

	_, err := r.KVService("system", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError, got nil")
	}
	var cue *service.CapabilityUnsupportedError
	if !isError(err, &cue) {
		t.Errorf("expected *service.CapabilityUnsupportedError, got %T: %v", err, err)
	}
	if !strings.HasPrefix(cue.Message, "kv: ") {
		t.Errorf("message %q must name the refused service kv", cue.Message)
	}
}

// ---------------------------------------------------------------------------
// Barrier-less tenant check
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NoBarrierRegistry_SkipsTenantCheck(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	// Any tenant name should succeed when no barrier registry is configured.
	svc, err := r.KVService("any-tenant", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

func TestRESTServiceRegistry_SystemTenant_SkipsBarrierCheck(t *testing.T) {
	// Even with a barrier registry, the system tenant must bypass the check
	// to preserve backward compatibility.
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.KVService("system", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// TestCheckTenantBarrier_TenantNotFound verifies that a tenant with no barrier
// in the registry is refused with a typed *rest.TenantNotFoundError.
func TestCheckTenantBarrier_TenantNotFound(t *testing.T) {
	t.Parallel()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("check-barrier-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(nil, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "sys/root"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(nil, sysBarrier)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	r := &restServiceRegistry{barrierReg: reg, systemTenant: "system"}

	var tnf *rest.TenantNotFoundError
	if err := r.checkTenantBarrier("ghost-tenant"); !errors.As(err, &tnf) || tnf.TenantID != "ghost-tenant" {
		t.Fatalf("checkTenantBarrier(unregistered) = %v, want *rest.TenantNotFoundError for ghost-tenant", err)
	}
}

// ---------------------------------------------------------------------------
// Non-default database
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NonDefaultDatabase_ReturnsCorrectServices(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	svc, err := r.KVService("system", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer for mydb")
	}
}

// ---------------------------------------------------------------------------
// resolveTenant with tenantSvc set
// ---------------------------------------------------------------------------

// TestRESTServiceRegistry_WithTenantSvc_KnownTenant verifies that when a
// tenant service is configured and GetTenant succeeds, the request is routed
// correctly using the effective isolation type.
func TestRESTServiceRegistry_WithTenantSvc_KnownTenant(t *testing.T) {
	r := NewRESTServiceRegistry(
		newFilledRegistry(),
		nil,
		"system",
		WithTenantServicer(&knownTenantServicer{}),
	)
	svc, err := r.KVService("sometenant", "default")
	if err != nil {
		t.Fatalf("KVService for known tenant: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// knownTenantServicer returns a valid TenantInfo from GetTenant.
type knownTenantServicer struct{ mockTenantServicer }

func (k *knownTenantServicer) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{
		ID:            tenantID,
		Name:          tenantID,
		IsolationType: tenant.IsolationPrefix,
	}, nil
}

// TestRESTServiceRegistry_WithTenantSvc_SystemTenantLookupFail verifies that
// when tenantSvc is set and GetTenant returns an error for the system tenant,
// the registry falls back to the barrier check (which succeeds with no barrier).
func TestRESTServiceRegistry_WithTenantSvc_SystemTenantFallback(t *testing.T) {
	// errTenantSvc always returns an error from GetTenant.
	r := NewRESTServiceRegistry(
		newFilledRegistry(),
		nil,
		"system",
		WithTenantServicer(&errTenantServicer{}),
	)
	// system tenant error → falls back to checkTenantBarrier(nil) → no error.
	svc, err := r.KVService("system", "default")
	if err != nil {
		t.Fatalf("system tenant fallback should succeed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// errTenantServicer always returns an error from GetTenant.
type errTenantServicer struct{ mockTenantServicer }

func (e *errTenantServicer) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, tenant.ErrTenantNotFound
}

// TestRESTServiceRegistry_VectorService_WithVectorField verifies that
// VectorService returns ServiceNotImplemented when Vector field is nil
// (default in newFilledServices) and succeeds when set.
func TestRESTServiceRegistry_VectorService_NilVector_NotImplemented(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")
	_, err := r.VectorService("system", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError when Vector is nil, got nil")
	}
	var cue *service.CapabilityUnsupportedError
	if !isError(err, &cue) {
		t.Errorf("expected *service.CapabilityUnsupportedError, got %T: %v", err, err)
	}
	if !strings.HasPrefix(cue.Message, "vector: ") {
		t.Errorf("message %q must name the refused service vector", cue.Message)
	}
}

// TestRESTServiceRegistry_VectorService_WithVector verifies that VectorService
// returns a non-nil VectorServicer when the Vector field is populated.
func TestRESTServiceRegistry_VectorService_Set(t *testing.T) {
	reg := NewDatabaseServiceRegistry("default")
	svcs := newFilledServices("default")
	svcs.Vector = &stubVectorService{}
	reg.Register("default", svcs)

	r := NewRESTServiceRegistry(reg, nil, "system")
	svc, err := r.VectorService("system", "default")
	if err != nil {
		t.Fatalf("VectorService: unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil VectorServicer")
	}
}

// TestRESTServiceRegistry_ApplyNamespace_NonSystemTenant verifies that a
// non-system tenant gets a namespace-prefixed KV service.
func TestRESTServiceRegistry_ApplyNamespace_NonSystemTenant(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")

	// Requesting "othertenant"/"default" without a barrierReg or tenantSvc:
	// checkTenantBarrier returns nil (no barrier configured), so resolve
	// should succeed and return a namespacedKVService.
	svc, err := r.KVService("othertenant", "default")
	if err != nil {
		t.Fatalf("KVService for non-system tenant: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer for non-system tenant")
	}
	// It should be a namespacedKVService (not the raw mock).
	if _, ok := svc.(*namespacedKVService); !ok {
		t.Errorf("expected *namespacedKVService, got %T", svc)
	}
}

// TestRESTServiceRegistry_ApplyNamespace_NilKV verifies applyNamespace
// returns the original DatabaseServices unchanged when KV is nil.
func TestRESTServiceRegistry_ApplyNamespace_NilKV(t *testing.T) {
	reg := NewDatabaseServiceRegistry("default")
	svcs := &DatabaseServices{Name: "default", MVCC: &stubMVCCService{}}
	reg.Register("default", svcs)

	r := NewRESTServiceRegistry(reg, nil, "system")

	// Non-system tenant should not panic even with nil KV.
	_, err := r.KVService("othertenant", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError when KV is nil, got nil")
	}
	var nie *service.CapabilityUnsupportedError
	if !isError(err, &nie) {
		t.Errorf("expected ServiceNotImplementedError, got %T: %v", err, err)
	}
}

func TestRESTServiceRegistry_UnknownDatabase_AllServicesReturnError(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system")

	tests := []struct {
		name string
		fn   func() error
	}{
		{"kv", func() error { _, err := r.KVService("system", "gone"); return err }},
		{"mvcc", func() error { _, err := r.MVCCService("system", "gone"); return err }},
		{"watch", func() error { _, err := r.WatchService("system", "gone"); return err }},
		{"lease", func() error { _, err := r.LeaseService("system", "gone"); return err }},
		{"index", func() error { _, err := r.IndexService("system", "gone"); return err }},
		{"ts", func() error { _, err := r.TimeSeriesService("system", "gone"); return err }},
		{"tx", func() error { _, err := r.TransactionService("system", "gone"); return err }},
		{"batch", func() error { _, err := r.BatchService("system", "gone"); return err }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatalf("expected error for unknown database, got nil")
			}
			var dnf *rest.DatabaseNotFoundError
			if !isError(err, &dnf) {
				t.Errorf("expected *rest.DatabaseNotFoundError, got %T: %v", err, err)
			}
		})
	}
}
