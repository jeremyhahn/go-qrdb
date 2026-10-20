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

// SpiffeID describes a workload SPIFFE identity extracted from a certificate.
// A tenant is a SPIFFE trust domain, so a tenant's principal has the
// canonical URI form:
//
//	spiffe://<tenantID>/<kind>/<name>
//
// where Kind is SpiffeKindUser or SpiffeKindService. SpiffeID.URI builds the
// URI SAN a certificate authority issues for a tenant's user or service; it
// returns the URI only when every field makes a SPIFFE ID that
// WorkloadFromCert reads back as exactly the same identity, and refuses with
// *SpiffeIDError otherwise. (Aliases pkg/auth/spiffe.ID.)
type SpiffeID = spiffe.ID

// SpiffeKindUser identifies a human user (PIV/CAC/WebAuthn/federated subject).
const SpiffeKindUser = spiffe.KindUser

// SpiffeKindService identifies a non-human service/workload identity.
const SpiffeKindService = spiffe.KindService

// ---------------------------------------------------------------------------
// SPIFFE sentinel errors
// ---------------------------------------------------------------------------

// ErrSpiffeNoTenantID is returned when no workload SPIFFE SAN is found on the
// certificate. (Aliases pkg/auth/spiffe.ErrNoTenantSPIFFEID.)
var ErrSpiffeNoTenantID = spiffe.ErrNoTenantSPIFFEID

// ErrSpiffeTrustDomainMismatch is returned when a certificate naming no tenant
// is not under the cluster trust domain. Fail-closed: the certificate must be
// rejected. (Aliases pkg/auth/spiffe.ErrTrustDomainMismatch.)
var ErrSpiffeTrustDomainMismatch = spiffe.ErrTrustDomainMismatch

// SpiffeAmbiguousTenantError is returned when a certificate's workload SPIFFE
// SANs are under more than one trust domain; such a certificate names no
// tenant. (Aliases pkg/auth/spiffe.AmbiguousTenantError.)
type SpiffeAmbiguousTenantError = spiffe.AmbiguousTenantError

// SpiffeIDError is SpiffeID.URI's refusal. It unwraps to one of the
// ErrSpiffe* field sentinels below. (Aliases pkg/auth/spiffe.IDError.)
type SpiffeIDError = spiffe.IDError

// SPIFFE ID sentinels, returned by SpiffeID.Validate and WorkloadFromCert and
// reachable through *SpiffeIDError. (Alias the pkg/auth/spiffe sentinels of
// the same meaning.)
var (
	ErrSpiffeTrustDomainEmpty   = spiffe.ErrTrustDomainEmpty
	ErrSpiffeTrustDomainInvalid = spiffe.ErrTrustDomainInvalid
	ErrSpiffeURIInvalid         = spiffe.ErrURIInvalid
	ErrSpiffePathInvalid        = spiffe.ErrPathInvalid
	ErrSpiffeKindInvalid        = spiffe.ErrKindInvalid
	ErrSpiffeNameEmpty          = spiffe.ErrNameEmpty
)

// ---------------------------------------------------------------------------
// SPIFFE functions
// ---------------------------------------------------------------------------

// WorkloadFromCert scans every URI SAN of a certificate for its workload
// SPIFFE identity. A spiffe SAN that is not a SPIFFE ID fails closed with its
// sentinel; workload SANs under more than one trust domain return
// *SpiffeAmbiguousTenantError; no workload SAN returns ErrSpiffeNoTenantID.
// Most callers want TenantFromClientCert, the full rule, which also requires
// the first Subject OU to name the same tenant as the trust domain.
// (Aliases pkg/auth/spiffe.WorkloadFromCert.)
var WorkloadFromCert = spiffe.WorkloadFromCert
