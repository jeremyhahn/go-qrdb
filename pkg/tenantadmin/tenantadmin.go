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

// Package tenantadmin records the first admin of a tenant whose identity
// records cannot be written yet.
//
// A tenant of a dedicated isolation type (barrier, volume, raft) keeps its
// users, roles and certificate bindings behind its own barrier
// (tenant.IAMBehindTenantBarrier), and that barrier does not exist until the
// tenant admin initializes it. Tenant onboarding nevertheless binds the
// admin's certificate when the tenant is created. For such a tenant the
// binding is recorded here, in the tenant's identity namespace on the
// replicated system store -- readable while the tenant barrier is sealed --
// and it does two things until the barrier is initialized:
//
//   - it is the ONLY certificate the barrier's trust-on-first-init window
//     admits (pkg/auth/middleware), so whoever initializes the barrier is the
//     admin the platform operator onboarded; and
//   - once the barrier is up, the admin's account is created from it, bound
//     to that certificate and granted the tenant's Admin role, and the record
//     is deleted (pkg/bootstrap.CompletePendingTenantAdmin).
package tenantadmin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// pendingKey is the reserved key a tenant's pending admin lives under inside
// its identity namespace. A data-plane key cannot reach it: the data plane is
// "{tenant}/{database}/..." and a database name cannot hold ':'.
const pendingKey = "__sys:pending-admin"

// Pending is a tenant admin onboarded before the tenant's identity records
// could be written.
type Pending struct {
	// Username is the admin account to create.
	Username string `json:"username"`

	// Thumbprint is the SPKI thumbprint (user.ComputeSPKIThumbprint) of the
	// certificate the admin was onboarded with.
	Thumbprint string `json:"thumbprint"`

	// NotAfter is that certificate's expiry; the binding carries it.
	NotAfter time.Time `json:"not_after"`

	// Serial is the certificate's serial when this cluster signed it (a CSR
	// onboarding), empty for a bring-your-own certificate.
	Serial string `json:"serial,omitempty"`
}

// Store reads and writes pending tenant admins.
type Store interface {
	// Create records tenantID's pending admin only if the tenant has none,
	// decided atomically against the replicated store; an existing record is
	// never replaced (*AlreadyPendingError), exactly as `qrdb init` never
	// replaces an established first admin. Two concurrent onboardings of the
	// same tenant cannot both record one.
	Create(ctx context.Context, tenantID string, p *Pending) error

	// Get returns tenantID's pending admin, or (nil, nil) when it has none.
	Get(ctx context.Context, tenantID string) (*Pending, error)

	// Delete removes tenantID's pending admin; removing none is not an error.
	Delete(ctx context.Context, tenantID string) error
}

// KVStore is the built-in Store over the replicated system store.
type KVStore struct {
	store kvstore.KVStore
}

// Compile-time interface check.
var _ Store = (*KVStore)(nil)

// NewKVStore returns the built-in Store over store, which must be the
// replicated system store (not a tenant-confined one): it confines each
// tenant itself.
func NewKVStore(store kvstore.KVStore) *KVStore {
	return &KVStore{store: store}
}

func (s *KVStore) scoped(tenantID string) (kvstore.KVStore, error) {
	if err := tenant.ValidateTenantIDShape(tenantID); err != nil {
		return nil, &InvalidTenantError{TenantID: tenantID, Err: err}
	}
	if tenantid.IsSystem(tenantID) {
		// The system tenant's admin is created by first boot, never pending.
		return nil, &InvalidTenantError{TenantID: tenantID}
	}
	scoped, err := kvstore.NewNamespacedKVStore(s.store, tenant.IdentityNamespace(tenantID))
	if err != nil {
		return nil, &StoreError{Op: "scope", Err: err}
	}
	return scoped, nil
}

// Create implements Store. The store must offer kvstore.AtomicKVStore; one
// that does not is refused rather than written with a check-then-put that
// two writers could both pass.
func (s *KVStore) Create(ctx context.Context, tenantID string, p *Pending) error {
	if p == nil || p.Username == "" || p.Thumbprint == "" {
		return &IncompleteError{}
	}
	scoped, err := s.scoped(tenantID)
	if err != nil {
		return err
	}
	atomic, ok := scoped.(kvstore.AtomicKVStore)
	if !ok {
		return &StoreError{Op: "create", Err: kvstore.ErrAtomicWriteUnsupported}
	}
	value, err := json.Marshal(p)
	if err != nil {
		return &StoreError{Op: "encode", Err: err}
	}
	stored, err := atomic.PutIfAbsent(ctx, pendingKey, value)
	if err != nil {
		return &StoreError{Op: "create", Err: err}
	}
	if !stored {
		return &AlreadyPendingError{TenantID: tenantID}
	}
	return nil
}

// Get implements Store.
func (s *KVStore) Get(ctx context.Context, tenantID string) (*Pending, error) {
	scoped, err := s.scoped(tenantID)
	if err != nil {
		return nil, err
	}
	value, err := scoped.Get(ctx, pendingKey)
	if err != nil {
		return nil, &StoreError{Op: "get", Err: err}
	}
	if value == nil {
		return nil, nil
	}
	var p Pending
	if err := json.Unmarshal(value, &p); err != nil {
		return nil, &StoreError{Op: "decode", Err: err}
	}
	return &p, nil
}

// Delete implements Store.
func (s *KVStore) Delete(ctx context.Context, tenantID string) error {
	scoped, err := s.scoped(tenantID)
	if err != nil {
		return err
	}
	if err := scoped.Delete(ctx, pendingKey); err != nil {
		return &StoreError{Op: "delete", Err: err}
	}
	return nil
}
