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

package embedded

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// suspendableTenants is the tenant status the auth middleware admits
// through: every tenant is active until suspend is called.
type suspendableTenants struct{ suspended atomic.Bool }

func (s *suspendableTenants) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	status := tenant.StatusActive
	if s.suspended.Load() {
		status = tenant.StatusSuspended
	}
	return &tenant.TenantInfo{ID: tenantID, Status: status}, nil
}

// scopedAdmissionWatch is a database's watch service whose watchers the
// test pushes onto; each event carries the key the watch was opened on as
// it reached the database, so the registry's namespacing passes it through.
type scopedAdmissionWatch struct {
	mockWatchService
	src    *stubWatcher
	opened atomic.Value // string
}

func newScopedAdmissionWatch() *scopedAdmissionWatch {
	w := &scopedAdmissionWatch{src: newStubWatcher()}
	open := func(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
		w.opened.Store(key)
		return w.src, nil
	}
	w.watchFn, w.watchPrefixFn = open, open
	return w
}

func (w *scopedAdmissionWatch) push(rev int64) {
	key, _ := w.opened.Load().(string)
	w.src.ch <- watch.WatchResponse{WatchID: 1, Revision: rev, Events: []*watch.Event{
		{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: rev}},
	}}
}

// newAdmittedScopedTransport builds a transport the way a composer with
// RBAC does -- WithAuthMiddleware over a tenant status reader, and the
// public server.NewRESTServiceRegistry, which admits nothing of its own --
// and scopes it to the "ledger" database.
func newAdmittedScopedTransport(t *testing.T) (*Transport, *scopedAdmissionWatch, *suspendableTenants) {
	t.Helper()
	ledger := newScopedAdmissionWatch()
	dbs := server.NewDatabaseServiceRegistry("default")
	dbs.Register("ledger", &server.DatabaseServices{Name: "ledger", KV: newMockKVService(), Watch: ledger})
	tenants := &suspendableTenants{}
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithTenantStatusReader(tenants))
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithWatchService(&mockWatchService{}),
		WithAuthMiddleware(mw),
		WithServiceRegistry(server.NewRESTServiceRegistry(dbs, nil, tenantid.System)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	return scoped.(*Transport), ledger, tenants
}

func acmeIdentityCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	id := &auth.Identity{Subject: "alice", TenantID: "acme"}
	return tenant.WithTenantID(auth.WithIdentity(ctx, id), id.TenantID)
}

// requireSuspensionEndsScopedWatch delivers one event while the tenant is
// active, suspends it, and requires the next event to end the stream with
// the typed admission refusal rather than reach the caller.
func requireSuspensionEndsScopedWatch(t *testing.T, ch <-chan transport.WatchResponse, ledger *scopedAdmissionWatch, tenants *suspendableTenants) {
	t.Helper()
	ledger.push(1)
	if got := <-ch; got.Canceled || got.Revision != 1 {
		t.Fatalf("event while active = %+v; want revision 1 delivered", got)
	}

	tenants.suspended.Store(true)
	ledger.push(2)
	got := <-ch
	want := (&authmw.TenantNotActiveError{TenantID: "acme", Status: string(tenant.StatusSuspended)}).Error()
	if !got.Canceled || got.CancelReason != want {
		t.Fatalf("event after suspension = %+v; want a terminal response carrying %q", got, want)
	}
	if !strings.Contains(got.CancelReason, "acme") {
		t.Fatalf("cancel reason %q does not name the tenant", got.CancelReason)
	}
	if extra, ok := <-ch; ok {
		t.Fatalf("stream delivered %+v after the refusal; want it closed", extra)
	}
}

// A key watch opened through a database-scoped transport ends with the
// tenant-not-active refusal once its tenant is suspended, even though the
// registry the scope resolves through admits nothing itself.
func TestWithDatabase_WatchEndsWhenTenantSuspended(t *testing.T) {
	scoped, ledger, tenants := newAdmittedScopedTransport(t)
	ch, err := scoped.Watch(acmeIdentityCtx(t), "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	requireSuspensionEndsScopedWatch(t, ch, ledger, tenants)
}

// The same for a prefix watch.
func TestWithDatabase_WatchPrefixEndsWhenTenantSuspended(t *testing.T) {
	scoped, ledger, tenants := newAdmittedScopedTransport(t)
	ch, err := scoped.WatchPrefix(acmeIdentityCtx(t), "p/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	requireSuspensionEndsScopedWatch(t, ch, ledger, tenants)
}

// Without an AuthMiddleware the scoped watch is not wrapped: there is no
// admitter to re-run, matching a network transport with auth disabled.
func TestWithDatabase_WatchUnadmittedWithoutAuthMiddleware(t *testing.T) {
	dbs := server.NewDatabaseServiceRegistry("default")
	dbs.Register("ledger", &server.DatabaseServices{Name: "ledger", KV: newMockKVService(), Watch: newScopedAdmissionWatch()})
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithWatchService(&mockWatchService{}),
		WithServiceRegistry(server.NewRESTServiceRegistry(dbs, nil, tenantid.System)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	if _, ok := scoped.(*Transport).watchSvc.(*scopedWatch); !ok {
		t.Fatalf("watchSvc = %T; want the bare scoped watch", scoped.(*Transport).watchSvc)
	}
}
