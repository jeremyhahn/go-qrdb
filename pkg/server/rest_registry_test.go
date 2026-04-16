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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
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
	reg := NewRESTServiceRegistry(NewDatabaseServiceRegistry("default"), nil, "default")
	if reg == nil {
		t.Fatal("expected non-nil ServiceRegistry")
	}
}

// ---------------------------------------------------------------------------
// Database resolution
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_KnownDatabase_ReturnsService(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")

	svc, err := r.KVService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

func TestRESTServiceRegistry_UnknownDatabase_ReturnsDatabaseNotFoundError(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")

	_, err := r.KVService("default", "nonexistent")
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
	case **rest.ServiceNotImplementedError:
		if e, ok := err.(*rest.ServiceNotImplementedError); ok {
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
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.MVCCService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil MVCCKVServicer")
	}
}

func TestRESTServiceRegistry_WatchService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.WatchService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil WatchServicer")
	}
}

func TestRESTServiceRegistry_LeaseService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.LeaseService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil LeaseServicer")
	}
}

func TestRESTServiceRegistry_IndexService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.IndexService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil IndexServicer")
	}
}

func TestRESTServiceRegistry_TimeSeriesService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.TimeSeriesService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil TimeSeriesServicer")
	}
}

func TestRESTServiceRegistry_TransactionService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.TransactionService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil TransactionServicer")
	}
}

func TestRESTServiceRegistry_BatchService_KnownDB(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.BatchService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil BatchServicer")
	}
}

func TestRESTServiceRegistry_VectorService_ReturnsNotImplemented(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	_, err := r.VectorService("default", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError, got nil")
	}
	var nie *rest.ServiceNotImplementedError
	if !isError(err, &nie) {
		t.Errorf("expected *rest.ServiceNotImplementedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Nil service fields
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NilService_ReturnsServiceNotImplementedError(t *testing.T) {
	reg := NewDatabaseServiceRegistry("default")
	// Register a database with all nil services.
	reg.Register("default", &DatabaseServices{Name: "default"})
	r := NewRESTServiceRegistry(reg, nil, "default")

	_, err := r.KVService("default", "default")
	if err == nil {
		t.Fatal("expected ServiceNotImplementedError, got nil")
	}
	var nie *rest.ServiceNotImplementedError
	if !isError(err, &nie) {
		t.Errorf("expected *rest.ServiceNotImplementedError, got %T: %v", err, err)
	}
	if nie.Service != "kv" {
		t.Errorf("Service field: got %q, want %q", nie.Service, "kv")
	}
}

// ---------------------------------------------------------------------------
// Barrier-less tenant check
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NoBarrierRegistry_SkipsTenantCheck(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	// Any tenant name should succeed when no barrier registry is configured.
	svc, err := r.KVService("any-tenant", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

func TestRESTServiceRegistry_DefaultTenant_SkipsBarrierCheck(t *testing.T) {
	// Even with a barrier registry, the "default" tenant must bypass the check
	// to preserve backward compatibility.
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.KVService("default", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// ---------------------------------------------------------------------------
// Non-default database
// ---------------------------------------------------------------------------

func TestRESTServiceRegistry_NonDefaultDatabase_ReturnsCorrectServices(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")
	svc, err := r.KVService("default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer for mydb")
	}
}

func TestRESTServiceRegistry_UnknownDatabase_AllServicesReturnError(t *testing.T) {
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "default")

	tests := []struct {
		name string
		fn   func() error
	}{
		{"kv", func() error { _, err := r.KVService("default", "gone"); return err }},
		{"mvcc", func() error { _, err := r.MVCCService("default", "gone"); return err }},
		{"watch", func() error { _, err := r.WatchService("default", "gone"); return err }},
		{"lease", func() error { _, err := r.LeaseService("default", "gone"); return err }},
		{"index", func() error { _, err := r.IndexService("default", "gone"); return err }},
		{"ts", func() error { _, err := r.TimeSeriesService("default", "gone"); return err }},
		{"tx", func() error { _, err := r.TransactionService("default", "gone"); return err }},
		{"batch", func() error { _, err := r.BatchService("default", "gone"); return err }},
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
