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

// Package spiffe parses and builds the workload SPIFFE IDs carried as URI SANs
// on mTLS client certificates, to the SPIFFE ID standard
// (docs/reference/spiffe-id.md). The canonical string form is:
//
//	spiffe://<trust-domain>/<kind>/<name>
//
// where kind is "user" (human identity) or "svc" (service identity). The
// trust domain of a tenant's workload is the tenant ID, verbatim: each tenant
// is its own trust domain. A certificate that names no tenant -- a node's --
// is under the cluster's trust domain. go-qrdb is the bottom layer and owns
// mTLS authentication, so it carries its own SPIFFE verifier rather than
// importing the issuer-side helper from go-xkms (which depends on go-qrdb).
// The format is kept identical on both sides; this package is the
// verification source of truth, and refuses any URI the standard does not
// allow as a SPIFFE ID (fail-closed).
package spiffe

import (
	"crypto/x509"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// SPIFFE verification errors. Sentinel errors callers match with errors.Is.
var (
	// ErrTrustDomainEmpty indicates an empty trust domain was parsed.
	ErrTrustDomainEmpty = errors.New("spiffe: trust domain must not be empty")

	// ErrTrustDomainInvalid indicates a trust domain that is not a SPIFFE
	// trust domain name: anything but lowercase letters, digits, dots, dashes
	// and underscores, or longer than 255 bytes (SPIFFE ID §2.1, §2.3).
	ErrTrustDomainInvalid = errors.New("spiffe: trust domain is not a valid spiffe trust domain name")

	// ErrURIInvalid indicates a spiffe URI the standard does not allow as a
	// SPIFFE ID: one with userinfo, a query, a fragment or an opaque part, or
	// one longer than 2048 bytes (SPIFFE ID §2, §2.1, §2.3).
	ErrURIInvalid = errors.New("spiffe: uri is not a valid spiffe id")

	// ErrPathInvalid indicates a SPIFFE ID path the standard forbids -- an
	// empty, "." or ".." segment, a trailing "/", a percent-encoded character,
	// or a character outside letters, digits, dots, dashes and underscores
	// (SPIFFE ID §2.2) -- or one that is not the workload form /<kind>/<name>.
	ErrPathInvalid = errors.New("spiffe: path is not a valid workload spiffe id path")

	// ErrKindInvalid indicates an unsupported workload kind was parsed.
	ErrKindInvalid = errors.New("spiffe: kind must be user or svc")

	// ErrNameEmpty indicates an empty workload name was parsed.
	ErrNameEmpty = errors.New("spiffe: name must not be empty")

	// ErrTrustDomainMismatch indicates a SPIFFE ID under a trust domain other
	// than the one the certificate must name: a node's outside the pinned
	// cluster trust domain, or a certificate with no tenant outside it.
	// Fail-closed: the certificate must be rejected.
	ErrTrustDomainMismatch = errors.New("spiffe: trust domain does not match the expected trust domain")

	// ErrNoTenantSPIFFEID indicates no workload SPIFFE ID SAN was found.
	ErrNoTenantSPIFFEID = errors.New("spiffe: no workload spiffe id present")
)

// SPIFFE ID length bounds (SPIFFE ID §2.3).
const (
	maxTrustDomainLen = 255
	maxIDLen          = 2048
)

// AmbiguousTenantError is returned by WorkloadFromCert when a certificate
// carries workload SPIFFE IDs under more than one distinct trust domain. A
// trust domain is a tenant, so such a certificate names no tenant: answering
// with the first SAN would let the issuer's SAN order choose the tenant, so it
// is refused (fail-closed).
type AmbiguousTenantError struct {
	// TrustDomains are the distinct trust domains the SANs name, in SAN order.
	TrustDomains []string
}

// Error returns a human-readable description naming every trust domain.
func (e *AmbiguousTenantError) Error() string {
	return "spiffe: certificate names more than one trust domain: " + strings.Join(e.TrustDomains, ", ")
}

// Workload kinds.
const (
	// KindUser identifies a human user (PIV/CAC/WebAuthn/federated subject).
	KindUser = "user"

	// KindService identifies a non-human service/workload identity.
	KindService = "svc"

	scheme = "spiffe"
)

// ID describes a workload SPIFFE identity, spiffe://<TrustDomain>/<Kind>/<Name>.
// For a tenant's workload TrustDomain is the tenant ID.
type ID struct {
	TrustDomain string
	Kind        string
	Name        string
}

// Validate verifies every field is present and makes a SPIFFE ID: the trust
// domain is a SPIFFE trust domain name, the kind is KindUser or KindService,
// the name is one SPIFFE ID path segment, and the whole ID is within the
// standard's length bound.
func (id ID) Validate() error {
	if err := checkTrustDomain(id.TrustDomain); err != nil {
		return err
	}
	if id.Kind != KindUser && id.Kind != KindService {
		return ErrKindInvalid
	}
	if id.Name == "" {
		return ErrNameEmpty
	}
	if !validSegment(id.Name) {
		return ErrPathInvalid
	}
	if len(id.url().String()) > maxIDLen {
		return ErrURIInvalid
	}
	return nil
}

// String renders the canonical SPIFFE string form, or "" for an ID that does
// not validate.
func (id ID) String() string {
	if id.Validate() != nil {
		return ""
	}
	return id.url().String()
}

// URI builds the URI SAN a certificate authority issues for id. It is the
// builder WorkloadFromCert is the parser of: Validate admits only characters
// a SPIFFE ID carries unescaped, so ParseID reads the URI back as exactly id.
// A refusal is an *IDError that unwraps to the Validate sentinel.
func (id ID) URI() (*url.URL, error) {
	if err := id.Validate(); err != nil {
		return nil, &IDError{ID: id, Err: err}
	}
	return id.url(), nil
}

func (id ID) url() *url.URL {
	return &url.URL{Scheme: scheme, Host: id.TrustDomain, Path: "/" + id.Kind + "/" + id.Name}
}

// ParseID extracts a workload SPIFFE identity from a URI. It returns false
// (not an error) when the URI is not a SPIFFE ID, or is a node identity
// (ParseNodeID's), so callers can skip those SANs. Any other spiffe URI must be
// a SPIFFE ID to the standard and of the workload form /<kind>/<name>;
// otherwise it returns the sentinel naming what is wrong (fail-closed).
func ParseID(u *url.URL) (ID, bool, error) {
	if u == nil || u.Scheme != scheme {
		return ID{}, false, nil
	}
	if strings.HasPrefix(u.Path, "/"+nodePathSegment+"/") {
		return ID{}, false, nil
	}
	if u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" || len(u.String()) > maxIDLen {
		return ID{}, false, ErrURIInvalid
	}
	if err := checkTrustDomain(u.Host); err != nil {
		return ID{}, false, err
	}
	// A percent-encoded path leaves RawPath set: the decoded Path is not
	// what the certificate carries.
	if u.RawPath != "" || !strings.HasPrefix(u.Path, "/") {
		return ID{}, false, ErrPathInvalid
	}
	parts := strings.Split(u.Path[1:], "/")
	for _, seg := range parts {
		if !validSegment(seg) {
			return ID{}, false, ErrPathInvalid
		}
	}
	if len(parts) != 2 {
		return ID{}, false, ErrPathInvalid
	}
	id := ID{TrustDomain: u.Host, Kind: parts[0], Name: parts[1]}
	if id.Kind != KindUser && id.Kind != KindService {
		return ID{}, false, ErrKindInvalid
	}
	return id, true, nil
}

