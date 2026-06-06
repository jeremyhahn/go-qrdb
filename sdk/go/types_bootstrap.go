// Copyright (c) 2026 Jeremy Hahn
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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/bootstrap"

// Bootstrap orchestrator re-exports. This is the canonical control-plane /
// data-plane bootstrap surface for SDK consumers. Higher-level projects (xkms,
// quicbaas, platform) drive bootstrap via these, never importing pkg/bootstrap
// directly.
//
// The three entry points model the control-plane / data-plane split:
//
//   - BootstrapSystem          — daemon init: system barrier + first-boot.
//   - ProvisionTenant          — system-admin: minimal control-plane tenant
//     provisioning (no tenant barrier init).
//   - InitializeTenantBarrier  — tenant-admin self-service: init THIS tenant's
//     barrier.
type (
	// BootstrapDependencies carries the seams every bootstrap entry point
	// composes: store adapters (Tenants/Users/Roles), the admin-mTLS CA seams
	// (CA for Path A, Signer for Path B), the CertBinder store seam, the seal
	// barrier registry, and a logger.
	BootstrapDependencies = bootstrap.Dependencies

	// BootstrapSystemConfig configures system bootstrap: the system barrier
	// strategy (none/passphrase/shamir), passphrase, RBAC posture, and the
	// optional admin CSR (selects admin-mTLS Path B).
	BootstrapSystemConfig = bootstrap.SystemConfig

	// BootstrapSystemResult reports the system barrier strategy initialized
	// and the first-boot result.
	BootstrapSystemResult = bootstrap.SystemResult

	// BootstrapTenantProvisionConfig configures minimal control-plane tenant
	// provisioning.
	BootstrapTenantProvisionConfig = bootstrap.TenantProvisionConfig

	// BootstrapTenantProvisionResult reports the provisioned tenant + its
	// tenant-admin identity (and enrolled cert).
	BootstrapTenantProvisionResult = bootstrap.TenantProvisionResult
)

// Bootstrap strategy constants. Operator-facing system/tenant barrier
// strategies accepted by the bootstrap orchestrator. Hardware strategies are
// injected into the barrier registry by xkms; they are never selected here.
const (
	// BootstrapStrategyNone disables barrier encryption (passthrough).
	BootstrapStrategyNone = bootstrap.StrategyNone

	// BootstrapStrategyPassphrase protects the root key with an operator
	// passphrase (Argon2id / PBKDF2-FIPS).
	BootstrapStrategyPassphrase = bootstrap.StrategyPassphrase

	// BootstrapStrategyShamir splits the root key into M-of-N Shamir shares.
	BootstrapStrategyShamir = bootstrap.StrategyShamir
)

// BootstrapSystem initializes the system barrier per the chosen strategy and
// runs first-boot (system tenant + roles + admin + admin-mTLS). Daemon init.
var BootstrapSystem = bootstrap.BootstrapSystem

// ProvisionTenant performs minimal control-plane provisioning of a new tenant
// (tenant record + tenant-admin user + tenant-admin cert enrollment). It does
// NOT initialize a tenant barrier. System-admin action.
var ProvisionTenant = bootstrap.ProvisionTenant

// InitializeTenantBarrier registers and initializes a named tenant's own
// barrier with the chosen strategy and credentials. Tenant-admin self-service;
// authorization is enforced at the API / transport layer.
var InitializeTenantBarrier = bootstrap.InitializeTenantBarrier
