// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// flakyGetKVStore wraps a real kvstore.KVStore and makes every Get fail with
// a fixed error -- a faithful double for "the read index is not ready" or
// "the leader was lost", driven through the REAL role/tenant store code
// rather than a hand-built *role.StoreError/*tenant.StoreError, so this
// proves the producer's own transient decision, not a test's guess at it
// (go-qrdb strict-isolation phase 0 review finding BL2).
type flakyGetKVStore struct {
	kvstore.KVStore
	getErr error
}

func (f *flakyGetKVStore) Get(context.Context, string) ([]byte, error) {
	return nil, f.getErr
}

// garbageGetKVStore wraps a real kvstore.KVStore and makes every Get
// succeed with bytes that are not valid JSON, driving the REAL
// unmarshal-failure path (a corrupt stored record) rather than asserting it.
type garbageGetKVStore struct {
	kvstore.KVStore
}

func (garbageGetKVStore) Get(context.Context, string) ([]byte, error) {
	return []byte("not valid json"), nil
}

// flakyStorageBackend wraps a real seal.StorageBackend and makes every Get
// fail with a fixed error -- the same faithful-double shape as
// flakyGetKVStore, but for TenantService, which is built over
// seal.StorageBackend rather than kvstore.KVStore.
type flakyStorageBackend struct {
	seal.StorageBackend
	getErr error
}

func (f *flakyStorageBackend) Get(context.Context, string) ([]byte, error) {
	return nil, f.getErr
}

// absentThenFailKVStore answers the FIRST Get as an absent key (nil, nil)
// and every Get after that with a fixed error. tenant.StoreResolver.Resolve
// reads the tenant's metadata first (GetTenant) and, only when that comes
// back ErrTenantNotFound, reads the tombstone second (refuseIfDeleted ->
// deletedAt) -- so this reproduces "the tenant is unknown, and the store
// cannot even answer whether it was deleted" without needing
// pkg/tenant's unexported key-prefix constants, which a test outside that
// package cannot name and must not re-derive by hand (a second copy of a
// key format is exactly the kind of duplication that silently drifts).
type absentThenFailKVStore struct {
	kvstore.KVStore
	calls  atomic.Int64
	getErr error
}

func (f *absentThenFailKVStore) Get(context.Context, string) ([]byte, error) {
	if f.calls.Add(1) == 1 {
		return nil, nil
	}
	return nil, f.getErr
}

