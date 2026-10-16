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

// Package delegation is the tenant-issued service delegation: an X.509
// certificate extension with which a tenant's own login CA (Key B) names an
// embedder's service principal and the permissions that principal may
// exercise inside the tenant.
//
// A delegation is an ordinary short-lived client certificate carrying one
// extra, CRITICAL extension -- in the spirit of RFC 3820 proxy certificates
// and SPIFFE SVIDs, but issued by the tenant's CA rather than by the
// delegator's own key. Critical, so that a verifier which does not understand
// it refuses the certificate (RFC 5280 §4.2) instead of reading it as a user
// login. Nothing here signs or verifies anything: issuance is the tenant CA's
// crypto/x509 signature and verification is crypto/x509's chain building;
// this package only encodes, parses and validates the extension's content.
//
// The extension value is the DER encoding of
//
//	DelegationGrant ::= SEQUENCE {
//	    version    INTEGER (1),
//	    tenant     UTF8String,
//	    principal  UTF8String,
//	    scope      SEQUENCE SIZE (1..64) OF UTF8String }
package delegation

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// OID identifies the delegation extension, named tp-delegation.
//
// It is 1.3.6.1.4.1.29377.101.24: the arc 29377.101 is the company's IANA
// Private Enterprise Number (29377) certificate-extension arc, shared with
// go-trusted-platform and go-xkms. A UUID-derived arc (2.25, ITU-T X.667)
// needs no registration but cannot be used: Go's encoding/asn1 refuses an arc
// component wider than 31 bits, so crypto/x509 would refuse to parse every
// certificate carrying one.
var OID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 29377, 101, 24}

// Version is the only DelegationGrant version this package encodes or
// accepts.
const Version = 1

// DefaultLifetime is a delegation's validity when its issuer asks for none:
// short, like a SPIFFE X.509-SVID, so revocation is the exception rather
// than the mechanism.
const DefaultLifetime = time.Hour

// MaxLifetime bounds a delegation's validity. Issuance caps a request at it
// and verification refuses a certificate whose NotBefore..NotAfter exceeds it,
// so a CA that ignored the cap still cannot mint a long-lived delegation.
const MaxLifetime = 24 * time.Hour

// MaxScope bounds the number of permissions one delegation may name.
const MaxScope = 64

// MaxPrincipalLen bounds a service principal's name, matching the node's own
// bound on the principal it accepts delegations for.
const MaxPrincipalLen = 64

// maxPermissionLen bounds one scope entry.
const maxPermissionLen = 128

// maxTenantLen bounds the tenant field.
const maxTenantLen = 256

// Grant is the content of a delegation: Principal may act inside Tenant with
// at most the permissions in Scope. The permissions a delegated request
// actually holds are Scope intersected with the principal's own grants on
// the verifying node, so a Grant can narrow the principal's authority but
// never widen it.
type Grant struct {
	// Tenant is the tenant the delegation acts inside. It must equal the
	// tenant the certificate names and whose CA signed it.
	Tenant string `json:"tenant"`

	// Principal is the service principal the delegation is for.
	Principal string `json:"principal"`

	// Scope names the permissions ("resource:action") the delegation
	// confers, at most MaxScope of them, without duplicates.
	Scope []string `json:"scope"`
}

// Validate reports whether g is a well-formed grant, returning *GrantError
// naming the first field that is not.
func (g Grant) Validate() error {
	if g.Tenant == "" || len(g.Tenant) > maxTenantLen || !isVisibleASCII(g.Tenant) {
		return &GrantError{Field: "tenant", Reason: "must be 1.." + strconv.Itoa(maxTenantLen) + " printable characters"}
	}
	if err := ValidatePrincipal(g.Principal); err != nil {
		return err
	}
	if len(g.Scope) == 0 || len(g.Scope) > MaxScope {
		return &GrantError{Field: "scope", Reason: "must name 1.." + strconv.Itoa(MaxScope) + " permissions"}
	}
	for i, p := range g.Scope {
		if !validPermission(p) {
			return &GrantError{Field: "scope", Reason: strconv.Quote(p) + " is not a resource:action permission"}
		}
		if slices.Contains(g.Scope[:i], p) {
			return &GrantError{Field: "scope", Reason: strconv.Quote(p) + " is named twice"}
		}
	}
	return nil
}

// ValidatePrincipal reports whether name is a well-formed service principal
// name: lowercase letters, digits, '.', '_' and '-', starting with a letter
// or digit, at most MaxPrincipalLen bytes. It returns *GrantError otherwise.
// Whether the name is also free of the built-in role names is the caller's
// check: those live above this package.
func ValidatePrincipal(name string) error {
	if name == "" || len(name) > MaxPrincipalLen {
		return &GrantError{Field: "principal", Reason: "must be 1.." + strconv.Itoa(MaxPrincipalLen) + " bytes"}
	}
	for i := 0; i < len(name); i++ {
		if !principalByte(name[i], i == 0) {
			return &GrantError{Field: "principal", Reason: "must be [a-z0-9][a-z0-9._-]*"}
		}
	}
	return nil
}

// principalByte reports whether c may appear in a principal name; the first
// byte must be a letter or digit.
func principalByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '.' || c == '_' || c == '-':
		return !first
	}
	return false
}

