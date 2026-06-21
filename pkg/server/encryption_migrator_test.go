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
