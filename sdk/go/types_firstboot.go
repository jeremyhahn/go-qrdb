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

import "github.com/jeremyhahn/go-qrdb/pkg/firstboot"

// First-boot bootstrap re-exports. This is the canonical admin/roles/tenant +
// admin-mTLS bootstrap surface for SDK consumers. Higher-level projects
// (xkms, quicbaas, platform) drive first-boot via these, never importing
// pkg/firstboot directly.
type (
	// FirstBootDependencies is the seam set first-boot needs: store adapters
	// (TenantCreator/UserCreator/RoleCreator), the admin-mTLS CA seams
	// (CertProvider for Path A, CertSigner for Path B), the CertBinder store
	// seam, and a logger.
	FirstBootDependencies = firstboot.Dependencies
	// FirstBootResult reports what first-boot created (incl. AdminCertThumbprint).
	FirstBootResult = firstboot.Result

	// CertProvider is admin-mTLS Path A: generate-and-distribute (xkms TrustedCA).
	CertProvider = firstboot.CertProvider
	// CertSigner is admin-mTLS Path B: CSR-sign (xkms TrustedCA).
	CertSigner = firstboot.CertSigner
	// CertBinder is the store seam that pins the admin cert's SPKI thumbprint
	// to the admin user; the consumer's user store implements it.
	CertBinder = firstboot.CertBinder

	// TenantCreator/UserCreator/RoleCreator are the store-adapter seams.
	FirstBootTenantCreator = firstboot.TenantCreator
	FirstBootUserCreator   = firstboot.UserCreator
	FirstBootRoleCreator   = firstboot.RoleCreator
)

// FirstBootInitialize runs the canonical first-boot path (idempotent): system
// tenant + built-in roles + admin user + admin-mTLS (Path A or B) + SPKI binding.
var FirstBootInitialize = firstboot.Initialize
