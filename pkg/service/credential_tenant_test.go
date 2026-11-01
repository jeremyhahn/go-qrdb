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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantCredentialFixture is a node with a barrier-strategy node-wide store
// over the system barrier and two tenants, acme and globex, each with its own
// initialized barrier.
type tenantCredentialFixture struct {
	svc      CredentialServicer
	registry *seal.BarrierRegistry
	homes    *memoryHomes
	system   context.Context
	acme     context.Context
	globex   context.Context
}

func newTenantCredentialFixture(t *testing.T, opts ...credentials.Option) *tenantCredentialFixture {
	t.Helper()
	ctx := context.Background()
	sysBarrier, _ := newTestSoftwareBarrier(t)
	require.NoError(t, sysBarrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("sys-pass"))))
	registry, err := seal.NewBarrierRegistry(testLogger(), sysBarrier)
	require.NoError(t, err)
	for _, id := range []string{"acme", "globex"} {
		strategy, err := seal.NewSoftwareStrategy(ctx, []byte(id+"-pass"))
		require.NoError(t, err)
		tb, err := registry.RegisterTenant(ctx, id, seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
		require.NoError(t, err)
		require.NoError(t, tb.Barrier().Initialize(ctx, seal.MustCredentials(ctx, []byte(id+"-pass"))))
	}
	inner, err := credentials.NewCredentialService(&credentials.Config{Strategy: credentials.StrategyBarrier}, sysBarrier, testLogger(), opts...)
	require.NoError(t, err)
	svc := NewCredentialService(inner, WithCredentialTenantBarriers(registry)).(*CredentialService)
	homes := newMemoryHomes()
	svc.SetTenantHomes(homes)
	return &tenantCredentialFixture{
		svc:      svc,
		registry: registry,
		homes:    homes,
		system:   ctx,
		acme:     tenant.WithTenantID(ctx, "acme"),
		globex:   tenant.WithTenantID(ctx, "globex"),
	}
}

// A credential acme stores is invisible to globex under every verb, and the
// node-wide store neither sees it nor lends its own to a tenant.
func TestCredentialService_TenantStoresAreIsolated(t *testing.T) {
	f := newTenantCredentialFixture(t)
	require.NoError(t, f.svc.CredentialStore(f.acme, "db-pass", []byte("acme-secret")))
	require.NoError(t, f.svc.CredentialStore(f.system, "node-pass", []byte("node-secret")))

	names, err := f.svc.CredentialList(f.globex)
	require.NoError(t, err)
	assert.Empty(t, names, "globex lists acme's credentials")

	_, err = retrieveBytesFrom(f.globex, f.svc, "db-pass")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
	_, err = f.svc.CredentialRetrieveVersionBuffer(f.globex, "db-pass", 0)
	assert.Error(t, err, "globex retrieves a version of acme's credential")
	versions, err := f.svc.CredentialListVersions(f.globex, "db-pass")
	if err == nil {
		assert.Empty(t, versions, "globex lists versions of acme's credential")
	}

	// Whatever globex's rotate does in its own store, acme's value stands.
	_, _ = f.svc.CredentialRotate(f.globex, "db-pass", credentials.RotateOptions{NewValue: []byte("globex-overwrite")})
	_ = f.svc.CredentialDelete(f.globex, "db-pass")
	got, err := retrieveBytesFrom(f.acme, f.svc, "db-pass")
	require.NoError(t, err)
	assert.Equal(t, []byte("acme-secret"), got)

	names, err = f.svc.CredentialList(f.acme)
	require.NoError(t, err)
	assert.Equal(t, []string{"db-pass"}, names, "acme's store holds the node-wide credential")

	names, err = f.svc.CredentialList(f.system)
	require.NoError(t, err)
	assert.Equal(t, []string{"node-pass"}, names, "the node-wide store holds a tenant credential")
	_, err = retrieveBytesFrom(f.acme, f.svc, "node-pass")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

// The full versioned rotation API works against a tenant's own store.
func TestCredentialService_TenantRotationLifecycle(t *testing.T) {
	f := newTenantCredentialFixture(t)
	require.NoError(t, f.svc.CredentialSubmit(f.acme, "api-key", []byte("v1")))
	res, err := f.svc.CredentialRotate(f.acme, "api-key", credentials.RotateOptions{NewValue: []byte("v2"), GracePeriod: 3600e9})
	require.NoError(t, err)
	assert.Greater(t, res.NewVersion, res.PreviousVersion)

	versions, err := f.svc.CredentialListVersions(f.acme, "api-key")
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	buf, err := f.svc.CredentialRetrieveVersionBuffer(f.acme, "api-key", res.PreviousVersion)
	require.NoError(t, err)
	prev, err := CredentialWireValue(buf)
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), prev)

	reaped, err := f.svc.CredentialGCExpired(f.acme, "api-key")
	require.NoError(t, err)
	assert.Zero(t, reaped, "a version still in its grace period was reaped")

	require.NoError(t, f.svc.CredentialDelete(f.acme, "api-key"))
	_, err = retrieveBytesFrom(f.acme, f.svc, "api-key")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
}

