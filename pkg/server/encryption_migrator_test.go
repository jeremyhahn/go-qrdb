// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeIsolationResolver is a tenantIsolationResolver returning a fixed tenant
// info or error, for routing tests.
type fakeIsolationResolver struct {
	info *tenant.TenantInfo
	err  error
}

func (f fakeIsolationResolver) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return f.info, f.err
}

// TestEffectiveEncryptionMigrator_ClusterBypass proves a wired cluster migrator
// is returned to transports directly (not wrapped in the scoped router).
func TestEffectiveEncryptionMigrator_ClusterBypass(t *testing.T) {
	sentinel := encMigratorFunc(func(context.Context, service.BarrierScope, seal.MigrationOptions) error { return nil })
	s := &Server{opts: serverOptions{
		clusterEncMigrator: sentinel,
		tenantHosts:        &TenantHostRegistry{}, // present, but bypassed by the cluster migrator
	}}
	if got := s.effectiveEncryptionMigrator(); got == nil {
		t.Fatal("effectiveEncryptionMigrator returned nil")
	} else if _, isScoped := got.(*scopedEncryptionMigrator); isScoped {
		t.Fatal("cluster migrator must NOT be wrapped in the scoped router")
	}
}

// TestEffectiveEncryptionMigrator_PassthroughWhenNoScopes proves that with no
// tenant-host/raft-backend wired (and no cluster migrator) the configured system
// migrator is returned unchanged.
func TestEffectiveEncryptionMigrator_PassthroughWhenNoScopes(t *testing.T) {
	sentinel := encMigratorFunc(func(context.Context, service.BarrierScope, seal.MigrationOptions) error { return nil })
	s := &Server{opts: serverOptions{encMigrator: sentinel}}
	got := s.effectiveEncryptionMigrator()
	if _, isScoped := got.(*scopedEncryptionMigrator); isScoped {
		t.Fatal("expected the unwrapped system migrator, got a scoped router")
	}
}

// TestEffectiveEncryptionMigrator_ComposesScopedRouter proves a scoped router is
// composed when a tenant host registry (or raft backend) is present.
func TestEffectiveEncryptionMigrator_ComposesScopedRouter(t *testing.T) {
	s := &Server{opts: serverOptions{
		encMigrator: encMigratorFunc(func(context.Context, service.BarrierScope, seal.MigrationOptions) error { return nil }),
		raftBackend: &RaftBackendFactory{},
	}}
	got := s.effectiveEncryptionMigrator()
	if _, isScoped := got.(*scopedEncryptionMigrator); !isScoped {
		t.Fatalf("expected a *scopedEncryptionMigrator, got %T", got)
	}
}

// TestIsRaftTenant covers every branch of the isolation-routing predicate.
func TestIsRaftTenant(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		m    *scopedEncryptionMigrator
		want bool
	}{
		{"nil resolver", &scopedEncryptionMigrator{}, false},
		{"resolver error", &scopedEncryptionMigrator{isolation: fakeIsolationResolver{err: errors.New("boom")}}, false},
		{"nil info", &scopedEncryptionMigrator{isolation: fakeIsolationResolver{}}, false},
		{"volume tenant", &scopedEncryptionMigrator{isolation: fakeIsolationResolver{info: &tenant.TenantInfo{IsolationType: tenant.IsolationVolume}}}, false},
		{"raft tenant", &scopedEncryptionMigrator{isolation: fakeIsolationResolver{info: &tenant.TenantInfo{IsolationType: tenant.IsolationRaft}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.isRaftTenant(ctx, "t1"); got != c.want {
				t.Fatalf("isRaftTenant = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEnableEncryption_TenantUnavailableWhenNoHandlers proves a tenant-scope
// request fails closed when neither a tenant-host nor a raft handler is wired.
func TestEnableEncryption_TenantUnavailableWhenNoHandlers(t *testing.T) {
	m := &scopedEncryptionMigrator{}
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "t1"},
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase})
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("EnableEncryption(no handlers) = %v, want ErrMigrationProviderUnavailable", err)
	}
}

// TestScopedEncryptionMigrator_TenantAdmission pins the admission hook: a
// refusing admission stops a tenant-scope migration before anything runs, an
// admitting one is released when the migration returns, and system scope
// never consults it. Both constructions — NewScopedEncryptionMigrator's option
// and the server's WithTenantMigrationAdmission — carry it.
func TestScopedEncryptionMigrator_TenantAdmission(t *testing.T) {
	refused := errors.New("closing")
	var systemCalls int
	system := encMigratorFunc(func(context.Context, service.BarrierScope, seal.MigrationOptions) error {
		systemCalls++
		return nil
	})
	tenantScope := service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}

	t.Run("refusal stops the migration", func(t *testing.T) {
		m := NewScopedEncryptionMigrator(system, nil, &RaftBackendFactory{}, nil, nil,
			WithTenantAdmission(func(context.Context) (func(), error) { return nil, refused }))
		if err := m.EnableEncryption(context.Background(), tenantScope, seal.MigrationOptions{}); !errors.Is(err, refused) {
			t.Fatalf("EnableEncryption = %v, want the admission's refusal", err)
		}
	})

	t.Run("admitted migration is released", func(t *testing.T) {
		released := 0
		s := &Server{opts: serverOptions{
			encMigrator: system,
			raftBackend: &RaftBackendFactory{},
			tenantMigrationAdmission: func(context.Context) (func(), error) {
				return func() { released++ }, nil
			},
		}}
		// The unconfigured factory refuses the recycle itself; the release
		// must follow whatever the migration returns.
		_ = s.effectiveEncryptionMigrator().EnableEncryption(context.Background(), tenantScope, seal.MigrationOptions{})
		if released != 1 {
			t.Fatalf("admission released %d times, want 1", released)
		}
	})

	t.Run("system scope is not admitted by it", func(t *testing.T) {
		m := NewScopedEncryptionMigrator(system, nil, &RaftBackendFactory{}, nil, nil,
			WithTenantAdmission(func(context.Context) (func(), error) { return nil, refused }))
		if err := m.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{}); err != nil {
			t.Fatalf("system scope = %v, want it to bypass the tenant admission", err)
		}
		if systemCalls == 0 {
			t.Fatal("the system migrator was not called")
		}
	})
}

// TestEnableEncryption_RefusesSystemShamirTarget proves a system-scope
// migration to Shamir is refused before the system migrator runs: the
// in-process data-plane restart it triggers would discard the envelopes the
// migration distributes, leaving a sealed barrier no holder could open.
func TestEnableEncryption_RefusesSystemShamirTarget(t *testing.T) {
	called := false
	m := &scopedEncryptionMigrator{system: encMigratorFunc(func(context.Context, service.BarrierScope, seal.MigrationOptions) error {
		called = true
		return nil
	})}
	err := m.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem},
		seal.MigrationOptions{TargetStrategy: seal.StrategyShamir, ShamirThreshold: 2, ShamirTotal: 3})
	if !errors.Is(err, service.ErrShamirSystemMigrationUnsupported) {
		t.Fatalf("err = %v, want service.ErrShamirSystemMigrationUnsupported", err)
	}
	if called {
		t.Fatal("the system migrator ran for a refused Shamir target")
	}
	if err := m.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem},
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase}); err != nil || !called {
		t.Fatalf("a passphrase target was not passed through: err=%v called=%v", err, called)
	}
}
