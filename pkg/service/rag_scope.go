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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// RAGBinding is the node's per-(tenant, database) service registry as the
// RAG pipeline reaches it. The RAG service is one per node and is built with
// the rest of the service graph, before the server that owns the registry
// exists, so the registry is bound later (RAGRegistryBinder, called by the
// server at start) and read on every call. Until it is bound every RAG
// operation is refused rather than served from the node's raw stores.
//
// Every record the pipeline reads or writes -- chunk text, document metadata
// and vectors -- is resolved through the registry for the request's tenant
// (tenant.IDFromContext) and database (tenant.DatabaseFromContext), exactly as
// a data-plane request on any transport is. That is what applies the
// database's seal, its ownership, the tenant's quota and its isolation
// routing, and what refuses a database the caller does not have: an unknown
// or foreign X-QRDB-Database is a not-found, never a new keyspace.
//
// A call whose context carries no tenant is refused (RAGTenantRequiredError)
// rather than resolved for the system tenant: on an authenticated node every
// transport binds the caller's tenant, so a missing one is a composition
// fault, and guessing the system tenant would hand the caller the system
// tenant's records. A composition with no request authentication -- where no
// call carries a tenant -- binds with RAGUnauthenticatedTenant to name the
// tenant such calls act for.
type RAGBinding struct {
	reg atomic.Pointer[registryRef]
}

// registryRef boxes a Registry so it can live in an atomic.Pointer, with the
// tenant a call that carries none acts for ("" refuses such calls).
type registryRef struct {
	Registry
	unauthenticatedTenant string
}

// RAGTenantRequiredError is returned when a RAG call's context carries no
// tenant on a composition that authenticates requests: there every transport
// binds the caller's tenant, so a call without one is refused rather than
// resolved for a guessed tenant.
type RAGTenantRequiredError struct {
	Op string
}

// Error returns a human-readable description.
func (e *RAGTenantRequiredError) Error() string {
	return "service: " + e.Op + ": the call carries no tenant"
}

// RAGBindOption configures Bind.
type RAGBindOption func(*registryRef)

// RAGUnauthenticatedTenant names the tenant a call whose context carries no
// tenant acts for. Only a composition with no request authentication sets it:
// there, no call carries a tenant, and the node's operator owns every call.
func RAGUnauthenticatedTenant(tenantID string) RAGBindOption {
	return func(r *registryRef) { r.unauthenticatedTenant = tenantID }
}

// NewRAGBinding returns an unbound binding.
func NewRAGBinding() *RAGBinding { return &RAGBinding{} }

// Bind sets the registry the pipeline resolves through. A nil registry
// unbinds it.
func (b *RAGBinding) Bind(reg Registry, opts ...RAGBindOption) {
	if reg == nil {
		b.reg.Store(nil)
		return
	}
	ref := &registryRef{Registry: reg}
	for _, opt := range opts {
		opt(ref)
	}
	b.reg.Store(ref)
}

// resolve returns the bound registry and the tenant the call acts for, or the
// refusal: an unbound pipeline, or a call that carries no tenant on a
// composition that did not name one for such calls.
func (b *RAGBinding) resolve(ctx context.Context, op string) (Registry, string, error) {
	ref := b.reg.Load()
	if ref == nil {
		return nil, "", &CapabilityUnsupportedError{Message: "rag: the node's database registry is not bound"}
	}
	tenantID, ok := tenant.IDFromContext(ctx)
	if !ok {
		if ref.unauthenticatedTenant == "" {
			return nil, "", &RAGTenantRequiredError{Op: op}
		}
		tenantID = ref.unauthenticatedTenant
	}
	return ref.Registry, tenantID, nil
}

// KV resolves the caller's KV servicer.
func (b *RAGBinding) KV(ctx context.Context) (KVServicer, error) {
	reg, tenantID, err := b.resolve(ctx, "rag")
	if err != nil {
		return nil, err
	}
	return reg.KVService(tenantID, tenant.DatabaseFromContext(ctx))
}

// Vector resolves the caller's vector servicer. It is scoped to the
// caller's database, so one database's documents cannot overwrite another's
// vectors.
func (b *RAGBinding) Vector(ctx context.Context) (VectorService, error) {
	reg, tenantID, err := b.resolve(ctx, "rag")
	if err != nil {
		return nil, err
	}
	return reg.VectorService(tenantID, tenant.DatabaseFromContext(ctx))
}

// RAGRegistryBinder is implemented by a RAG service whose pipeline resolves
// through a RAGBinding. The server binds its registry at start.
type RAGRegistryBinder interface {
	BindRegistry(reg Registry, opts ...RAGBindOption)
}

// RAGKVStore is the part of the KV service the RAG pipeline keeps chunk text
// and document metadata in. It matches pkg/rag's KVStore.
type RAGKVStore interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// ScopedRAGStore is the RAG pipeline's KV store: every call resolves the KV
// servicer for the caller's (tenant, database) through the RAGBinding and
// uses it with the caller's key, the registry having already confined it to
// the database's "<tenant>/<database>/" keyspace. A database drop therefore
// reclaims its RAG records with the rest of its keyspace.
//
// The RAG service is a node-wide singleton, and its keys are
// "rag:{namespace}:{document}:{chunk}", which name no tenant: read from the
// node's shared store, two tenants using the same namespace and document ID
// overwrote each other's chunk text. It is the only store NewRAGService
// accepts, so the service cannot be composed unscoped.
type ScopedRAGStore struct {
	binding *RAGBinding
}

// NewScopedRAGStore returns the RAG store resolving through binding.
func NewScopedRAGStore(binding *RAGBinding) *ScopedRAGStore {
	return &ScopedRAGStore{binding: binding}
}

var _ RAGKVStore = (*ScopedRAGStore)(nil)

// Put stores value at key in the caller's database.
func (s *ScopedRAGStore) Put(ctx context.Context, key string, value []byte) error {
	kv, err := s.binding.KV(ctx)
	if err != nil {
		return err
	}
	return kv.Put(ctx, key, value)
}

// Get returns the value at key in the caller's database.
func (s *ScopedRAGStore) Get(ctx context.Context, key string) ([]byte, error) {
	kv, err := s.binding.KV(ctx)
	if err != nil {
		return nil, err
	}
	return kv.Get(ctx, key)
}

// Delete removes key from the caller's database.
func (s *ScopedRAGStore) Delete(ctx context.Context, key string) error {
	kv, err := s.binding.KV(ctx)
	if err != nil {
		return err
	}
	return kv.Delete(ctx, key)
}

// Scan returns the caller's records under prefix, keyed as written.
func (s *ScopedRAGStore) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	kv, err := s.binding.KV(ctx)
	if err != nil {
		return nil, err
	}
	return kv.Scan(ctx, prefix)
}