// A tenant's store is always barrier-backed; the system tenant reports the
// node-wide store's own strategy.
func TestCredentialService_StrategyFollowsTheCallersStore(t *testing.T) {
	ctx := context.Background()
	registry, err := seal.NewBarrierRegistry(testLogger(), func() *seal.StorageBarrier {
		b, _ := newTestSoftwareBarrier(t)
		require.NoError(t, b.Initialize(ctx, seal.MustCredentials(ctx, []byte("sys-pass"))))
		return b
	}())
	require.NoError(t, err)
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("acme-pass"))
	require.NoError(t, err)
	tb, err := registry.RegisterTenant(ctx, "acme", seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
	require.NoError(t, err)
	require.NoError(t, tb.Barrier().Initialize(ctx, seal.MustCredentials(ctx, []byte("acme-pass"))))
	manual, err := credentials.NewCredentialService(&credentials.Config{Strategy: credentials.StrategyManual}, nil, testLogger())
	require.NoError(t, err)
	svc := NewCredentialService(manual, WithCredentialTenantBarriers(registry)).(*CredentialService)
	svc.SetTenantHomes(newMemoryHomes())

	got, err := svc.CredentialStrategy(ctx)
	require.NoError(t, err)
	assert.Equal(t, "manual", got)
	got, err = svc.CredentialStrategy(tenant.WithTenantID(ctx, "acme"))
	require.NoError(t, err)
	assert.Equal(t, "barrier", got)
}

// credentialVerbs drives every verb of the servicer once for the refusal
// tables below.
var credentialVerbs = map[string]func(context.Context, CredentialServicer) error{
	"store": func(ctx context.Context, s CredentialServicer) error {
		return s.CredentialStore(ctx, "n", []byte("v"))
	},
	"retrieve": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialRetrieveBuffer(ctx, "n")
		return err
	},
	"submit": func(ctx context.Context, s CredentialServicer) error {
		return s.CredentialSubmit(ctx, "n", []byte("v"))
	},
	"delete": func(ctx context.Context, s CredentialServicer) error {
		return s.CredentialDelete(ctx, "n")
	},
	"strategy": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialStrategy(ctx)
		return err
	},
	"list": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialList(ctx)
		return err
	},
	"rotate": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialRotate(ctx, "n", credentials.RotateOptions{NewValue: []byte("v")})
		return err
	},
	"retrieve_version": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialRetrieveVersionBuffer(ctx, "n", 0)
		return err
	},
	"list_versions": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialListVersions(ctx, "n")
		return err
	},
	"gc_expired": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialGCExpired(ctx, "n")
		return err
	},
	"set_rotation_policy": func(ctx context.Context, s CredentialServicer) error {
		return s.CredentialSetRotationPolicy(ctx, "n", credentials.RotationPolicy{})
	},
	"rotation_policy": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialRotationPolicy(ctx, "n")
		return err
	},
	"migration_conflicts": func(ctx context.Context, s CredentialServicer) error {
		_, err := s.CredentialMigrationConflicts(ctx)
		return err
	},
	"resolve_migration_conflict": func(ctx context.Context, s CredentialServicer) error {
		return s.CredentialResolveMigrationConflict(ctx, "n", credentials.MigrationDiscard)
	},
}

