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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedLockStub is the narrowest LockServicer that can answer "which servicer
// did the handler reach, and with what?".
type scopedLockStub struct {
	service.LockServicer
	calls []string
}

func (s *scopedLockStub) Lock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "lock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) TryLock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "trylock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) Unlock(_ context.Context, key string, _ int64) error {
	s.calls = append(s.calls, "unlock:"+key)
	return nil
}

func (s *scopedLockStub) Extend(_ context.Context, key string, _ int64) (*lock.Lock, error) {
	s.calls = append(s.calls, "extend:"+key)
	return &lock.Lock{Key: key, FencingToken: 2}, nil
}

func (s *scopedLockStub) Status(_ context.Context, key string) (*lock.Lock, error) {
	s.calls = append(s.calls, "status:"+key)
	return &lock.Lock{Key: key, HolderID: "w1", FencingToken: 1}, nil
}

func (s *scopedLockStub) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	s.calls = append(s.calls, "list:"+prefix)
	return nil, nil
}

// lockScopeRegistry records the (tenant, database) pair LockService was asked
// to resolve.
type lockScopeRegistry struct {
	service.Registry
	svc       service.LockServicer
	err       error
	gotTenant string
	gotDB     string
	calls     int
}

func (r *lockScopeRegistry) LockService(tenantID, db string) (service.LockServicer, error) {
	r.calls++
	r.gotTenant, r.gotDB = tenantID, db
	return r.svc, r.err
}

// lockRequests is every lock route, so the assertions below cover the whole
// surface rather than one verb -- an unresolved verb is an unconfined verb.
var lockRequests = []struct {
	name   string
	method string
	path   string
	body   string
	want   string
}{
	{"acquire", http.MethodPost, "/lock/acquire", `{"key":"deploy","holder_id":"w1","ttl":60}`, "lock:deploy"},
	{"trylock", http.MethodPost, "/lock/trylock", `{"key":"deploy","holder_id":"w1","ttl":60}`, "trylock:deploy"},
	{"release", http.MethodPost, "/lock/release", `{"key":"deploy","fencing_token":1}`, "unlock:deploy"},
	{"extend", http.MethodPost, "/lock/extend", `{"key":"deploy","fencing_token":1}`, "extend:deploy"},
	{"status", http.MethodGet, "/lock/status?key=deploy", "", "status:deploy"},
	{"list", http.MethodGet, "/lock/list?prefix=jobs/", "", "list:jobs/"},
}

// serveLock runs the request through scopeMiddleware before dispatch, so the
// database scope arrives the way a real request's does -- from the
// X-QRDB-Database header -- rather than being planted in the context.
func serveLock(srv *Server, rec http.ResponseWriter, req *http.Request) {
	scopeMiddleware(http.HandlerFunc(srv.dispatch)).ServeHTTP(rec, req)
}

// newLockRequest builds the request for one row of lockRequests, carrying the
// caller's tenant and database scope.
func newLockRequest(row int, tenantID, db string) *http.Request {
	r := lockRequests[row]
	var req *http.Request
	if r.body == "" {
		req = httptest.NewRequest(r.method, r.path, nil)
	} else {
		req = httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
	}
	if db != "" {
		req.Header.Set("X-QRDB-Database", db)
	}
	return req.WithContext(tenant.WithTenantID(req.Context(), tenantID))
}

// TestRESTLockResolvesThroughTheRegistry is the transport half of go-qrdb#455
// on REST: with a registry configured, every lock verb must resolve the
// servicer for the caller's (tenant, database) scope and must not reach the
// process-wide singleton, which is built over the raw, un-namespaced engines.
func TestRESTLockResolvesThroughTheRegistry(t *testing.T) {
	for i, row := range lockRequests {
		t.Run(row.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			scoped := &scopedLockStub{}
			reg := &lockScopeRegistry{svc: scoped}
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
				WithLock(singleton), WithServiceRegistry(reg))

			rec := httptest.NewRecorder()
			serveLock(srv, rec, newLockRequest(i, "acme", "db2"))

			assertStatus(t, rec, http.StatusOK)
			if len(singleton.calls) != 0 {
				t.Fatalf("the process-wide singleton was used: %v", singleton.calls)
			}
			if len(scoped.calls) != 1 || scoped.calls[0] != row.want {
				t.Fatalf("scoped servicer saw %v, want [%s]", scoped.calls, row.want)
			}
			if reg.gotTenant != "acme" || reg.gotDB != "db2" {
				t.Fatalf("registry resolved (%q, %q), want (acme, db2)", reg.gotTenant, reg.gotDB)
			}
		})
	}
}

// TestRESTLockFallsBackToTheSingleton covers the registry-less composition,
// where the singleton is correct -- the whole store and the caller's own
// keyspace are the same set -- and must still be served.
func TestRESTLockFallsBackToTheSingleton(t *testing.T) {
	for i, row := range lockRequests {
		t.Run(row.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
				WithLock(singleton))

			rec := httptest.NewRecorder()
			serveLock(srv, rec, newLockRequest(i, "acme", ""))

			assertStatus(t, rec, http.StatusOK)
			if len(singleton.calls) != 1 || singleton.calls[0] != row.want {
				t.Fatalf("singleton saw %v, want [%s]", singleton.calls, row.want)
			}
		})
	}
}

// TestRESTLockResolveFailurePropagates pins that a refused resolution -- an
// unknown tenant, an unknown database, a sealed barrier -- reaches the client
// as that error rather than falling back to a servicer whose keyspace the
// caller was refused.
func TestRESTLockResolveFailurePropagates(t *testing.T) {
	singleton := &scopedLockStub{}
	reg := &lockScopeRegistry{err: &DatabaseNotFoundError{TenantID: "acme", Database: "db2"}}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithLock(singleton), WithServiceRegistry(reg))

	rec := httptest.NewRecorder()
	serveLock(srv, rec, newLockRequest(1, "acme", "db2"))

	assertStatus(t, rec, http.StatusNotFound)
	if len(singleton.calls) != 0 {
		t.Fatalf("a refused resolution fell back to the singleton: %v", singleton.calls)
	}
}

// TestRESTLockWithNeitherIsRefused pins the fail-closed case: no registry and
// no singleton reports not-implemented rather than dereferencing nil. Before
// go-qrdb#455 the routes attached whenever EITHER was configured while every
// handler tested only the singleton, so a registry-configured server answered
// 501 on a surface it could in fact serve.
func TestRESTLockWithNeitherIsRefused(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	rec := httptest.NewRecorder()
	if svc := srv.lockSvcFor(rec, context.Background()); svc != nil {
		t.Fatalf("expected nil, got %T", svc)
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if body := rec.Body.String(); !strings.Contains(body, (&ServiceNotImplementedError{Service: "lock"}).Error()) {
		t.Fatalf("response body %q does not carry the lock not-implemented error", body)
	}
}

// TestRESTLockRegistryOnlyServerServesTheSurface is the other half of the
// route-gating defect: a server configured with a registry and NO singleton
// attaches the lock routes, and must now answer them from the registry rather
// than reporting 501.
func TestRESTLockRegistryOnlyServerServesTheSurface(t *testing.T) {
	scoped := &scopedLockStub{}
	reg := &lockScopeRegistry{svc: scoped}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithServiceRegistry(reg))

	rec := httptest.NewRecorder()
	serveLock(srv, rec, newLockRequest(1, "acme", "db2"))

	assertStatus(t, rec, http.StatusOK)
	if reg.calls != 1 {
		t.Fatalf("registry resolutions = %d, want 1", reg.calls)
	}
}
