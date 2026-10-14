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

package tenant

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recordingKeyProvider records every request and answers it with the
// configured result, or with the default provider's when none is set.
type recordingKeyProvider struct {
	mu       sync.Mutex
	requests []seal.BarrierKeyRequest
	result   *seal.BarrierKeyStrategy
	err      error
}

var _ seal.BarrierKeyProvider = (*recordingKeyProvider)(nil)

func (p *recordingKeyProvider) BarrierStrategy(ctx context.Context, req seal.BarrierKeyRequest) (seal.BarrierKeyStrategy, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if p.err != nil {
		return seal.BarrierKeyStrategy{}, p.err
	}
	if p.result != nil {
		return *p.result, nil
	}
	return seal.NewBarrierKeyProvider(nil).BarrierStrategy(ctx, req)
}

// newKeyProviderService builds a TenantService over provider and creates
// tenantID, ready for a barrier init.
func newKeyProviderService(t *testing.T, tenantID string, provider seal.BarrierKeyProvider, opts ...TenantServiceOption) *TenantService {
	t.Helper()
	opts = append([]TenantServiceOption{WithBarrierKeyProvider(provider)}, opts...)
	svc, err := NewTenantService(seal.NewMemoryBackend(), newTestBarrierRegistry(t), testLogger(), opts...)
	require.NoError(t, err)
	_, err = svc.CreateTenant(context.Background(), tenantID, tenantID, "")
	require.NoError(t, err)
	return svc
}

// TestTenantBarrierInit_RequestNamesBarrierNodeAndKeyRef pins what a node
// tells the provider: the tenant barrier, its own identity, the strategy, the
// key reference the caller passed and the first epoch.
func TestTenantBarrierInit_RequestNamesBarrierNodeAndKeyRef(t *testing.T) {
	const tenantID = "key-request"
	provider := &recordingKeyProvider{}
	svc := newKeyProviderService(t, tenantID, provider, WithNodeID("10.0.0.1:7000"))

	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), tenantID, nil, 0, 0, seal.StrategyNone, nil, WithBarrierKeyRef("slot-9"))
	require.NoError(t, err)

	require.Len(t, provider.requests, 1)
	require.Equal(t, seal.BarrierKeyRequest{
		Barrier:    "tenant/" + tenantID,
		NodeID:     "10.0.0.1:7000",
		StrategyID: seal.StrategyNone,
		KeyRef:     "slot-9",
		Epoch:      1,
	}, provider.requests[0])
}

// TestTenantBarrierInit_NoKeyRefOrNodeID leaves both empty when neither the
// service nor the caller names them.
func TestTenantBarrierInit_NoKeyRefOrNodeID(t *testing.T) {
	const tenantID = "key-request-bare"
	provider := &recordingKeyProvider{}
	svc := newKeyProviderService(t, tenantID, provider)

	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), tenantID, nil, 0, 0, seal.StrategyNone, nil)
	require.NoError(t, err)

	require.Len(t, provider.requests, 1)
	require.Empty(t, provider.requests[0].NodeID)
	require.Empty(t, provider.requests[0].KeyRef)
}

func TestTenantBarrierInit_ProviderErrorIsBarrierError(t *testing.T) {
	const tenantID = "key-provider-refuses"
	errRefused := errors.New("device unavailable")
	svc := newKeyProviderService(t, tenantID, &recordingKeyProvider{err: errRefused})

	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), tenantID, nil, 0, 0, seal.StrategyNone, nil)

	var barrierErr *BarrierError
	require.ErrorAs(t, err, &barrierErr)
	require.Equal(t, "strategy", barrierErr.Op)
	require.ErrorIs(t, err, errRefused)
}

func TestTenantBarrierInit_ProviderNilStrategy(t *testing.T) {
	const tenantID = "key-provider-nil"
	svc := newKeyProviderService(t, tenantID, &recordingKeyProvider{result: &seal.BarrierKeyStrategy{}})

	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), tenantID, nil, 0, 0, seal.StrategyNone, nil)

	var barrierErr *BarrierError
	require.ErrorAs(t, err, &barrierErr)
	require.ErrorIs(t, err, ErrNilBarrierStrategy)
}

func TestTenantBarrierInit_ProviderNotRegisteredIsInvalidStrategy(t *testing.T) {
	const tenantID = "key-provider-unregistered"
	svc := newKeyProviderService(t, tenantID, &recordingKeyProvider{
		err: &seal.StrategyNotRegisteredError{Name: seal.StrategyNone, Available: []seal.StrategyID{seal.StrategyPassphrase}},
	})

	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), tenantID, nil, 0, 0, seal.StrategyNone, nil)

	var invalid *InvalidStrategyError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, string(seal.StrategyNone), invalid.Strategy)
	require.Equal(t, []string{string(seal.StrategyPassphrase)}, invalid.Available)
}
