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

import "github.com/jeremyhahn/go-qrdb/pkg/permission"

// ---------------------------------------------------------------------------
// ACME administration and CA hierarchy permissions
//
// These re-export pkg/permission's ACME-administration and CA-hierarchy
// constants so a consumer that hosts an RFC 8555 ACME admin surface (go-xkms's
// TrustedCA) can name the exact grant its route decorators request without
// importing qrdb's pkg/* tree or re-declaring the strings. The definitions,
// their registry entries, and the built-in role grants that confer them all
// live in qrdb — this file is the SDK handle, never a second source of truth.
// ---------------------------------------------------------------------------

const (
	// PermissionACMEAccountsRead grants read access to a single ACME
	// account's registration state.
	PermissionACMEAccountsRead = permission.ACMEAccountsRead
	// PermissionACMEAccountsList grants access to enumerate ACME accounts.
	PermissionACMEAccountsList = permission.ACMEAccountsList
	// PermissionACMEAccountsDisable grants access to deactivate an ACME
	// account.
	PermissionACMEAccountsDisable = permission.ACMEAccountsDisable

	// PermissionACMEOrdersRead grants read access to a single ACME order.
	PermissionACMEOrdersRead = permission.ACMEOrdersRead
	// PermissionACMEOrdersList grants access to enumerate ACME orders.
	PermissionACMEOrdersList = permission.ACMEOrdersList

	// PermissionACMEEABRead grants read access to an External Account
	// Binding's metadata.
	PermissionACMEEABRead = permission.ACMEEABRead
	// PermissionACMEEABList grants access to enumerate External Account
	// Bindings.
	PermissionACMEEABList = permission.ACMEEABList
	// PermissionACMEEABCreate grants access to mint an External Account
	// Binding.
	PermissionACMEEABCreate = permission.ACMEEABCreate
	// PermissionACMEEABDelete grants access to revoke an External Account
	// Binding.
	PermissionACMEEABDelete = permission.ACMEEABDelete

	// PermissionCACreate grants access to create a root or intermediate
	// certificate authority.
	PermissionCACreate = permission.CACreate
	// PermissionCARead grants read access to a certificate authority.
	PermissionCARead = permission.CARead
	// PermissionCAList grants access to enumerate certificate authorities.
	PermissionCAList = permission.CAList
	// PermissionCAUpdate grants access to change a certificate authority's
	// status or select the default issuing CA.
	PermissionCAUpdate = permission.CAUpdate
)
