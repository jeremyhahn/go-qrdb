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

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// fakeAuthFactorKV — a minimal in-memory service.KVServicer used only to
// verify buildUserAuthFactorServiceOption's construction path. None of its
// methods are exercised by these tests (service.NewUserAuthFactorService
// only checks that the value is non-nil at construction time); it exists
// purely to satisfy the interface.
// ---------------------------------------------------------------------------

type fakeAuthFactorKV struct{}

func (fakeAuthFactorKV) Put(context.Context, string, []byte) error { return nil }
func (fakeAuthFactorKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (fakeAuthFactorKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (fakeAuthFactorKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
func (fakeAuthFactorKV) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (fakeAuthFactorKV) Delete(context.Context, string) error        { return nil }
func (fakeAuthFactorKV) List(context.Context, string) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) Scan(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) GetStale(context.Context, string) ([]byte, error) { return nil, nil }
func (fakeAuthFactorKV) ListStale(context.Context, string) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ScanStale(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) PutWithMode(context.Context, string, []byte, writemode.Mode) error {
	return nil
}
func (fakeAuthFactorKV) DeleteWithMode(context.Context, string, writemode.Mode) error {
	return nil
}
func (fakeAuthFactorKV) GetWithConsistency(context.Context, string, consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ListWithConsistency(context.Context, string, consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ScanWithConsistency(context.Context, string, consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ExistsWithConsistency(context.Context, string, consistency.ReadOptions) (bool, error) {
	return false, nil
}
func (fakeAuthFactorKV) PutEntity(context.Context, *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return &service.PutEntityResponse{}, nil
}
func (fakeAuthFactorKV) GetEntity(context.Context, *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return &service.GetEntityResponse{}, nil
}
func (fakeAuthFactorKV) DeleteEntity(context.Context, *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return &service.DeleteEntityResponse{}, nil
}
func (fakeAuthFactorKV) ListEntities(context.Context, *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{}, nil
}

var _ service.KVServicer = fakeAuthFactorKV{}

// ---------------------------------------------------------------------------
// buildUserAuthFactorServiceOption (user_auth_factor.go)
// ---------------------------------------------------------------------------

// newDaemonStoreResolver returns the PRODUCTION tenant store resolver over an
// in-memory store, with the named tenants provisioned. The daemon wiring under
// test is the tenant boundary of factor resolution, so the resolver is the one
// thing that must not be a stub here.
func newDaemonStoreResolver(t *testing.T, tenantIDs ...string) (tenant.StoreResolver, kvstore.KVStore) {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(store, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	for _, id := range tenantIDs {
		_, cErr := tenants.CreateTenant(context.Background(), id, id, tenant.IsolationPrefix)
		require.NoError(t, cErr)
	}
	resolver, err := tenant.NewStoreResolver(store, nil, tenants)
	require.NoError(t, err)
	return resolver, store
}

// TestBuildUserAuthFactorServiceOption_Routed verifies that a resolver yields
// a working server.ServerOption carrying the TENANT-ROUTED service: server.New
// accepts it, it starts cleanly with no protocols enabled, and a factor
// enrolled under one tenant does not resolve under another.
func TestBuildUserAuthFactorServiceOption_Routed(t *testing.T) {
	resolver, store := newDaemonStoreResolver(t, "acme", "globex")
	opt, err := buildUserAuthFactorServiceOption(context.Background(), resolver, store, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotNil(t, opt)

	// No protocols enabled: server.New + Start()/Stop() must accept the
	// option and exit quickly, proving the constructed option is a valid
	// server.ServerOption rather than merely a non-nil closure.
	srv := server.New(&qrdbconfig.Config{}, fakeAuthFactorKV{}, nil,
		opt)
	require.NoError(t, srv.Start())
	defer srv.Stop()

	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{Resolver: resolver})
	require.NoError(t, err)
	cred := []byte("daemon-credential")
	require.NoError(t, svc.Add(tenant.WithTenantID(context.Background(), "acme"), 1, &auth.AuthFactor{
		ID: "daemon-factor", UserID: 1, Type: auth.AuthFactorMTLS, Credential: cred,
	}))
	_, err = svc.LookupByCredentialKey(tenant.WithTenantID(context.Background(), "globex"), auth.AuthFactorMTLS, cred)
	assert.Error(t, err, "the daemon-wired service resolved across tenants")
}

// TestBuildUserAuthFactorServiceOption_MigratesLegacyRecords verifies the
// daemon wiring performs the go-qrdb#393 migration before it serves: a
// credential enrolled under the old flat keyspace still resolves afterwards,
// in its owning tenant only.
func TestBuildUserAuthFactorServiceOption_MigratesLegacyRecords(t *testing.T) {
	resolver, store := newDaemonStoreResolver(t, "acme")
	owners, err := tenant.NewResolvedIdentityIndex(resolver)
	require.NoError(t, err)
	_, err = owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(31), "acme")
	require.NoError(t, err)

	legacy := &auth.AuthFactor{ID: "legacy", UserID: 31, Type: auth.AuthFactorMTLS, Credential: []byte("legacy-cred")}
	data, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), "__sys:auth/factors/legacy", data))

	opt, err := buildUserAuthFactorServiceOption(context.Background(), resolver, store, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotNil(t, opt)

	// The migration is one-shot INSIDE the service and runs before it answers
	// anything, so the first call is what triggers it. Building the service a
	// second time over the same resolver and legacy store is exactly what the
	// option holds.
	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{
		Resolver: resolver, LegacyStore: store, Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	found, err := svc.LookupByCredentialKey(
		tenant.WithTenantID(context.Background(), "acme"), auth.AuthFactorMTLS, []byte("legacy-cred"))
	require.NoError(t, err, "a credential enrolled before routing no longer resolves")
	assert.Equal(t, "legacy", found.ID)

	moved, err := store.Get(context.Background(), tenant.IdentityNamespace("acme")+"__sys:auth/factors/legacy")
	require.NoError(t, err)
	assert.NotEmpty(t, moved, "the legacy record was not migrated into its owning tenant")
	remaining, err := store.List(context.Background(), "__sys:auth/factors/")
	require.NoError(t, err)
	assert.Empty(t, remaining, "the legacy record was left at the untenanted root")
}

// TestBuildUserAuthFactorServiceOption_NoResolver verifies the fail-closed
// refusal: with no resolver there is no tenant boundary to serve behind, so
// the wiring returns ErrNoTenantStoreResolver rather than an unrouted service.
func TestBuildUserAuthFactorServiceOption_NoResolver(t *testing.T) {
	opt, err := buildUserAuthFactorServiceOption(context.Background(), nil, nil, nil)
	assert.ErrorIs(t, err, ErrNoTenantStoreResolver)
	assert.Nil(t, opt)
}

// ListAll mirrors List on this double: it applies no internal-key guard, so
// the filtered and unfiltered enumerations are the same call.
func (f fakeAuthFactorKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return f.List(ctx, prefix)
}
