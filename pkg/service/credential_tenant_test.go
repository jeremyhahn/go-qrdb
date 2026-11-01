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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantCredentialFixture is a node with a barrier-strategy node-wide store
// over the system barrier and two tenants, acme and globex, each with its own
// initialized barrier.
type tenantCredentialFixture struct {
	svc      CredentialServicer
	registry *seal.BarrierRegistry
	system   context.Context
	acme     context.Context
	globex   context.Context
}

func newTenantCredentialFixture(t *testing.T) *tenantCredentialFixture {
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
	inner, err := credentials.NewCredentialService(&credentials.Config{Strategy: credentials.StrategyBarrier}, sysBarrier, testLogger())
	require.NoError(t, err)
	return &tenantCredentialFixture{
		svc:      NewCredentialService(inner, WithCredentialTenantBarriers(registry)),
		registry: registry,
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
	svc := NewCredentialService(manual, WithCredentialTenantBarriers(registry))

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

var errTenantLookup = errors.New("tenant lookup failed")

type failingTenantBarriers struct{}

func (failingTenantBarriers) GetTenant(string) (*seal.TenantBarrier, error) {
	return nil, errTenantLookup
}

var _ CredentialTenantBarriers = failingTenantBarriers{}
