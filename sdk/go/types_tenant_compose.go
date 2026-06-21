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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Tenant isolation modes
//
// TenantIsolationBarrier is re-exported in types_tenant.go; the remaining
// modes are aliased here so a composing service (e.g. go-xkms) can name every
// isolation mode through the SDK boundary without importing pkg/tenant.
// ---------------------------------------------------------------------------

// TenantIsolationPrefix isolates tenant data with logical key prefixing against
// the shared system store (no per-tenant barrier). (Aliases pkg/tenant.IsolationPrefix.)
const TenantIsolationPrefix = tenant.IsolationPrefix

// TenantIsolationVolume gives a tenant a dedicated filesystem DataRoot hosting a
// per-tenant local Raft cluster encrypted under the tenant's own DEK. Requires
// the volume pool and seal subsystem. (Aliases pkg/tenant.IsolationVolume.)
const TenantIsolationVolume = tenant.IsolationVolume

// TenantIsolationRaft gives a tenant a dedicated Raft GROUP per database on the
// shared system Host, with state-machine data encrypted under the tenant's own
// DEK. Requires the volume pool and seal subsystem. (Aliases pkg/tenant.IsolationRaft.)
const TenantIsolationRaft = tenant.IsolationRaft

// ---------------------------------------------------------------------------
// CreateTenant options
// ---------------------------------------------------------------------------

// WithBarrierAutoUnseal selects a dedicated-isolation tenant's barrier
// key-management mode. Pass true to opt into system-managed auto-unseal
// (entitlement-gated); the default (false) is the secure self-managed mode.
// (Aliases pkg/tenant.WithBarrierAutoUnseal.)
var WithBarrierAutoUnseal = tenant.WithBarrierAutoUnseal

// ---------------------------------------------------------------------------
// TenantService construction options
//
// NewServiceTenantService (types_service.go) re-exports the TenantService
// constructor; these aliases let a composing service supply its options
// (notably a hardware-backed StrategyRegistry) through the SDK boundary. The
// tenant-variant WithStrategyRegistry is named WithTenantStrategyRegistry to
// avoid colliding with the embedded-server option WithStrategyRegistry
// (types_server.go).
// ---------------------------------------------------------------------------

// TenantServiceOption configures optional TenantService dependencies. (Aliases
// pkg/tenant.TenantServiceOption.)
type TenantServiceOption = tenant.TenantServiceOption

// WithTenantStrategyRegistry injects a StrategyRegistry so per-tenant barrier
// initialization can resolve hardware/cloud seal strategies by name. (Aliases
// pkg/tenant.WithStrategyRegistry.)
var WithTenantStrategyRegistry = tenant.WithStrategyRegistry

// WithTenantDistributor injects a CeremonyDistributor wired onto each newly
// registered Shamir tenant barrier before initialization. (Aliases
// pkg/tenant.WithTenantDistributor.)
var WithTenantDistributor = tenant.WithTenantDistributor

// WithTenantCeremonyCA injects the CeremonyCA used when lazily constructing
// per-tenant ceremony services. (Aliases pkg/tenant.WithTenantCeremonyCA.)
var WithTenantCeremonyCA = tenant.WithTenantCeremonyCA

// WithTenantDefaultQuotas injects the operator-configured default quotas applied
// to tenants whose own Quotas are nil. (Aliases pkg/tenant.WithDefaultQuotas.)
var WithTenantDefaultQuotas = tenant.WithDefaultQuotas

// WithTenantDefaultEntitlements injects the operator-configured default
// entitlements applied to tenants whose own Entitlements are nil. (Aliases
// pkg/tenant.WithDefaultEntitlements.)
var WithTenantDefaultEntitlements = tenant.WithDefaultEntitlements

// ---------------------------------------------------------------------------
// Entitlements
// ---------------------------------------------------------------------------

// Entitlements describes which isolation tiers and dedicated-storage features a
// tenant's plan permits. (Aliases pkg/quota.Entitlements.)
type Entitlements = quota.Entitlements

// DefaultEntitlements returns the conservative built-in default entitlements
// (self-managed barriers only; no dedicated raft/volume/WAL). (Aliases
// pkg/quota.DefaultEntitlements.)
var DefaultEntitlements = quota.DefaultEntitlements

// EntitlementError is returned when a tenant requests a feature its plan does
// not permit (e.g. a disallowed barrier mode, dedicated WAL, or isolation
// tier). A composing service should map it to a 403 at its API boundary.
// (Aliases pkg/quota.EntitlementError.)
type EntitlementError = quota.EntitlementError

// ---------------------------------------------------------------------------
// Strategy registry interface + system-managed auto-unseal strategy
// ---------------------------------------------------------------------------

// StrategyRegistry resolves seal strategies by name. A composing service
// registers its hardware/cloud strategy factories here and injects the registry
// via WithTenantStrategyRegistry / WithStrategyRegistry. (Aliases
// pkg/seal.StrategyRegistry.)
type StrategyRegistry = seal.StrategyRegistry

// SystemBarrierStrategy seals a tenant root key under the already-unsealed
// system barrier, enabling credential-free system-managed auto-unseal. (Aliases
// pkg/seal.SystemBarrierStrategy.)
type SystemBarrierStrategy = seal.SystemBarrierStrategy

// StrategySystemBarrier identifies the system-managed auto-unseal strategy.
// (Aliases pkg/seal.StrategySystemBarrier.)
const StrategySystemBarrier = seal.StrategySystemBarrier

// NewSystemBarrierStrategy constructs a SystemBarrierStrategy wrapping the given
// system barrier. (Aliases pkg/seal.NewSystemBarrierStrategy.)
var NewSystemBarrierStrategy = seal.NewSystemBarrierStrategy
