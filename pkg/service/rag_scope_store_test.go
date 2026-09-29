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
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ragScopeRegistry is a Registry that resolves KV and vectors per (tenant,
// database) the way the node's registry does: a database it does not know is
// not found, a sealed one is refused, and every known pair has its own
// stores. It records each resolution.
type ragScopeRegistry struct {
	Registry
	mu      sync.Mutex
	kv      map[string]KVServicer
	vectors map[string]VectorService
	sealed  map[string]bool
	asked   []string
}

func newRAGScopeRegistry() *ragScopeRegistry {
	return &ragScopeRegistry{kv: map[string]KVServicer{}, vectors: map[string]VectorService{}, sealed: map[string]bool{}}
}

func (r *ragScopeRegistry) resolve(tenantID, db string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := tenantID + "/" + db
	r.asked = append(r.asked, key)
	if r.sealed[key] {
		return "", &BarrierSealedError{TenantID: tenantID, Database: db}
	}
	if _, ok := r.kv[key]; !ok {
		return "", &DatabaseNotFoundError{TenantID: tenantID, Database: db}
	}
	return key, nil
}

func (r *ragScopeRegistry) KVService(tenantID, db string) (KVServicer, error) {
	key, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	return r.kv[key], nil
}

func (r *ragScopeRegistry) VectorService(tenantID, db string) (VectorService, error) {
	key, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	return r.vectors[key], nil
}

// newBoundRAGStore returns a ScopedRAGStore whose binding resolves every
// (tenant, database) to kv.
func newBoundRAGStore(kv KVServicer) *ScopedRAGStore {
	b := NewRAGBinding()
	b.Bind(&singleKVRegistry{kv: kv}, RAGUnauthenticatedTenant(tenantid.System))
	return NewScopedRAGStore(b)
}

// singleKVRegistry resolves KV for every (tenant, database) to one store.
type singleKVRegistry struct {
	Registry
	kv KVServicer
}

func (r *singleKVRegistry) KVService(string, string) (KVServicer, error) { return r.kv, nil }

func TestScopedRAGStore_ResolvesEveryVerbPerCall(t *testing.T) {
	reg := newRAGScopeRegistry()
	app := newRAGMockKV()
	reg.kv["acme/app"] = app
	b := NewRAGBinding()
	b.Bind(reg)
	store := NewScopedRAGStore(b)
	acme := tenant.WithDatabase(tenant.WithTenantID(context.Background(), "acme"), "app")

	if err := store.Put(acme, "rag:kb:d:0", []byte("text")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(app.kvStore["rag:kb:d:0"]) != "text" {
		t.Fatalf("Put did not land in acme/app: %v", app.kvStore)
	}
	if got, err := store.Get(acme, "rag:kb:d:0"); err != nil || string(got) != "text" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if got, err := store.Scan(acme, "rag:kb:"); err != nil || len(got) != 1 {
		t.Fatalf("Scan = %v, %v", got, err)
	}
	if err := store.Delete(acme, "rag:kb:d:0"); err != nil || len(app.kvStore) != 0 {
		t.Fatalf("Delete = %v, left %v", err, app.kvStore)
	}

	// A database the caller does not have, another tenant, and a sealed
	// database are all refused on every verb.
	reg.sealed["acme/app"] = true
	for _, ctx := range []context.Context{
		acme,
		tenant.WithDatabase(tenant.WithTenantID(context.Background(), "acme"), "nosuchdb"),
		tenant.WithDatabase(tenant.WithTenantID(context.Background(), "globex"), "app"),
	} {
		if err := store.Put(ctx, "k", []byte("v")); err == nil {
			t.Errorf("Put for %s/%s succeeded", tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
		}
		if _, err := store.Get(ctx, "k"); err == nil {
			t.Errorf("Get for %s/%s succeeded", tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
		}
		if _, err := store.Scan(ctx, "rag:"); err == nil {
			t.Errorf("Scan for %s/%s succeeded", tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
		}
		if err := store.Delete(ctx, "k"); err == nil {
			t.Errorf("Delete for %s/%s succeeded", tenant.FromContext(ctx), tenant.DatabaseFromContext(ctx))
		}
	}
	var sealed *BarrierSealedError
	if err := store.Put(acme, "k", []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Put on a sealed database = %v, want *BarrierSealedError", err)
	}
}

// Until the server binds the registry, and after it is unbound, RAG refuses
// rather than reaching any store.
func TestRAGBinding_UnboundRefuses(t *testing.T) {
	b := NewRAGBinding()
	store := NewScopedRAGStore(b)
	var unsupported *CapabilityUnsupportedError
	if err := store.Put(context.Background(), "k", []byte("v")); !errors.As(err, &unsupported) {
		t.Fatalf("Put on an unbound store = %v, want *CapabilityUnsupportedError", err)
	}
	if _, err := b.Vector(context.Background()); !errors.As(err, &unsupported) {
		t.Fatalf("Vector on an unbound binding = %v, want *CapabilityUnsupportedError", err)
	}
	b.Bind(&singleKVRegistry{kv: newRAGMockKV()}, RAGUnauthenticatedTenant(tenantid.System))
	if err := store.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put once bound: %v", err)
	}
	b.Bind(nil)
	if err := store.Put(context.Background(), "k", []byte("v")); !errors.As(err, &unsupported) {
		t.Fatalf("Put after unbinding = %v, want *CapabilityUnsupportedError", err)
	}
}

// The RAG service binds its pipeline's registry for the server.
func TestRAGService_BindRegistry(t *testing.T) {
	store := NewScopedRAGStore(NewRAGBinding())
	svc := NewRAGService(nil, nil, store)
	binder, ok := svc.(RAGRegistryBinder)
	if !ok {
		t.Fatal("the RAG service does not implement RAGRegistryBinder")
	}
	kv := newRAGMockKV()
	binder.BindRegistry(&singleKVRegistry{kv: kv}, RAGUnauthenticatedTenant(tenantid.System))
	if err := store.Put(context.Background(), "k", []byte("v")); err != nil || string(kv.kvStore["k"]) != "v" {
		t.Fatalf("Put after BindRegistry = %v, store %v", err, kv.kvStore)
	}
}
