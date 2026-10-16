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

import "github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"

// ---------------------------------------------------------------------------
// Tenant-issued delegation types
//
// A delegation is a short-lived client certificate a tenant's own login CA
// issues to an embedder's service principal, carrying a critical extension
// that names the principal and the permissions it may exercise inside the
// tenant. These aliases let an embedder read and check that extension using
// the sdk/go module alone, without reaching into go-qrdb/pkg/auth/delegation.
// ---------------------------------------------------------------------------

// DelegationGrant is the content of a delegation: Principal may act inside
// Tenant with at most the permissions in Scope. (Aliases
// pkg/auth/delegation.Grant.)
type DelegationGrant = delegation.Grant

// DelegationGrantError reports the first malformed field of a
// DelegationGrant. (Aliases pkg/auth/delegation.GrantError.)
type DelegationGrantError = delegation.GrantError

// DelegationExtensionError reports a delegation extension that is not
// critical or whose value does not parse. (Aliases
// pkg/auth/delegation.ExtensionError.)
type DelegationExtensionError = delegation.ExtensionError

// DelegationLifetimeError reports a delegation certificate whose validity
// window exceeds DelegationMaxLifetime. (Aliases
// pkg/auth/delegation.LifetimeError.)
type DelegationLifetimeError = delegation.LifetimeError

// DelegationOID identifies the delegation extension (tp-delegation,
// 1.3.6.1.4.1.29377.101.24). (Aliases pkg/auth/delegation.OID.)
var DelegationOID = delegation.OID

// DelegationDefaultLifetime is a delegation's validity when its issuer asks
// for none. (Aliases pkg/auth/delegation.DefaultLifetime.)
const DelegationDefaultLifetime = delegation.DefaultLifetime

// DelegationMaxLifetime bounds a delegation's validity at issuance and at
// verification. (Aliases pkg/auth/delegation.MaxLifetime.)
const DelegationMaxLifetime = delegation.MaxLifetime

// DelegationMaxScope bounds the number of permissions one delegation may
// name. (Aliases pkg/auth/delegation.MaxScope.)
const DelegationMaxScope = delegation.MaxScope

// ---------------------------------------------------------------------------
// Tenant-issued delegation functions
// ---------------------------------------------------------------------------

// DelegationFromCertificate returns the grant a certificate carries. found
// reports whether the delegation extension is present at all; a present
// extension that is not critical or does not parse returns
// *DelegationExtensionError. (Aliases pkg/auth/delegation.FromCertificate.)
var DelegationFromCertificate = delegation.FromCertificate

// DelegationCheckLifetime refuses a delegation certificate whose validity
// window exceeds DelegationMaxLifetime with *DelegationLifetimeError.
// (Aliases pkg/auth/delegation.CheckLifetime.)
var DelegationCheckLifetime = delegation.CheckLifetime
