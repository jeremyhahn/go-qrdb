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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// barrierModeChecker is the narrow slice of the tenant service the migrator
// needs: gate a tenant-scope migration's target strategy against the tenant's
// entitlements (same governance as barrier-init). Satisfied by
// tenant.TenantServicer.
type barrierModeChecker interface {
	CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error
}

// TenantLookup is the narrow slice of the tenant service that reads one
// tenant's metadata. It routes a tenant-scope migration to the mechanism that
// owns the tenant's at-rest stores -- IsolationRaft tenants keep their data in
// dedicated raft shards on the shared host (RaftBackendFactory), all other
// dedicated-barrier tenants in a per-tenant volume Host (TenantHostRegistry) --
// and it is how TenantHostRegistry finds a tenant's DataRoot. Satisfied by
// tenant.TenantServicer.
//
// A component that holds a TenantLookup for longer than one lifecycle of the
// tenant service must be given one that resolves the CURRENT service on every
// call; see TenantServiceResolver for why a captured handle goes stale.
type TenantLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// TenantServiceResolver is the narrow slice of a tenant service
// NewScopedEncryptionMigrator consults: barrierModeChecker's entitlement gate
// plus TenantLookup's routing lookup. tenant.TenantServicer
// satisfies it structurally, so a caller that already holds a stable
// tenant.TenantServicer (the daemon's server.Server, which builds it once and
// never swaps it) passes it directly.
//
// A caller whose tenant-service handle CAN be swapped out from under it
// across a lifecycle event must instead pass an adapter that resolves the
// CURRENT handle on every call rather than a value captured once here. That
// is sdk/go/node's case: wireClientAndServer builds the router once per
// data-plane build, but the router is deliberately kept alive across a
// system-scope migration's teardownDataPlane→rebuild window (see
// Node.retractDataPlaneServices) so a concurrent tenant-scope migration keeps
// working — svcNode, and the *tenant.Service hanging off it, is swapped
// during that window (potentially for as long as it takes a Security Officer
// quorum to act, when the committed target is Shamir; see
// nodeMigrationProvider.Reopen). A captured handle would silently keep
// routing the concurrent migration's entitlement and isolation checks
// against a STOPPED service for the whole window; go-qrdb#222 is exactly
// that defect.
type TenantServiceResolver interface {
	barrierModeChecker
	TenantLookup
}

// scopedEncryptionMigrator routes a scoped enable-encryption request to the
// mechanism that owns the targeted barrier's at-rest stores:
//
//   - ScopeTenant → the per-tenant volume Host registry, which recycles the
//     tenant's IsolationVolume (re-encrypting both the state-machine Pebble and
//     the Raft WAL) in place. Only a tenant that owns a DEDICATED barrier may be
//     migrated; a tenant on the shared system barrier inherits the system
//     settings and is rejected with service.ErrSharedBarrierImmutable.
//   - ScopeSystem → the configured system migrator (the SDK *node.Node on the
//     embedded path), which recycles the node's own data plane.
//
// It is the single composition point both deployment paths flow through
// (server.New): the daemon wires only the tenant registry, the embedded node
// wires only the system migrator, and either may wire both.
type scopedEncryptionMigrator struct {
	// admitTenant, when set, is consulted before a tenant-scope migration
	// runs and its release after; see TenantMigrationAdmission.
	admitTenant TenantMigrationAdmission

	system     service.EncryptionMigrator // system-scope handler; may be nil
	tenants    *TenantHostRegistry        // IsolationVolume tenant-scope handler; may be nil
	raft       *RaftBackendFactory        // IsolationRaft tenant-scope handler; may be nil
	barrierReg *seal.BarrierRegistry      // dedicated-barrier authority check
	tenantSvc  barrierModeChecker         // barrier-mode entitlement check; may be nil
	isolation  TenantLookup               // routes tenant scope by isolation mode; may be nil
}

// Compile-time check.
var _ service.EncryptionMigrator = (*scopedEncryptionMigrator)(nil)

// TenantMigrationAdmission admits a tenant-scope migration: it returns a
// release the router calls when the migration returns, or an error that
// refuses the migration before anything runs. The SDK node supplies one so
// its Close waits for in-flight tenant migrations and refuses new ones
// (go-qrdb#226). Tenant migrations admitted together still run concurrently.
type TenantMigrationAdmission func(ctx context.Context) (release func(), err error)

// ScopedMigratorOption configures NewScopedEncryptionMigrator.
type ScopedMigratorOption func(*scopedEncryptionMigrator)

// WithTenantAdmission sets the router's TenantMigrationAdmission.
func WithTenantAdmission(a TenantMigrationAdmission) ScopedMigratorOption {
	return func(m *scopedEncryptionMigrator) { m.admitTenant = a }
}

