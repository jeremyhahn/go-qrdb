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

import "github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"

// ---------------------------------------------------------------------------
// SPIFFE identity types
//
// go-qrdb owns mTLS authentication and carries its own SPIFFE verifier. These
// aliases re-export the SPIFFE identity primitives so consumers can use the
// sdk/go module exclusively without reaching into go-qrdb/pkg/auth/spiffe.
// ---------------------------------------------------------------------------

// SpiffeID describes a tenant-scoped SPIFFE identity extracted from a
// certificate. The canonical URI form is:
//
//	spiffe://<trust-domain>/tenant/<tenantID>/<kind>/<name>
//
// where Kind is SpiffeKindUser or SpiffeKindService. (Aliases
// pkg/auth/spiffe.ID.)
type SpiffeID = spiffe.ID

// SpiffeKindUser identifies a human user (PIV/CAC/WebAuthn/federated subject).
const SpiffeKindUser = spiffe.KindUser

// SpiffeKindService identifies a non-human service/workload identity.
const SpiffeKindService = spiffe.KindService

// ---------------------------------------------------------------------------
// SPIFFE sentinel errors
// ---------------------------------------------------------------------------

// ErrSpiffeNoTenantID is returned when no tenant-scoped SPIFFE SAN is found
// on the certificate. (Aliases pkg/auth/spiffe.ErrNoTenantSPIFFEID.)
var ErrSpiffeNoTenantID = spiffe.ErrNoTenantSPIFFEID

// ErrSpiffeTrustDomainMismatch is returned when the certificate's SPIFFE trust
// domain does not match the operator-pinned trust domain. Fail-closed: the
// certificate must be rejected. (Aliases
// pkg/auth/spiffe.ErrTrustDomainMismatch.)
var ErrSpiffeTrustDomainMismatch = spiffe.ErrTrustDomainMismatch

// ---------------------------------------------------------------------------
// SPIFFE functions
// ---------------------------------------------------------------------------

// TenantFromCert scans a certificate's URI SANs for a tenant-scoped SPIFFE
// identity, enforcing the pinned trust domain (fail-closed). When
// pinnedTrustDomain is non-empty, any SPIFFE tenant SAN whose trust domain
// differs returns ErrSpiffeTrustDomainMismatch. When no tenant SPIFFE SAN is
// present, returns ErrSpiffeNoTenantID. (Aliases
// pkg/auth/spiffe.TenantFromCert.)
var TenantFromCert = spiffe.TenantFromCert