// validPermission reports whether p has the "resource:action" shape: two
// non-empty printable halves around a single colon. Whether p is a
// permission the node knows is the issuer's check, and whether the principal
// holds it is the verifier's.
func validPermission(p string) bool {
	if len(p) == 0 || len(p) > maxPermissionLen || !isVisibleASCII(p) {
		return false
	}
	i := -1
	for j := 0; j < len(p); j++ {
		if p[j] == ':' {
			if i >= 0 {
				return false
			}
			i = j
		}
	}
	return i > 0 && i < len(p)-1
}

// isVisibleASCII reports whether s is non-empty printable ASCII with no space.
func isVisibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// Extension returns g as the CRITICAL certificate extension a delegation
// carries. It validates g first.
func (g Grant) Extension() (pkix.Extension, error) {
	if err := g.Validate(); err != nil {
		return pkix.Extension{}, err
	}
	var b cryptobyte.Builder
	b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
		b.AddASN1Int64(Version)
		addUTF8(b, g.Tenant)
		addUTF8(b, g.Principal)
		b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
			for _, p := range g.Scope {
				addUTF8(b, p)
			}
		})
	})
	der, err := b.Bytes()
	if err != nil {
		return pkix.Extension{}, &ExtensionError{Reason: "encode", Err: err}
	}
	return pkix.Extension{Id: OID, Critical: true, Value: der}, nil
}

// addUTF8 appends s as a UTF8String.
func addUTF8(b *cryptobyte.Builder, s string) {
	b.AddASN1(cbasn1.UTF8String, func(b *cryptobyte.Builder) { b.AddBytes([]byte(s)) })
}

// FromCertificate returns the delegation c carries. found is false, with a
// nil error, when c carries none. A delegation extension that is not marked
// critical, is malformed, or carries an invalid grant is an error: it is
// never read as "no delegation", because the certificate would then be
// judged as something it was not issued to be.
func FromCertificate(c *x509.Certificate) (g Grant, found bool, err error) {
	if c == nil {
		return Grant{}, false, nil
	}
	for _, ext := range c.Extensions {
		if !ext.Id.Equal(OID) {
			continue
		}
		if !ext.Critical {
			return Grant{}, true, &ExtensionError{Reason: "the extension is not marked critical"}
		}
		g, err := parse(ext.Value)
		if err != nil {
			return Grant{}, true, err
		}
		return g, true, nil
	}
	return Grant{}, false, nil
}

// parse decodes a DelegationGrant, refusing trailing bytes, a version other
// than Version, non-UTF8String strings and an invalid grant.
func parse(der []byte) (Grant, error) {
	in := cryptobyte.String(der)
	var seq, scope cryptobyte.String
	var version int64
	var g Grant
	if !in.ReadASN1(&seq, cbasn1.SEQUENCE) || !in.Empty() {
		return Grant{}, &ExtensionError{Reason: "not a DER SEQUENCE"}
	}
	if !seq.ReadASN1Integer(&version) {
		return Grant{}, &ExtensionError{Reason: "malformed version"}
	}
	if version != Version {
		return Grant{}, &ExtensionError{Reason: "unsupported version " + strconv.FormatInt(version, 10)}
	}
	if !readUTF8(&seq, &g.Tenant) || !readUTF8(&seq, &g.Principal) {
		return Grant{}, &ExtensionError{Reason: "malformed tenant or principal"}
	}
	if !seq.ReadASN1(&scope, cbasn1.SEQUENCE) || !seq.Empty() {
		return Grant{}, &ExtensionError{Reason: "malformed scope"}
	}
	for !scope.Empty() {
		var p string
		if !readUTF8(&scope, &p) {
			return Grant{}, &ExtensionError{Reason: "malformed scope entry"}
		}
		g.Scope = append(g.Scope, p)
	}
	if err := g.Validate(); err != nil {
		return Grant{}, &ExtensionError{Reason: "invalid grant", Err: err}
	}
	return g, nil
}

// readUTF8 reads one UTF8String holding valid UTF-8 into out.
func readUTF8(s *cryptobyte.String, out *string) bool {
	var v cryptobyte.String
	if !s.ReadASN1(&v, cbasn1.UTF8String) || !utf8.Valid(v) {
		return false
	}
	*out = string(v)
	return true
}

// Verifiable returns the certificate crypto/x509 should verify in c's place.
//
// crypto/x509 refuses any certificate with a critical extension it does not
// handle -- which is the point of marking a delegation critical -- so a
// verifier that DOES handle it says so by verifying a shallow copy whose
// UnhandledCriticalExtensions no longer lists OID. The copy is made only when
// the extension parses as a valid grant; any other certificate, including
// one with a malformed delegation, is returned unchanged and is refused by
// crypto/x509 exactly as before. Handling the extension here obliges the
// caller to enforce it: a verifier that calls Verifiable and then reads the
// certificate as a user login has defeated the extension's criticality.
func Verifiable(c *x509.Certificate) *x509.Certificate {
	if c == nil {
		return nil
	}
	i := slices.IndexFunc(c.UnhandledCriticalExtensions, OID.Equal)
	if i < 0 {
		return c
	}
	if _, found, err := FromCertificate(c); !found || err != nil {
		return c
	}
	handled := *c
	handled.UnhandledCriticalExtensions = slices.Delete(slices.Clone(c.UnhandledCriticalExtensions), i, i+1)
	return &handled
}

// CheckLifetime reports whether c's validity window is within MaxLifetime,
// returning *LifetimeError otherwise.
func CheckLifetime(c *x509.Certificate) error {
	if life := c.NotAfter.Sub(c.NotBefore); life > MaxLifetime {
		return &LifetimeError{Lifetime: life}
	}
	return nil
}