// A tenant caller neither reads, disables nor replaces the rotation policy of
// a system credential of the same name: the policy verbs resolve the caller's
// own store like every other verb.
func TestCredentialService_TenantCannotTouchSystemRotationPolicy(t *testing.T) {
	f := newTenantCredentialFixture(t)
	require.NoError(t, f.svc.CredentialStore(f.system, "node-pass", []byte("node-secret")))
	sysPolicy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, f.svc.CredentialSetRotationPolicy(f.system, "node-pass", sysPolicy))

	_, err := f.svc.CredentialRotationPolicy(f.acme, "node-pass")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound, "acme reads the system credential's policy")
	err = f.svc.CredentialSetRotationPolicy(f.acme, "node-pass", credentials.RotationPolicy{})
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound, "acme disables the system credential's policy")
	err = f.svc.CredentialSetRotationPolicy(f.acme, "node-pass", credentials.RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1})
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound, "acme replaces the system credential's policy")

	got, err := f.svc.CredentialRotationPolicy(f.system, "node-pass")
	require.NoError(t, err)
	assert.Equal(t, sysPolicy, got.Policy)

	// acme's own credential of the same name carries its own policy.
	require.NoError(t, f.svc.CredentialStore(f.acme, "node-pass", []byte("acme-secret")))
	acmePolicy := credentials.RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1}
	require.NoError(t, f.svc.CredentialSetRotationPolicy(f.acme, "node-pass", acmePolicy))
	got, err = f.svc.CredentialRotationPolicy(f.acme, "node-pass")
	require.NoError(t, err)
	assert.Equal(t, acmePolicy, got.Policy)
	got, err = f.svc.CredentialRotationPolicy(f.system, "node-pass")
	require.NoError(t, err)
	assert.Equal(t, sysPolicy, got.Policy, "acme's policy reached the system store")
}

// A servicer built without tenant barriers refuses a tenant caller on every
// verb instead of serving it the node-wide store.
func TestCredentialService_UnboundTenantStoresRefuse(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for name, verb := range credentialVerbs {
		t.Run(name, func(t *testing.T) {
			var unsupported *CapabilityUnsupportedError
			assert.True(t, errors.As(verb(ctx, svc), &unsupported), "want *CapabilityUnsupportedError")
		})
	}
}

// A tenant with no dedicated barrier (one on the shared system barrier) is
// refused on every verb.
func TestCredentialService_SharedBarrierTenantRefuses(t *testing.T) {
	f := newTenantCredentialFixture(t)
	ctx := tenant.WithTenantID(context.Background(), "initech")
	for name, verb := range credentialVerbs {
		t.Run(name, func(t *testing.T) {
			var unsupported *CapabilityUnsupportedError
			err := verb(ctx, f.svc)
			require.True(t, errors.As(err, &unsupported), "got %v, want *CapabilityUnsupportedError", err)
			assert.Contains(t, unsupported.Message, `"initech"`)
		})
	}
}

// A sealed tenant barrier refuses its tenant's verbs with *BarrierSealedError
// and leaves every other store serving.
func TestCredentialService_SealedTenantRefuses(t *testing.T) {
	f := newTenantCredentialFixture(t)
	require.NoError(t, f.svc.CredentialStore(f.globex, "n", []byte("v")))
	tb, err := f.registry.GetTenant("acme")
	require.NoError(t, err)
	require.NoError(t, tb.Seal())
	for name, verb := range credentialVerbs {
		t.Run(name, func(t *testing.T) {
			var sealed *BarrierSealedError
			err := verb(f.acme, f.svc)
			require.True(t, errors.As(err, &sealed), "got %v, want *BarrierSealedError", err)
			assert.Equal(t, "acme", sealed.TenantID)
		})
	}
	got, err := retrieveBytesFrom(f.globex, f.svc, "n")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}

// A registry lookup failure other than an unknown tenant is returned as is.
func TestCredentialService_TenantLookupErrorPassesThrough(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t).(*CredentialService)
	svc.tenants = failingTenantBarriers{}
	_, err := svc.CredentialList(tenant.WithTenantID(context.Background(), "acme"))
	assert.ErrorIs(t, err, errTenantLookup)
}

