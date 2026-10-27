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

package lifecycle

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newCredentialOptionsRegistry returns a barrier registry over an initialized
// system barrier with one tenant, acme, on its own initialized barrier.
func newCredentialOptionsRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	ctx := context.Background()
	newBarrier := func(pass string) *seal.StorageBarrier {
		strategy, err := seal.NewSoftwareStrategy(ctx, []byte(pass))
		require.NoError(t, err)
		b, err := seal.NewStorageBarrier(slog.Default(), seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
		require.NoError(t, err)
		require.NoError(t, b.Initialize(ctx, seal.MustCredentials(ctx, []byte(pass))))
		t.Cleanup(func() { _ = b.Close() })
		return b
	}
	registry, err := seal.NewBarrierRegistry(slog.Default(), newBarrier("system-pass"))
	require.NoError(t, err)
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("acme-pass"))
	require.NoError(t, err)
	tb, err := registry.RegisterTenant(ctx, "acme", seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
	require.NoError(t, err)
	require.NoError(t, tb.Barrier().Initialize(ctx, seal.MustCredentials(ctx, []byte("acme-pass"))))
	return registry
}

func newCredentialOptionsCore(t *testing.T) *credentials.CredentialService {
	t.Helper()
	core, err := credentials.NewCredentialService(nil, nil, slog.Default())
	require.NoError(t, err)
	return core
}

// With no barrier registry the option list is empty, so a tenant caller is
// refused rather than handed a lookup over a nil registry.
func TestCredentialServiceOptions_NilRegistryBindsNothing(t *testing.T) {
	opts := credentialServiceOptions(Infra{})
	assert.Empty(t, opts)

	svc := service.NewCredentialService(newCredentialOptionsCore(t), opts...)
	_, err := svc.CredentialStrategy(tenant.WithTenantID(context.Background(), "acme"))
	var unsupported *service.CapabilityUnsupportedError
	require.ErrorAs(t, err, &unsupported)
}

// With a barrier registry the option binds it, so a tenant with its own
// barrier is served from a barrier-strategy store over that barrier while
// the system tenant keeps the node-wide store.
func TestCredentialServiceOptions_RegistryBindsTenantStores(t *testing.T) {
	opts := credentialServiceOptions(Infra{BarrierRegistry: newCredentialOptionsRegistry(t)})
	require.Len(t, opts, 1)

	svc := service.NewCredentialService(newCredentialOptionsCore(t), opts...)
	got, err := svc.CredentialStrategy(tenant.WithTenantID(context.Background(), "acme"))
	require.NoError(t, err)
	assert.Equal(t, credentials.StrategyBarrier, got)

	got, err = svc.CredentialStrategy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, credentials.StrategyManual, got)
}
