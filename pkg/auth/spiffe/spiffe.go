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

// Package spiffe verifies tenant-scoped SPIFFE identities carried as URI SANs
// on mTLS client certificates. The canonical string form is:
//
//	spiffe://<trust-domain>/tenant/<tenantID>/<kind>/<name>
//
// where kind is "user" (human identity) or "svc" (service identity). go-qrdb
// is the bottom layer and owns mTLS authentication, so it carries its own
// SPIFFE verifier rather than importing the issuer-side helper from go-xkms
// (which depends on go-qrdb). The format is kept identical on both sides; this
// package is the verification source of truth and pins the operator-configured
// trust domain (fail-closed on mismatch).
package spiffe

import (
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
)

// SPIFFE verification errors. Sentinel errors callers match with errors.Is.
var (
	// ErrTrustDomainEmpty indicates an empty trust domain was parsed.
	ErrTrustDomainEmpty = errors.New("spiffe: trust domain must not be empty")

	// ErrTenantEmpty indicates an empty tenant ID was parsed.
	ErrTenantEmpty = errors.New("spiffe: tenant id must not be empty")

	// ErrKindInvalid indicates an unsupported workload kind was parsed.
	ErrKindInvalid = errors.New("spiffe: kind must be user or svc")

	// ErrNameEmpty indicates an empty workload name was parsed.
	ErrNameEmpty = errors.New("spiffe: name must not be empty")

	// ErrTrustDomainMismatch indicates the certificate's SPIFFE trust domain
	// does not match the operator-pinned trust domain. Fail-closed: the
	// certificate must be rejected.
	ErrTrustDomainMismatch = errors.New("spiffe: trust domain does not match pinned trust domain")

	// ErrNoTenantSPIFFEID indicates no tenant-scoped SPIFFE SAN was found.
	ErrNoTenantSPIFFEID = errors.New("spiffe: no tenant spiffe identity present")
)

// Workload kinds.
const (
	// KindUser identifies a human user (PIV/CAC/WebAuthn/federated subject).
	KindUser = "user"

	// KindService identifies a non-human service/workload identity.
	KindService = "svc"

	scheme = "spiffe"
)

// ID describes a tenant-scoped SPIFFE identity extracted from a certificate.
type ID struct {
	TrustDomain string
	TenantID    string
	Kind        string
	Name        string
}

// Validate verifies all fields are present and well-formed.
func (id ID) Validate() error {
	if strings.TrimSpace(id.TrustDomain) == "" {
		return ErrTrustDomainEmpty
	}
	if strings.TrimSpace(id.TenantID) == "" {
		return ErrTenantEmpty
	}
	if id.Kind != KindUser && id.Kind != KindService {
		return ErrKindInvalid
	}
	if strings.TrimSpace(id.Name) == "" {
		return ErrNameEmpty
	}
	return nil
}

// String renders the canonical SPIFFE string form.
func (id ID) String() string {
	if id.Validate() != nil {
		return ""
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   id.TrustDomain,
		Path:   "/tenant/" + id.TenantID + "/" + id.Kind + "/" + id.Name,
	}
	return u.String()
}

// ParseID extracts a tenant SPIFFE identity from a URI. It returns false (not
// an error) when the URI is not a SPIFFE tenant path so callers can skip
// non-SPIFFE SANs. A malformed tenant SPIFFE path returns an error.
func ParseID(u *url.URL) (ID, bool, error) {
	if u == nil || u.Scheme != scheme {
		return ID{}, false, nil
	}
	if u.Host == "" {
		return ID{}, false, ErrTrustDomainEmpty
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "tenant" {
		return ID{}, false, nil
	}
	id := ID{
		TrustDomain: u.Host,
		TenantID:    parts[1],
		Kind:        parts[2],
		Name:        parts[3],
	}
	if err := id.Validate(); err != nil {
		return ID{}, false, err
	}
	return id, true, nil
}

// TenantFromCert scans a certificate's URI SANs for a tenant-scoped SPIFFE
// identity, enforcing the pinned trust domain (fail-closed).
//
//   - When pinnedTrustDomain is non-empty, any SPIFFE tenant SAN whose trust
//     domain differs returns ErrTrustDomainMismatch (reject foreign certs).
//   - When no tenant SPIFFE SAN is present, returns ErrNoTenantSPIFFEID so the
//     caller can fall back to home-realm discovery or fail closed.
//   - A malformed tenant SPIFFE SAN returns the parse error (fail-closed).
func TenantFromCert(cert *x509.Certificate, pinnedTrustDomain string) (ID, error) {
	if cert == nil {
		return ID{}, ErrNoTenantSPIFFEID
	}
	for _, u := range cert.URIs {
		id, ok, err := ParseID(u)
		if err != nil {
			return ID{}, err
		}
		if !ok {
			continue
		}
		if pinnedTrustDomain != "" && id.TrustDomain != pinnedTrustDomain {
			return ID{}, ErrTrustDomainMismatch
		}
		return id, nil
	}
	return ID{}, ErrNoTenantSPIFFEID
}
