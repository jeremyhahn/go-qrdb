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
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// scopeRegistry is a service.Registry that resolves KV per (tenant,
// database) the way the node's registry does: an unknown database is not
// found, a sealed one is refused, and every other pair has its own store.
// Only the KV resolver is driven by these tests.
type scopeRegistry struct {
	service.Registry
	mu     sync.Mutex
	stores map[string]*mockKVService // "<tenant>/<db>"
	sealed map[string]bool
	asked  []string
}

func newScopeRegistry(known ...string) *scopeRegistry {
	r := &scopeRegistry{stores: map[string]*mockKVService{}, sealed: map[string]bool{}}
	for _, k := range known {
		r.stores[k] = newMockKVService()
	}
	return r
}

func (r *scopeRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := tenantID + "/" + db
	r.asked = append(r.asked, key)
	if r.sealed[key] {
		return nil, &service.BarrierSealedError{TenantID: tenantID, Database: db}
	}
	s, ok := r.stores[key]
	if !ok {
		return nil, &service.DatabaseNotFoundError{TenantID: tenantID, Database: db}
	}
	return s, nil
}

// Without a service registry there is no way to reach another database, so
// scoping is refused rather than answered with the default database.
func TestWithDatabase_NoRegistryRefuses(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("ledger")
	var unsupported *transport.DatabaseScopeUnsupportedError
	if !errors.As(err, &unsupported) || scoped != nil {
		t.Fatalf("WithDatabase without a registry = %v, %v; want *transport.DatabaseScopeUnsupportedError", scoped, err)
	}
	if unsupported.Database != "ledger" {
		t.Errorf("Database = %q, want ledger", unsupported.Database)
	}

	// The default database needs no scoping and stays available.
	for _, name := range []string{"", "system"} {
		got, err := tr.WithDatabase(name)
		if err != nil || got != tr {
			t.Errorf("WithDatabase(%q) = %v, %v; want the receiver", name, got, err)
		}
	}
}

// With a registry the scoped transport's data plane is the named database's,
// resolved per call for the context's tenant: writes land there and never in
// the default store, and a sealed database is refused on the next call.
func TestWithDatabase_ResolvesThroughRegistry(t *testing.T) {
	defaultKV := newMockKVService()
	reg := newScopeRegistry(tenantid.System+"/ledger", "acme/ledger")
	tr, err := New(defaultKV, &mockAdminService{}, WithServiceRegistry(reg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scopedTr, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	scoped := scopedTr.(*Transport)
	ctx := context.Background()

	if err := scoped.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("scoped Put: %v", err)
	}
	if _, ok := defaultKV.store["k"]; ok {
		t.Fatal("the scoped write landed in the default database")
	}
	if got, _ := reg.stores[tenantid.System+"/ledger"].Get(ctx, "k"); string(got) != "v" {
		t.Fatalf("the scoped write did not land in ledger: %q", got)
	}

	// The tenant is the call's: acme's ledger is a different store.
	acme := tenant.WithTenantID(ctx, "acme")
	if _, err := scoped.Get(acme, "k"); err == nil {
		t.Fatal("acme read the system tenant's ledger key")
	}

	// Sealing the database refuses the next call on the already-scoped
	// transport, because resolution is per call.
	reg.mu.Lock()
	reg.sealed[tenantid.System+"/ledger"] = true
	reg.mu.Unlock()
	if err := scoped.Put(ctx, "k2", []byte("v")); err == nil {
		t.Fatal("a sealed database accepted a write through a scoped transport")
	}

	// An unknown database is refused, not served from the default store.
	unknown, err := tr.WithDatabase("nosuchdb")
	if err != nil {
		t.Fatalf("WithDatabase(nosuchdb): %v", err)
	}
	if err := unknown.(*Transport).Put(ctx, "k", []byte("v")); err == nil {
		t.Fatal("a write to an unknown database succeeded")
	}
	if len(defaultKV.store) != 0 {
		t.Fatalf("the default store was written: %v", defaultKV.store)
	}
}

// cloneShared must decide every field of Transport: a field added later and
// not copied would silently be missing from every scoped transport.
func TestCloneShared_CoversEveryField(t *testing.T) {
	// Fields cloneShared deliberately does not copy: its own sync state.
	own := map[string]bool{"sqlOnce": true, "sqlSvcCache": true, "factorVerifierMu": true, "closed": true, "factorVerifiers": true}
	// Fields cloneShared copies. A field of Transport in neither list is one
	// nobody decided about.
	copied := map[string]bool{}
	for _, n := range []string{"kvSvc", "adminSvc", "mvccSvc", "txSvc", "batchSvc", "watchSvc", "leaseSvc",
		"indexSvc", "tsSvc", "vectorSvc", "userSvc", "roleSvc", "certProv", "dbSvc", "barrierSvc", "pinBackend",
		"dekSvc", "ceremonySvc", "ceremonyCA", "credentialSvc", "backupSvc", "rebalanceSvc", "embeddingSvc",
		"ragSvc", "collectionSvc", "sealStoreSvc", "tenantSvc", "lockSvc", "custodianGroupSvc", "shareSvc",
		"authFactorSvc", "auditLogger", "encMigrator", "registry", "rbacAuthz", "authMW", "userAuthFactorSvc",
		"loginResolver", "sessionIssuer", "identityHydrator", "loginSessions", "poolManager", "poolStateStore",
		"nowFunc", "standalone"} {
		copied[n] = true
	}
	for i := 0; i < reflect.TypeOf((*Transport)(nil)).Elem().NumField(); i++ {
		name := reflect.TypeOf((*Transport)(nil)).Elem().Field(i).Name
		if !own[name] && !copied[name] {
			t.Errorf("Transport field %s is neither copied nor owned by cloneShared", name)
		}
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(newScopeRegistry()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Give every copyable interface/pointer/func field a non-nil value.
	v := reflect.ValueOf(tr).Elem()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if own[name] {
			continue
		}
		f := v.Field(i)
		if !f.IsZero() {
			continue
		}
		switch f.Kind() {
		case reflect.Pointer:
			reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem().Set(reflect.New(f.Type().Elem()))
		case reflect.Func:
			reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem().Set(reflect.MakeFunc(f.Type(), func([]reflect.Value) []reflect.Value {
				return nil
			}))
		case reflect.Bool:
			reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem().SetBool(true)
		case reflect.Interface:
			// Satisfied by a field-specific stand-in only when one exists;
			// the non-nil check below is limited to fields that were set.
		default:
			t.Fatalf("field %s has kind %s, which this test does not know how to fill", name, f.Kind())
		}
	}
	c := tr.cloneShared()
	cv := reflect.ValueOf(c).Elem()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if own[name] {
			continue
		}
		orig := reflect.NewAt(v.Field(i).Type(), v.Field(i).Addr().UnsafePointer()).Elem()
		got := reflect.NewAt(cv.Field(i).Type(), cv.Field(i).Addr().UnsafePointer()).Elem()
		if orig.Kind() == reflect.Func {
			if orig.IsNil() != got.IsNil() {
				t.Errorf("cloneShared dropped field %s", name)
			}
			continue
		}
		if !orig.IsZero() && orig.Interface() != got.Interface() {
			t.Errorf("cloneShared did not copy field %s", name)
		}
	}
}
