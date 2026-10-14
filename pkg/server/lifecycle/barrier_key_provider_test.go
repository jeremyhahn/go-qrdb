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
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// errProviderRefused stops a barrier init once the provider has been asked:
// the request is what these tests assert, not the init.
var errProviderRefused = errors.New("provider refused")

// capturingKeyProvider records every barrier-key request and refuses it.
type capturingKeyProvider struct {
	mu       sync.Mutex
	requests []seal.BarrierKeyRequest
}

func (p *capturingKeyProvider) BarrierStrategy(_ context.Context, req seal.BarrierKeyRequest) (seal.BarrierKeyStrategy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return seal.BarrierKeyStrategy{}, errProviderRefused
}

var _ seal.BarrierKeyProvider = (*capturingKeyProvider)(nil)

// raftAddrHost is a clustered service.Host: not a LocalBackend, and answering
// addr as its Raft address.
type raftAddrHost struct {
	service.Host
	addr string
}

func (h raftAddrHost) RaftAddress() string { return h.addr }

// newStandaloneHost returns the LocalBackend a standalone node is built on.
func newStandaloneHost() *backend.LocalBackend {
	return backend.NewLocalBackend(engine.NewKVEngine(memory.NewStorage()))
}

// newHomeRegistry returns a barrier registry over an uninitialized in-memory
// system barrier: enough to build a TenantService, which reads the registry
// only when a tenant resolves.
func newHomeRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("home-registry-passphrase"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(slog.Default(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(slog.Default(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// initThroughOpts builds a TenantService from buildTenantServiceOpts(infra)
// and starts a barrier init for strategy, returning the init's error.
func initThroughOpts(t *testing.T, infra Infra, strategy seal.StrategyID) error {
	t.Helper()
	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), newHomeRegistry(t), slog.Default(),
		buildTenantServiceOpts(infra)...)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{AllowedBarrierModes: []string{string(strategy)}}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	_, err = svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, strategy, nil)
	return err
}

// TestBuildTenantServiceOpts_ProviderAndNodeIdentity proves the injected
// provider reaches the TenantService, asked for this node's key under the
// node's identity: its Raft address, or LocalNodeID when standalone.
func TestBuildTenantServiceOpts_ProviderAndNodeIdentity(t *testing.T) {
	tests := map[string]struct {
		host   service.Host
		nodeID string
	}{
		"raft":       {host: raftAddrHost{Host: newStandaloneHost(), addr: "10.0.0.1:7000"}, nodeID: "10.0.0.1:7000"},
		"standalone": {host: newStandaloneHost(), nodeID: LocalNodeID},
		"no host":    {nodeID: LocalNodeID},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			provider := &capturingKeyProvider{}
			err := initThroughOpts(t, Infra{Host: tt.host, BarrierKeyProvider: provider}, "test-kms")
			if !errors.Is(err, errProviderRefused) {
				t.Fatalf("err = %v, want the provider's refusal", err)
			}
			if len(provider.requests) != 1 {
				t.Fatalf("provider asked %d times, want 1", len(provider.requests))
			}
			if got := provider.requests[0]; got.NodeID != tt.nodeID || got.Barrier != "tenant/acme" {
				t.Errorf("request = %+v, want NodeID %q for tenant/acme", got, tt.nodeID)
			}
		})
	}
}

// TestBuildTenantServiceOpts_ProviderWinsOverRegistry proves an injected
// provider is asked even when a StrategyRegistry is supplied too, and that a
// registry alone feeds the default provider: a strategy only it registers
// resolves past the provider.
func TestBuildTenantServiceOpts_ProviderWinsOverRegistry(t *testing.T) {
	stratReg := seal.NewDefaultStrategyRegistry()
	boom := errors.New("device unavailable")
	if err := stratReg.Register("test-kms", func() (seal.SealingStrategy, error) { return nil, boom }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	provider := &capturingKeyProvider{}
	if err := initThroughOpts(t, Infra{StrategyRegistry: stratReg, BarrierKeyProvider: provider}, "test-kms"); !errors.Is(err, errProviderRefused) {
		t.Fatalf("with provider: err = %v, want the provider's refusal", err)
	}
	if err := initThroughOpts(t, Infra{StrategyRegistry: stratReg}, "test-kms"); !errors.Is(err, boom) {
		t.Fatalf("registry only: err = %v, want the registered factory's %v", err, boom)
	}
	var invErr *tenant.InvalidStrategyError
	if err := initThroughOpts(t, Infra{}, "test-kms"); !errors.As(err, &invErr) {
		t.Fatalf("neither: err = %v, want *tenant.InvalidStrategyError", err)
	}
}