// WorkloadFromCert scans a certificate's URI SANs for its workload SPIFFE
// identity. Every URI SAN is examined; the answer never depends on SAN order.
//   - A malformed SPIFFE SAN returns the parse error (fail-closed).
//   - Workload SANs under more than one distinct trust domain return
//     *AmbiguousTenantError: the certificate names no tenant.
//   - When no workload SPIFFE SAN is present, returns ErrNoTenantSPIFFEID.
//
// It reads names and does not decide which trust domain the certificate must
// be under: that depends on the tenant its Subject names, which is the mTLS
// authenticator's rule (auth.TenantFromClientCert).
func WorkloadFromCert(cert *x509.Certificate) (ID, error) {
	if cert == nil {
		return ID{}, ErrNoTenantSPIFFEID
	}
	var (
		found   ID
		domains []string
	)
	for _, u := range cert.URIs {
		id, ok, err := ParseID(u)
		if err != nil {
			return ID{}, err
		}
		if !ok {
			continue
		}
		if len(domains) == 0 {
			found = id
		}
		if !slices.Contains(domains, id.TrustDomain) {
			domains = append(domains, id.TrustDomain)
		}
	}
	switch len(domains) {
	case 0:
		return ID{}, ErrNoTenantSPIFFEID
	case 1:
		return found, nil
	default:
		return ID{}, &AmbiguousTenantError{TrustDomains: domains}
	}
}

