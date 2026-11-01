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
	"fmt"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// statusTenants is a TenantStatusReader whose statuses a test changes.
type statusTenants struct {
	mu       sync.Mutex
	statuses map[string]tenant.Status
}

func (s *statusTenants) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.statuses[tenantID]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	return &tenant.TenantInfo{ID: tenantID, Status: status}, nil
}

func (s *statusTenants) set(tenantID string, status tenant.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[tenantID] = status
}

var _ authmw.TenantStatusReader = (*statusTenants)(nil)

// newSingletonWatchServer builds a server whose watch service is the
// singleton WithWatch service -- no database-service registry -- under an
// auth chain reading tenants, and returns the source its watches read.
func newSingletonWatchServer(t *testing.T, tenants *statusTenants) (*Server, *registryPushWatcher) {
	t.Helper()
	src := &registryPushWatcher{events: make(chan watch.WatchResponse, 2)}
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithTenantStatusReader(tenants))
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithWatch(&registryPushWatchService{w: src}), WithAuthMiddleware(mw))
	if reg := srv.buildServiceRegistry(); reg != nil {
		t.Fatalf("buildServiceRegistry() = %T, want nil for a singleton watch server", reg)
	}
	return srv, src
}

// openListenerWatch opens a watch for identity on the watch service every
// listener (REST, gRPC, QUIC, unix, MCP) of srv receives.
func openListenerWatch(t *testing.T, srv *Server, identity *auth.Identity) watch.Watcher {
	t.Helper()
	w, err := srv.listenerWatch().Watch(auth.WithIdentity(context.Background(), identity), "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// A watch opened on a singleton watch server while acme was active ends on
// its next event after acme is suspended, with the *TenantNotActiveError a
// new request gets: the singleton service goes through the same admitter
// the registry installs.
func TestServer_SingletonWatch_SuspensionEndsOpenWatch(t *testing.T) {
	tenants := &statusTenants{statuses: map[string]tenant.Status{"acme": tenant.StatusActive}}
	srv, src := newSingletonWatchServer(t, tenants)
	w := openListenerWatch(t, srv, &auth.Identity{Subject: "bob", TenantID: "acme"})

	src.events <- watch.WatchResponse{Revision: 1}
	if resp, ok := <-w.Events(); !ok || resp.Revision != 1 {
		t.Fatalf("event while active = %+v, %v; want revision 1 delivered", resp, ok)
	}

	tenants.set("acme", tenant.StatusSuspended)
	src.events <- watch.WatchResponse{Revision: 2}
	if resp, ok := <-w.Events(); ok {
		t.Fatalf("event after suspension delivered: %+v", resp)
	}
	var notActive *authmw.TenantNotActiveError
	if !errors.As(w.Err(), &notActive) {
		t.Fatalf("watch Err() = %T(%v), want *TenantNotActiveError", w.Err(), w.Err())
	}
	if notActive.TenantID != "acme" || notActive.Status != string(tenant.StatusSuspended) {
		t.Fatalf("refusal = %+v, want acme suspended", notActive)
	}
}

// A system-tenant watch on a singleton watch server keeps delivering after
// another tenant is suspended.
func TestServer_SingletonWatch_SystemWatchSurvivesOtherTenantSuspension(t *testing.T) {
	tenants := &statusTenants{statuses: map[string]tenant.Status{"acme": tenant.StatusActive}}
	srv, src := newSingletonWatchServer(t, tenants)
	w := openListenerWatch(t, srv, &auth.Identity{Subject: "root", TenantID: tenantid.System})

	tenants.set("acme", tenant.StatusSuspended)
	src.events <- watch.WatchResponse{Revision: 3}
	if resp, ok := <-w.Events(); !ok || resp.Revision != 3 {
		t.Fatalf("system watch event = %+v, %v; want revision 3 delivered", resp, ok)
	}
	if err := w.Err(); err != nil {
		t.Fatalf("system watch Err() = %v, want nil", err)
	}
}

// Without auth middleware the singleton watch service is handed to the
// listeners with no admitter in front of it: only the counting wrapper that
// makes a client's watch hold a watch.max_watchers slot.
func TestServer_SingletonWatch_NoAuthUnwrapped(t *testing.T) {
	svc := &registryPushWatchService{w: &registryPushWatcher{}}
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithWatch(svc))
	if got, want := fmt.Sprintf("%T", srv.listenerWatch()), fmt.Sprintf("%T", service.NewCountedWatch(svc)); got != want {
		t.Fatalf("listenerWatch() = %s, want the configured service behind only %s", got, want)
	}
	if srv.streamAdmitter() != nil {
		t.Fatal("streamAdmitter() non-nil without auth middleware")
	}
}