// TestResolveErrorIsTransient_ProducerDeclaresTransience is the BL2 proof:
// every case below builds the REAL producer (role.NewStore, or a real
// tenant.TenantServicer behind a real tenant.StoreResolver) over a KV double
// that fails or corrupts in a specific way, and checks what
// ClassifyAuthError says about the error that producer actually returned --
// never a hand-built *role.StoreError/*tenant.StoreError, which could no
// longer assert its own transience anyway (the field is unexported, set only
// by role.newStoreError/tenant.newStoreError).
func TestResolveErrorIsTransient_ProducerDeclaresTransience(t *testing.T) {
	t.Run("role store read failure is transient", func(t *testing.T) {
		readErr := errors.New("read index not ready")
		svc, err := role.NewStore(&flakyGetKVStore{KVStore: kvstore.NewMemoryKVStore(), getErr: readErr})
		if err != nil {
			t.Fatalf("role.NewStore: %v", err)
		}
		_, getErr := svc.GetRole(context.Background(), "some-role-id")
		if getErr == nil {
			t.Fatal("expected an error from a Get that always fails")
		}
		resolveErr := &rbac.ResolveError{Err: getErr}
		if got := ClassifyAuthError(resolveErr); got != AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnavailable: a role store read failure "+
				"must be retryable", got)
		}
	})

	t.Run("role store corrupt record is not transient", func(t *testing.T) {
		svc, err := role.NewStore(garbageGetKVStore{KVStore: kvstore.NewMemoryKVStore()})
		if err != nil {
			t.Fatalf("role.NewStore: %v", err)
		}
		_, getErr := svc.GetRole(context.Background(), "some-role-id")
		if getErr == nil {
			t.Fatal("expected an unmarshal error from a corrupt stored record")
		}
		resolveErr := &rbac.ResolveError{Err: getErr}
		if got := ClassifyAuthError(resolveErr); got == AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want anything but AuthFailureUnavailable: "+
				"a corrupt record is definitive, not transient", got)
		}
	})

	t.Run("role store decrypt failure is not transient (N1)", func(t *testing.T) {
		// A real, unsealed TenantBarrier wrapped the way an isolated tenant's
		// home store is in production (seal.NewTenantBarrierKVStore), over a
		// backend this test holds so it can reach the ciphertext beneath.
		ctx := context.Background()
		const passphrase = "resolve-producer-decrypt-failure"
		strategy, err := seal.NewSoftwareStrategy(ctx, []byte(passphrase))
		if err != nil {
			t.Fatalf("NewSoftwareStrategy: %v", err)
		}
		backend := seal.NewMemoryBackend()
		tb, err := seal.NewTenantBarrier("acme", slog.Default(), backend, seal.BarrierConfig{DeploymentID: 1}, strategy)
		if err != nil {
			t.Fatalf("NewTenantBarrier: %v", err)
		}
		if err := tb.Initialize(ctx, seal.MustCredentials(ctx, []byte(passphrase))); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if tb.IsSealed() {
			if err := tb.Unseal(ctx, seal.MustCredentials(ctx, []byte(passphrase))); err != nil {
				t.Fatalf("Unseal: %v", err)
			}
		}
		encKV, err := seal.NewTenantBarrierKVStore(tb)
		if err != nil {
			t.Fatalf("NewTenantBarrierKVStore: %v", err)
		}
		svc, err := role.NewStore(encKV)
		if err != nil {
			t.Fatalf("role.NewStore: %v", err)
		}
		r, err := svc.CreateRole(ctx, &role.CreateRoleRequest{
			TenantID:    "acme",
			Name:        "custom-role",
			Permissions: []permission.Permission{permission.KVGet},
		})
		if err != nil {
			t.Fatalf("CreateRole: %v", err)
		}

		// Tamper with the ciphertext directly in the backend, bypassing the
		// barrier entirely, so the next read through encKV fails to decrypt
		// it -- a real seal.ErrDecryptionFailed, not an asserted one. The keys
		// come from the store's own listing, so the barrier's key material
		// beside them is left alone.
		keys, err := encKV.List(ctx, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(keys) == 0 {
			t.Fatal("expected CreateRole to have written at least one key")
		}
		tampered := false
		for _, k := range keys {
			physical := "acme/" + k
			raw, err := backend.Get(ctx, physical)
			if err != nil || raw == nil {
				continue
			}
			// The primary record is the largest value (it carries the role's
			// permissions and metadata); the identity-index/name-index entries
			// are short fixed-width ids. Garbling only the longer one keeps
			// this test from also tripping the index lookup's own, unrelated
			// decrypt path.
			if len(raw) < 64 {
				continue
			}
			garbled := append([]byte(nil), raw...)
			garbled[0] ^= 0xFF
			if err := backend.Put(ctx, physical, garbled); err != nil {
				t.Fatalf("Put (tamper): %v", err)
			}
			tampered = true
		}
		if !tampered {
			t.Fatal("found no primary record to tamper with")
		}

		_, getErr := svc.GetRole(tenant.WithTenantID(context.Background(), "acme"), r.ID)
		if getErr == nil {
			t.Fatal("expected a decrypt failure reading a tampered ciphertext")
		}
		if !errors.Is(getErr, seal.ErrDecryptionFailed) {
			t.Fatalf("got %v, want it to carry seal.ErrDecryptionFailed -- the test does not yet "+
				"reproduce a genuine decrypt failure", getErr)
		}
		resolveErr := &rbac.ResolveError{Err: getErr}
		if got := ClassifyAuthError(resolveErr); got == AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want anything but AuthFailureUnavailable: "+
				"a decrypt/authentication failure is definitive, not transient (N1)", got)
		}
	})

	t.Run("tenant service+resolver read failure is transient", func(t *testing.T) {
		readErr := errors.New("read index not ready")
		tenants, err := tenant.NewKVTenantService(
			&flakyGetKVStore{KVStore: kvstore.NewMemoryKVStore(), getErr: readErr},
			slog.Default())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		resolver, err := tenant.NewStoreResolver(kvstore.NewMemoryKVStore(), nil, tenants)
		if err != nil {
			t.Fatalf("NewStoreResolver: %v", err)
		}
		_, resolveKVErr := resolver.Resolve(context.Background(), "acme")
		if resolveKVErr == nil {
			t.Fatal("expected an error from a tenant metadata read that always fails")
		}
		resolveErr := &rbac.ResolveError{Err: resolveKVErr}
		if got := ClassifyAuthError(resolveErr); got != AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnavailable: a tenant metadata "+
				"read failure (routingRoleService.storeFor -> resolver.Resolve) must be retryable", got)
		}
	})

	t.Run("tenant service (seal.StorageBackend) read failure is transient (TenantService.GetTenant)", func(t *testing.T) {
		// tenant.TenantService is the PRODUCTION TenantServicer wired in
		// pkg/server/lifecycle and cmd/qrdbd -- not kvTenantService, which the
		// "tenant service+resolver read failure" subtest above already covers.
		// It is built over seal.StorageBackend, not kvstore.KVStore, so its own
		// GetTenant producer site needs its own double.
		readErr := errors.New("read index not ready")
		backend := &flakyStorageBackend{StorageBackend: seal.NewMemoryBackend(), getErr: readErr}
		registry, err := seal.NewBarrierRegistry(slog.Default(), sealtest.UnsealedBarrier(t))
		if err != nil {
			t.Fatalf("NewBarrierRegistry: %v", err)
		}
		tenants, err := tenant.NewTenantService(backend, registry, slog.Default())
		if err != nil {
			t.Fatalf("NewTenantService: %v", err)
		}
		resolver, err := tenant.NewStoreResolver(kvstore.NewMemoryKVStore(), nil, tenants)
		if err != nil {
			t.Fatalf("NewStoreResolver: %v", err)
		}
		_, resolveErr := resolver.Resolve(context.Background(), "acme")
		if resolveErr == nil {
			t.Fatal("expected an error from a tenant metadata read that always fails")
		}
		wrapped := &rbac.ResolveError{Err: resolveErr}
		if got := ClassifyAuthError(wrapped); got != AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnavailable: TenantService.GetTenant's "+
				"own read failure (the production resolver path) must be retryable", got)
		}
	})

	t.Run("tenant tombstone read failure is transient (refuseIfDeleted/deletedAt, N2)", func(t *testing.T) {
		// No test drove this producer site at all: refuseIfDeleted's own
		// tombstone read (pkg/tenant/tombstone.go, N2), reached only when
		// GetTenant has already answered ErrTenantNotFound (an unknown, not a
		// failed, metadata read) and the resolver then asks "was this id
		// actually deleted?" The double answers the first Get (metadata) as
		// absent and every Get after that (the tombstone) with an error.
		readErr := errors.New("read index not ready")
		tenants, err := tenant.NewKVTenantService(
			&absentThenFailKVStore{KVStore: kvstore.NewMemoryKVStore(), getErr: readErr},
			slog.Default())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		resolver, err := tenant.NewStoreResolver(kvstore.NewMemoryKVStore(), nil, tenants)
		if err != nil {
			t.Fatalf("NewStoreResolver: %v", err)
		}
		_, resolveErr := resolver.Resolve(context.Background(), "acme")
		if resolveErr == nil {
			t.Fatal("expected an error from refuseIfDeleted's tombstone read, which always fails")
		}
		if errors.Is(resolveErr, tenant.ErrTenantNotFound) {
			t.Fatal("expected the tombstone read's OWN error, not the metadata read's ErrTenantNotFound " +
				"falling through unanswered")
		}
		wrapped := &rbac.ResolveError{Err: resolveErr}
		if got := ClassifyAuthError(wrapped); got != AuthFailureUnavailable {
			t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnavailable: refuseIfDeleted's tombstone "+
				"read failure must be retryable, not a silent fall-through to the shared store nor a "+
				"non-retryable deny", got)
		}
	})
}