// NewScopedEncryptionMigrator builds the local, in-process scoped encryption
// migrator — the router that performs the actual recycle (system → systemMig;
// IsolationRaft tenant → per-shard; IsolationVolume tenant → host). The daemon
// uses it as the cluster reconciler's LocalRecycler so a reconciler-driven
// recycle reuses the exact same routing, authority, and entitlement gates the
// transports use. tenantSvc may be nil (disables the entitlement + isolation
// gates); see TenantServiceResolver for who should pass a value directly
// versus a lazy-resolving adapter.
func NewScopedEncryptionMigrator(
	system service.EncryptionMigrator,
	tenants *TenantHostRegistry,
	raft *RaftBackendFactory,
	barrierReg *seal.BarrierRegistry,
	tenantSvc TenantServiceResolver,
	opts ...ScopedMigratorOption,
) service.EncryptionMigrator {
	m := &scopedEncryptionMigrator{
		system:     system,
		tenants:    tenants,
		raft:       raft,
		barrierReg: barrierReg,
	}
	if tenantSvc != nil {
		m.tenantSvc = tenantSvc
		m.isolation = tenantSvc
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// EnableEncryption dispatches by scope. It returns
// service.ErrMigrationProviderUnavailable when no handler is wired for the
// requested scope on this node.
func (m *scopedEncryptionMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	switch scope.Kind {
	case service.ScopeTenant:
		if m.tenants == nil && m.raft == nil {
			return service.ErrMigrationProviderUnavailable
		}
		if m.admitTenant != nil {
			release, err := m.admitTenant(ctx)
			if err != nil {
				return err
			}
			defer release()
		}
		// Authority: a tenant may only migrate its OWN dedicated barrier. A
		// tenant without a registered dedicated barrier is on the shared system
		// barrier and cannot change its encryption.
		if m.barrierReg != nil {
			if _, err := m.barrierReg.GetTenant(scope.TenantID); err != nil {
				if errors.Is(err, seal.ErrTenantNotFound) {
					return service.ErrSharedBarrierImmutable
				}
				return err
			}
		}
		// Governance: the tenant must be entitled to migrate to the requested
		// target strategy (same gate as barrier-init), so enable-encryption
		// cannot side-step AllowedBarrierModes.
		if m.tenantSvc != nil {
			if err := m.tenantSvc.CheckBarrierMode(ctx, scope.TenantID, opts.TargetStrategy); err != nil {
				return err
			}
		}
		// Route by isolation mode: an IsolationRaft tenant's data lives in
		// dedicated raft shards on the shared host, recycled per-shard; every
		// other dedicated-barrier tenant uses a per-tenant volume Host.
		if m.raft != nil && m.isRaftTenant(ctx, scope.TenantID) {
			return m.raft.MigrateTenantEncryption(ctx, scope.TenantID, opts)
		}
		if m.tenants == nil {
			return service.ErrMigrationProviderUnavailable
		}
		return m.tenants.MigrateEncryption(ctx, scope.TenantID, opts)
	case service.ScopeSystem:
		if m.system == nil {
			return service.ErrMigrationProviderUnavailable
		}
		if err := service.CheckSystemMigrationTarget(scope, opts); err != nil {
			return err
		}
		return m.system.EnableEncryption(ctx, scope, opts)
	default:
		return service.ErrMigrationProviderUnavailable
	}
}

// isRaftTenant reports whether the tenant's effective isolation mode is
// IsolationRaft (data in dedicated raft shards). It fails closed to false (the
// volume path) when no isolation resolver is wired or the lookup errors, so a
// missing resolver never misroutes a volume tenant to the raft mechanism.
func (m *scopedEncryptionMigrator) isRaftTenant(ctx context.Context, tenantID string) bool {
	if m.isolation == nil {
		return false
	}
	info, err := m.isolation.GetTenant(ctx, tenantID)
	if err != nil || info == nil {
		return false
	}
	return info.EffectiveIsolationType() == tenant.IsolationRaft
}

// effectiveEncryptionMigrator resolves the migrator the transports receive. When
// a per-tenant Host registry OR a dedicated-raft shard factory is present it
// composes a scoped router so tenant migrations reach the right recycle
// (IsolationVolume host vs IsolationRaft per-shard); otherwise it forwards the
// configured system migrator unchanged (which may itself be nil, leaving the
// enable-encryption endpoints unconfigured).
func (s *Server) effectiveEncryptionMigrator() service.EncryptionMigrator {
	// When a cluster-coordination migrator is wired, transports hit it directly:
	// it writes the replicated migration intent (and seals per-node credentials)
	// and the per-node reconcilers perform the local recycles. It must NOT be
	// wrapped in the scoped router (that router IS the local recycle, which the
	// reconcilers drive, not the transports).
	if s.opts.clusterEncMigrator != nil {
		return s.opts.clusterEncMigrator
	}
	if s.opts.tenantHosts == nil && s.opts.raftBackend == nil {
		return s.opts.encMigrator
	}
	m := &scopedEncryptionMigrator{
		admitTenant: s.opts.tenantMigrationAdmission,
		system:      s.opts.encMigrator,
		tenants:     s.opts.tenantHosts,
		raft:        s.opts.raftBackend,
		barrierReg:  s.opts.barrierRegistry,
	}
	// tenant.TenantServicer satisfies both narrow interfaces; guard the type
	// assertion so a non-standard servicer simply disables the optional gates.
	if s.opts.tenantSvc != nil {
		m.tenantSvc = s.opts.tenantSvc
		m.isolation = s.opts.tenantSvc
	}
	return m
}
