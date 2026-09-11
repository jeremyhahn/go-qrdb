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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedCollectionStub is the narrowest CollectionServicer that can answer
// "which servicer did the handler reach?". Only CreateCollection and
// GetCollectionInfo are implemented; the rest come from the embedded nil
// interface.
type scopedCollectionStub struct {
	service.CollectionServicer
	created []string
	read    []string
}

// CreateCollection records the database it was asked to create in.
func (s *scopedCollectionStub) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	s.created = append(s.created, req.Database+"/"+req.Name)
	return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
}

// GetCollectionInfo records the (database, name) pair it was asked about.
func (s *scopedCollectionStub) GetCollectionInfo(_ context.Context, database, name string) (*service.CollectionInfo, error) {
	s.read = append(s.read, database+"/"+name)
	return &service.CollectionInfo{Name: name, Database: database}, nil
}

// collectionScopeRegistry is a service.Registry stub that records the (tenant,
// database) pair CollectionService was asked to resolve.
type collectionScopeRegistry struct {
	service.Registry
	svc       service.CollectionServicer
	err       error
	gotTenant string
	gotDB     string
	calls     int
}

// CollectionService records the resolution request and returns the configured
// servicer or error.
func (r *collectionScopeRegistry) CollectionService(tenantID, db string) (service.CollectionServicer, error) {
	r.calls++
	r.gotTenant, r.gotDB = tenantID, db
	return r.svc, r.err
}

// TestRESTCollectionResolvesThroughTheRegistry holds the transport half of
// go-qrdb#441 on REST: with a registry configured, a collection request must
// resolve the servicer for the caller's tenant and the database the REQUEST
// names -- the body for POST /v1/collections -- and must not reach the
// process-wide singleton.
func TestRESTCollectionResolvesThroughTheRegistry(t *testing.T) {
	singleton := &scopedCollectionStub{}
	scoped := &scopedCollectionStub{}
	reg := &collectionScopeRegistry{svc: scoped}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(singleton), WithServiceRegistry(reg))

	body := `{"name":"users","database":"db2"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	if len(singleton.created) != 0 {
		t.Fatalf("the process-wide singleton was used: %v", singleton.created)
	}
	if len(scoped.created) != 1 || scoped.created[0] != "db2/users" {
		t.Fatalf("scoped servicer saw %v, want [db2/users]", scoped.created)
	}
	if reg.gotTenant != "acme" || reg.gotDB != "db2" {
		t.Fatalf("registry resolved (%q, %q), want (acme, db2)", reg.gotTenant, reg.gotDB)
	}
}

// TestRESTCollectionPathRoutesResolveTheirOwnDatabase pins that the
// path-addressed routes resolve the database from the PATH rather than from
// the X-QRDB-Database header. The header names a different database on
// purpose: a collection request addresses the database it spells out, and
// resolving the header's instead would put the documents in a third place.
func TestRESTCollectionPathRoutesResolveTheirOwnDatabase(t *testing.T) {
	scoped := &scopedCollectionStub{}
	reg := &collectionScopeRegistry{svc: scoped}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithServiceRegistry(reg))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/db2/users", nil)
	req.Header.Set("X-QRDB-Database", "otherdb")
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if reg.gotDB != "db2" {
		t.Fatalf("registry resolved database %q, want the path's db2", reg.gotDB)
	}
	if len(scoped.read) != 1 || scoped.read[0] != "db2/users" {
		t.Fatalf("scoped servicer saw %v, want [db2/users]", scoped.read)
	}
}

// TestRESTCollectionFallsBackToTheSingleton covers the registry-less
// composition, where the singleton is correct and must still be served.
func TestRESTCollectionFallsBackToTheSingleton(t *testing.T) {
	singleton := &scopedCollectionStub{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(singleton))

	body := `{"name":"users","database":"db2"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	if len(singleton.created) != 1 || singleton.created[0] != "db2/users" {
		t.Fatalf("singleton saw %v, want [db2/users]", singleton.created)
	}
}

// TestRESTCollectionResolveFailurePropagates pins that a refused resolution --
// an unknown tenant, an unknown database, a sealed barrier -- reaches the
// client as that error, rather than silently falling back to a servicer whose
// keyspace the caller was refused.
func TestRESTCollectionResolveFailurePropagates(t *testing.T) {
	singleton := &scopedCollectionStub{}
	reg := &collectionScopeRegistry{err: &DatabaseNotFoundError{TenantID: "acme", Database: "db2"}}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(singleton), WithServiceRegistry(reg))

	body := `{"name":"users","database":"db2"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
	if len(singleton.created) != 0 {
		t.Fatalf("a refused resolution fell back to the singleton: %v", singleton.created)
	}
}

// TestRESTCollectionWithNeitherIsRefused pins the fail-closed case: no
// registry and no singleton reports not-implemented rather than dereferencing
// nil.
func TestRESTCollectionWithNeitherIsRefused(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	rec := httptest.NewRecorder()
	if svc := srv.collectionSvcFor(rec, context.Background(), "db2"); svc != nil {
		t.Fatalf("expected nil, got %T", svc)
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if body := rec.Body.String(); !strings.Contains(body, (&ServiceNotImplementedError{Service: "collection"}).Error()) {
		t.Fatalf("response body %q does not carry the collection not-implemented error", body)
	}
}
