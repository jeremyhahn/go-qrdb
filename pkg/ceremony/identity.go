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

package ceremony

import (
	"crypto/x509/pkix"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// maxHolderNameLen bounds a username that becomes a certificate CN and a
// SPIFFE path segment.
const maxHolderNameLen = 64

// validPrincipalName reports whether name can be a certificate CN and one
// SPIFFE path segment: 1..64 characters from [A-Za-z0-9._@-].
func validPrincipalName(name string) bool {
	if name == "" || len(name) > maxHolderNameLen || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '@', r == '-':
		default:
			return false
		}
	}
	return true
}

// holderProfiles maps a holder role onto its certificate profile.
var holderProfiles = map[seal.HolderRole]string{
	seal.RoleSecurityOfficer: "security-officer",
	seal.RoleCustodian:       "custodian",
}

// assignedIdentity is the identity the ceremony CA writes into a certificate
// it issues. The CSR never contributes to it.
type assignedIdentity struct {
	subject pkix.Name
	uris    []*url.URL
}

// principalIdentity builds the CA-assigned identity for name in tenantID
// under trustDomain: CN = name, OU = [tenant, profile], and one SPIFFE URI
// SAN spiffe://<trust-domain>/tenant/<tenant>/<kind>/<name>.
func principalIdentity(trustDomain, tenantID, kind, name, profile string) (*assignedIdentity, error) {
	if trustDomain == "" {
		return nil, ErrTrustDomainNotConfigured
	}
	if !validPrincipalName(name) {
		return nil, &InvalidHolderNameError{Name: name}
	}
	id := spiffe.ID{TrustDomain: trustDomain, TenantID: tenantID, Kind: kind, Name: name}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(id.String())
	if err != nil {
		return nil, err
	}
	return &assignedIdentity{
		subject: pkix.Name{CommonName: name, OrganizationalUnit: []string{tenantID, profile}},
		uris:    []*url.URL{u},
	}, nil
}

// holderIdentity is principalIdentity in the SPIFFE holder namespace, which
// pkg/auth refuses as a login identity.
func holderIdentity(trustDomain, tenantID, name string, role seal.HolderRole) (*assignedIdentity, error) {
	profile, ok := holderProfiles[role]
	if !ok {
		return nil, ErrInvalidHolderRole
	}
	return principalIdentity(trustDomain, tenantID, spiffe.KindHolder, name, profile)
}
