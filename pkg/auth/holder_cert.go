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

package auth

import "crypto/x509"

// Holder certificate profiles. The ceremony CA (pkg/ceremony) stamps one of
// these as the second Subject OU of every Security Officer and share-holder
// certificate it signs, after the tenant: OU = [<tenant>, <profile>].
//
// A holder certificate proves who holds a share; it is not a login. It reaches
// only the identity-gated ceremony surface, where the handler binds the caller
// by certificate thumbprint, and never resolves to a user account -- a holder
// registered as "admin" does not become the tenant's admin user.
const (
	// ProfileSecurityOfficer marks a Security Officer certificate.
	ProfileSecurityOfficer = "security-officer"

	// ProfileCustodian marks an officer or custodian share-holder certificate.
	ProfileCustodian = "custodian"
)

// holderProfiles is the set HolderProfile recognises.
var holderProfiles = map[string]struct{}{
	ProfileSecurityOfficer: {},
	ProfileCustodian:       {},
}

// HolderProfileMetadataKey is the Identity.Metadata key the mTLS
// authenticator sets, to the profile, when the client certificate is a holder
// certificate. See IsHolderIdentity.
const HolderProfileMetadataKey = "holder_profile"

// HolderProfile reports the holder profile cert carries in any Subject OU, and
// whether it carries one. Every position is checked, not only the second: a
// certificate whose FIRST OU is a profile -- the layout before the ceremony CA
// set the tenant itself -- is a holder certificate too.
func HolderProfile(cert *x509.Certificate) (string, bool) {
	if cert == nil {
		return "", false
	}
	for _, ou := range cert.Subject.OrganizationalUnit {
		if _, ok := holderProfiles[ou]; ok {
			return ou, true
		}
	}
	return "", false
}

// IsHolderIdentity reports whether id authenticated with a holder
// certificate. Such an identity is never enriched from the user store.
func IsHolderIdentity(id *Identity) bool {
	return id != nil && id.Metadata[HolderProfileMetadataKey] != ""
}