// TestCredentialService_TenantWithoutHomesRefuses: a tenant caller on a node
// whose home stores are not attached, or were detached, is refused with a
// CapabilityUnsupportedError rather than served from the node-local barrier.
func TestCredentialService_TenantWithoutHomesRefuses(t *testing.T) {
	f := newTenantCredentialFixture(t)
	svc := f.svc.(*CredentialService)
	svc.SetTenantHomes(nil)
	for name, verb := range credentialVerbs {
		t.Run(name, func(t *testing.T) {
			var unsupported *CapabilityUnsupportedError
			assert.True(t, errors.As(verb(f.acme, svc), &unsupported), "want *CapabilityUnsupportedError")
		})
	}
	require.NoError(t, svc.CredentialStore(f.system, "sys-only", []byte("v")),
		"a system caller needs no tenant home store")
}

// TestCredentialService_TenantHomeStoreErrors: a home store the source cannot
// return, or one that serves no conditional writes, fails the tenant call.
func TestCredentialService_TenantHomeStoreErrors(t *testing.T) {
	f := newTenantCredentialFixture(t)
	svc := f.svc.(*CredentialService)

	homes := newMemoryHomes()
	homes.err = errHomeLookup
	svc.SetTenantHomes(homes)
	_, err := svc.CredentialList(f.acme)
	assert.ErrorIs(t, err, errHomeLookup)

	homes = newMemoryHomes()
	homes.plain = true
	svc.SetTenantHomes(homes)
	_, err = svc.CredentialList(f.acme)
	assert.ErrorIs(t, err, credentials.ErrStoreNotAtomic)
}

var errHomeLookup = errors.New("home store lookup failed")

// memoryHomes is a tenant.HomeStoreSource over one in-memory atomic KV per
// tenant, as a cluster's home groups serve one store per tenant to every node.
type memoryHomes struct {
	mu     sync.Mutex
	stores map[string]*kvstore.MemoryKVStore
	err    error
	plain  bool
}

var _ tenant.HomeStoreSource = (*memoryHomes)(nil)

func newMemoryHomes() *memoryHomes {
	return &memoryHomes{stores: make(map[string]*kvstore.MemoryKVStore)}
}

func (h *memoryHomes) EnsureHome(ctx context.Context, tenantID string) error {
	_, err := h.HomeStore(ctx, tenantID)
	return err
}

func (h *memoryHomes) HomeStore(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if h.err != nil {
		return nil, h.err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	store, ok := h.stores[tenantID]
	if !ok {
		store = kvstore.NewMemoryKVStore()
		h.stores[tenantID] = store
	}
	if h.plain {
		return struct{ kvstore.KVStore }{store}, nil
	}
	return store, nil
}

// A scheduled rotation reaches a tenant's own store: a due policy in acme's
// store rotates acme's credential under acme's context, and globex's
// credential and the system credential of the same name are untouched.
func TestCredentialService_RotateDueRotatesATenantsCredential(t *testing.T) {
	var sources []string
	f := newTenantCredentialFixture(t, credentials.WithRotationValueSource(
		func(ctx context.Context, name string) (*secure.Buffer, error) {
			id := tenant.FromContext(ctx)
			sources = append(sources, id+"/"+name)
			return secure.NewBufferFromBytes([]byte(id + "-rotated"))
		}))
	for _, c := range []struct {
		ctx   context.Context
		value string
	}{{f.system, "sys-secret"}, {f.acme, "acme-secret"}, {f.globex, "globex-secret"}} {
		require.NoError(t, f.svc.CredentialStore(c.ctx, "db-pass", []byte(c.value)))
	}
	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1}
	require.NoError(t, f.svc.CredentialSetRotationPolicy(f.acme, "db-pass", policy))

	rotated, err := f.svc.(*CredentialService).CredentialRotateDue(context.Background(), time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, rotated)
	assert.Equal(t, []string{"acme/db-pass"}, sources, "the source was not called once under acme's context")
	assert.Equal(t, "acme-rotated", retrieveTenantCredential(f.acme, t, f.svc, "db-pass"))
	assert.Equal(t, "globex-secret", retrieveTenantCredential(f.globex, t, f.svc, "db-pass"))
	assert.Equal(t, "sys-secret", retrieveTenantCredential(f.system, t, f.svc, "db-pass"))
	got, err := f.svc.CredentialRotationPolicy(f.globex, "db-pass")
	require.NoError(t, err)
	assert.False(t, got.Policy.Enabled, "acme's policy reached globex's store")
}