// checkTrustDomain returns nil when td is a SPIFFE trust domain name (SPIFFE
// ID §2.1, §2.3), and ErrTrustDomainEmpty or ErrTrustDomainInvalid otherwise.
func checkTrustDomain(td string) error {
	if td == "" {
		return ErrTrustDomainEmpty
	}
	if len(td) > maxTrustDomainLen {
		return ErrTrustDomainInvalid
	}
	for i := 0; i < len(td); i++ {
		if c := td[i]; !isLowerAlnum(c) && !isPunct(c) {
			return ErrTrustDomainInvalid
		}
	}
	return nil
}

// validSegment reports whether seg is a SPIFFE ID path segment (SPIFFE ID
// §2.2): non-empty, not a relative path modifier, and only letters, digits,
// dots, dashes and underscores.
func validSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	for i := 0; i < len(seg); i++ {
		if c := seg[i]; !isLowerAlnum(c) && !isPunct(c) && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func isLowerAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func isPunct(c byte) bool { return c == '.' || c == '-' || c == '_' }

// ValidateTrustDomain reports whether trustDomain can be pinned and minted
// under: it must be a SPIFFE trust domain name (SPIFFE ID §2.1) -- the same
// check the authenticator applies to a presented certificate and the
// certificate authority applies before it mints a SAN. A trust domain that
// fails it would make the CA refuse every certificate, so configuration
// validates it at load.
//
// A refusal is a *TrustDomainError carrying the rejected value; it unwraps to
// ErrTrustDomainEmpty or ErrTrustDomainInvalid.
func ValidateTrustDomain(trustDomain string) error {
	if err := checkTrustDomain(trustDomain); err != nil {
		return &TrustDomainError{Value: trustDomain, Err: err}
	}
	return nil
}

// TrustDomainError reports a trust domain ValidateTrustDomain refused, and
// carries the rejected value so the operator sees exactly what was
// configured -- including leading or trailing whitespace, which is quoted.
type TrustDomainError struct {
	// Value is the trust domain as configured.
	Value string

	// Err is ErrTrustDomainEmpty or ErrTrustDomainInvalid.
	Err error
}

// Error names the rejected value and why.
func (e *TrustDomainError) Error() string {
	return "spiffe: trust domain " + strconv.Quote(e.Value) + " refused: " + e.Err.Error()
}

// Unwrap returns the sentinel classifying the refusal.
func (e *TrustDomainError) Unwrap() error {
	return e.Err
}

// IDError reports an ID that ID.URI refused to build.
type IDError struct {
	// ID is the identity as given.
	ID ID

	// Err is the Validate sentinel, or the issuer's own refusal.
	Err error
}

// Error names the refused identity's fields and why.
func (e *IDError) Error() string {
	return "spiffe: id (trust domain " + strconv.Quote(e.ID.TrustDomain) +
		", kind " + strconv.Quote(e.ID.Kind) +
		", name " + strconv.Quote(e.ID.Name) + ") refused: " + e.Err.Error()
}

// Unwrap returns the sentinel classifying the refusal.
func (e *IDError) Unwrap() error {
	return e.Err
}
