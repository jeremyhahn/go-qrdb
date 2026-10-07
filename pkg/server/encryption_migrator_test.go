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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

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

// flakyIsolationResolver returns quicraft.ErrReadIndexNotReady for the first
// failures calls (simulating a freshly elected leader's ReadIndex warm-up
// window right after a system-scope recycle), then info/nil.
type flakyIsolationResolver struct {
	info     *tenant.TenantInfo
	failures int
	calls    int
}

func (f *flakyIsolationResolver) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, quicraft.ErrReadIndexNotReady
	}
	return f.info, nil
}

// flakyBarrierModeChecker mirrors flakyIsolationResolver for CheckBarrierMode.
type flakyBarrierModeChecker struct {
	failures int
	calls    int
}

func (f *flakyBarrierModeChecker) CheckBarrierMode(context.Context, string, seal.StrategyID) error {
	f.calls++
	if f.calls <= f.failures {
		return quicraft.ErrReadIndexNotReady
	}
	return nil
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

// TestIsRaftTenant_RetriesReadIndexNotReady proves isRaftTenant retries the
// isolation lookup through the transient quicraft.ErrReadIndexNotReady
// window (go-qrdb#584) instead of failing closed to the volume path on the
// first hit, which would misroute a tenant-scope migration landing right
// after a system-scope recycle.
func TestIsRaftTenant_RetriesReadIndexNotReady(t *testing.T) {
	r := &flakyIsolationResolver{info: &tenant.TenantInfo{IsolationType: tenant.IsolationRaft}, failures: 2}
	m := &scopedEncryptionMigrator{isolation: r}
	if got := m.isRaftTenant(context.Background(), "t1"); !got {
		t.Fatalf("isRaftTenant = %v, want true after retrying past the transient read-index error", got)
	}
	if r.calls != 3 {
		t.Fatalf("GetTenant called %d times, want 3 (2 transient failures + 1 success)", r.calls)
	}
}

// TestScopedEncryptionMigrator_EnableEncryption_RetriesEntitlementCheck proves
// EnableEncryption's tenant-scope entitlement gate (CheckBarrierMode) retries
// through the same transient read-index window rather than refusing a
// migration that lands right after a system-scope recycle (go-qrdb#584).
func TestScopedEncryptionMigrator_EnableEncryption_RetriesEntitlementCheck(t *testing.T) {
	checker := &flakyBarrierModeChecker{failures: 2}
	m := &scopedEncryptionMigrator{
		tenants:   &TenantHostRegistry{},
		tenantSvc: checker,
	}
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"},
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase})

	if checker.calls != 3 {
		t.Fatalf("CheckBarrierMode called %d times, want 3 (2 transient failures + 1 success)", checker.calls)
	}
	if errors.Is(err, quicraft.ErrReadIndexNotReady) {
		t.Fatalf("EnableEncryption surfaced the transient read-index error instead of retrying: %v", err)
	}
	// The zero-value TenantHostRegistry refuses with ErrTenantHostUnavailable
	// once CheckBarrierMode clears -- proving the retry let the migration past
	// the entitlement gate rather than stopping there.
	if !errors.Is(err, ErrTenantHostUnavailable) {
		t.Fatalf("EnableEncryption = %v, want ErrTenantHostUnavailable (past the entitlement gate)", err)
	}
}