// A sealed tenant's store is skipped, not reported: its policies fall due at
// the first sweep after it is unsealed.
func TestCredentialService_RotateDueSkipsASealedTenant(t *testing.T) {
	var calls int
	f := newTenantCredentialFixture(t, credentials.WithRotationValueSource(
		func(context.Context, string) (*secure.Buffer, error) {
			calls++
			return secure.NewBufferFromBytes([]byte("rotated"))
		}))
	require.NoError(t, f.svc.CredentialStore(f.acme, "db-pass", []byte("acme-secret")))
	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1}
	require.NoError(t, f.svc.CredentialSetRotationPolicy(f.acme, "db-pass", policy))
	tb, err := f.registry.GetTenant("acme")
	require.NoError(t, err)
	require.NoError(t, tb.Seal())

	rotated, err := f.svc.(*CredentialService).CredentialRotateDue(context.Background(), time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	assert.Zero(t, rotated)
	assert.Zero(t, calls)
}

// A tenant whose store cannot be resolved is reported by ID, wrapping the
// cause, and does not stop the system store's sweep.
func TestCredentialService_RotateDueReportsATenantFailure(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t).(*CredentialService)
	svc.tenants = failingTenantBarriers{}
	_, err := svc.CredentialRotateDue(context.Background(), time.Now())
	var tenantErr *CredentialTenantRotationError
	require.ErrorAs(t, err, &tenantErr)
	assert.Equal(t, "acme", tenantErr.TenantID)
	assert.ErrorIs(t, err, errTenantLookup)
	assert.Contains(t, err.Error(), `tenant "acme"`)
}

// TestCredentialService_TenantMigrationConflictResolves verifies a tenant's
// node-local credentials/ record that differs from its replicated record is
// listed as a conflict for that tenant alone, and that importing it makes
// the node-local value the current version.
func TestCredentialService_TenantMigrationConflictResolves(t *testing.T) {
	f := newTenantCredentialFixture(t)
	tb, err := f.registry.GetTenant("acme")
	require.NoError(t, err)
	b := tb.Barrier()
	kv, err := f.homes.HomeStore(f.system, "acme")
	require.NoError(t, err)
	home, err := credentials.NewKVStore(kv)
	require.NoError(t, err)
	live, err := credentials.NewCredentialService(&credentials.Config{Strategy: credentials.StrategyBarrier}, home, testLogger())
	require.NoError(t, err)
	require.NoError(t, live.StoreCredential(f.system, "db", []byte("replicated")))
	require.NoError(t, b.Put(f.system, "credentials/db", []byte("node-local")))

	conflicts, err := f.svc.CredentialMigrationConflicts(f.acme)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "db", conflicts[0].Name)
	assert.True(t, conflicts[0].Value)
	assert.Equal(t, "replicated", retrieveTenantCredential(f.acme, t, f.svc, "db"))

	other, err := f.svc.CredentialMigrationConflicts(f.globex)
	require.NoError(t, err)
	assert.Empty(t, other, "globex sees acme's conflict")

	err = f.svc.CredentialResolveMigrationConflict(f.acme, "missing", credentials.MigrationImport)
	assert.ErrorIs(t, err, credentials.ErrMigrationConflictNotFound)
	err = f.svc.CredentialResolveMigrationConflict(f.acme, "db", "merge")
	assert.ErrorIs(t, err, credentials.ErrInvalidMigrationResolution)

	require.NoError(t, f.svc.CredentialResolveMigrationConflict(f.acme, "db", credentials.MigrationImport))
	assert.Equal(t, "node-local", retrieveTenantCredential(f.acme, t, f.svc, "db"))
	conflicts, err = f.svc.CredentialMigrationConflicts(f.acme)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

func retrieveTenantCredential(ctx context.Context, t *testing.T, svc CredentialServicer, name string) string {
	t.Helper()
	buf, err := svc.CredentialRetrieveBuffer(ctx, name)
	require.NoError(t, err)
	defer buf.Destroy()
	var out string
	require.NoError(t, buf.WithBytes(func(b []byte) error {
		out = string(b)
		return nil
	}))
	return out
}

var errTenantLookup = errors.New("tenant lookup failed")

type failingTenantBarriers struct{}

func (failingTenantBarriers) GetTenant(string) (*seal.TenantBarrier, error) {
	return nil, errTenantLookup
}

func (failingTenantBarriers) ListTenants() []string { return []string{"acme"} }

var _ CredentialTenantBarriers = failingTenantBarriers{}
