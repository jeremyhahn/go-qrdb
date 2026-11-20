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

package node

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// errKeyProviderRefused stops the barrier init once the provider has been
// asked: the request is what the test asserts.
var errKeyProviderRefused = errors.New("key provider refused")

// refusingKeyProvider records every barrier-key request and refuses it.
type refusingKeyProvider struct {
	mu       sync.Mutex
	requests []seal.BarrierKeyRequest
}

func (p *refusingKeyProvider) BarrierStrategy(_ context.Context, req seal.BarrierKeyRequest) (seal.BarrierKeyStrategy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return seal.BarrierKeyStrategy{}, errKeyProviderRefused
}

var _ seal.BarrierKeyProvider = (*refusingKeyProvider)(nil)

// TestStandaloneNode_BarrierKeyProviderReachesTenantService proves the
// server option an embedder passes is the provider the node's tenant service
// asks, under the standalone node's identity: the TenantService is built
// before server.New, so the builder must forward the option itself.
func TestStandaloneNode_BarrierKeyProviderReachesTenantService(t *testing.T) {
	ctx := context.Background()
	provider := &refusingKeyProvider{}
	n, err := NewStandaloneNode(ctx, newTenantIsolationConfig(t),
		[]server.ServerOption{server.WithBarrierKeyProvider(provider)}, WithTenantIsolation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services().Tenant
	_, err = svc.CreateTenant(ctx, "acme", "acme", "")
	require.NoError(t, err)
	_, err = svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{AllowedBarrierModes: []string{"test-kms"}})
	require.NoError(t, err)

	_, err = svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, "test-kms", nil)
	require.ErrorIs(t, err, errKeyProviderRefused)
	require.Len(t, provider.requests, 1)
	require.Equal(t, "tenant/acme", provider.requests[0].Barrier)
	require.Equal(t, lifecycle.LocalNodeID, provider.requests[0].NodeID)
}

// TestWithCryptoInfra_ForwardsBarrierKeySeams pins the one place every node
// builder -- standalone, clustered, sealed or not -- hands the seal registry
// and the barrier key provider to BuildServices.
func TestWithCryptoInfra_ForwardsBarrierKeySeams(t *testing.T) {
	reg := seal.NewDefaultStrategyRegistry()
	provider := &refusingKeyProvider{}

	infra, err := withCryptoInfra(lifecycle.Infra{}, nil, []server.ServerOption{
		server.WithStrategyRegistry(reg),
		server.WithBarrierKeyProvider(provider),
	})
	require.NoError(t, err)
	require.Same(t, reg, infra.StrategyRegistry)
	require.Same(t, provider, infra.BarrierKeyProvider)
}
