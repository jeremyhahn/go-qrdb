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

package user

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// RoleBinding grants a role within a tenant scope. It is the tenant-scoped RBAC
// primitive: a control-plane user can hold an Admin binding for tenant X without
// being an admin of the system tenant.
type RoleBinding struct {
	RoleID   string `json:"roleID"`
	TenantID string `json:"tenantID"`
}

// NormalizeRoleBindings returns bindings as they are stored on the user with
// id userID in userTenant: each TenantID folded onto its canonical form with
// tenant.Normalize, in a new slice so the caller's request is not mutated.
// The read side compares a binding's TenantID with the normalized target
// tenant, so a binding stored as "" would never match anything.
//
// A non-system user may hold a binding only in its own tenant, and any other
// is refused with *RoleBindingTenantError: RBAC refuses such a user every
// request outside its own tenant before a binding is consulted, so the
// binding could never grant. A nil bindings is returned as nil, keeping the
// leave-alone meaning UpdateUserRequest gives it.
func NormalizeRoleBindings(userID uint64, userTenant string, bindings []RoleBinding) ([]RoleBinding, error) {
	if bindings == nil {
		return nil, nil
	}
	userTenant = tenantid.Normalize(userTenant)
	inSystem := tenantid.IsSystem(userTenant)
	out := make([]RoleBinding, len(bindings))
	for i, b := range bindings {
		b.TenantID = tenantid.Normalize(b.TenantID)
		if !inSystem && b.TenantID != userTenant {
			return nil, &RoleBindingTenantError{UserID: userID, UserTenantID: userTenant, BindingTenantID: b.TenantID}
		}
		out[i] = b
	}
	return out, nil
}

// CertBindingKind identifies how an mTLS certificate is matched to a user.
type CertBindingKind int

const (
	// CertBindingSPKIThumbprint binds to the SHA-256 of the certificate's
	// SubjectPublicKeyInfo. It is the CANONICAL default: it tracks the public
	// key, so it survives certificate renewal/re-issuance with the same key.
	CertBindingSPKIThumbprint CertBindingKind = iota
	// CertBindingFullCertFingerprint binds to the SHA-256 of the whole
	// certificate DER. Use only when exact-certificate pinning is required.
	CertBindingFullCertFingerprint
	// CertBindingPIVAttested marks a binding established via PIV attestation.
	CertBindingPIVAttested
)

// String returns the wire label for the kind ("spki_thumbprint",
// "full_cert_fingerprint", "piv_attested"), the form every transport lists a
// binding's kind in. The persisted form stays the ordinal; the label is a
// projection, and the table it comes from lives with the wire type.
func (k CertBindingKind) String() string {
	return transport.CertBindingKindLabel(int(k))
}

// CertBinding binds an mTLS client certificate to a user. Thumbprint is the
// canonical SPKI SHA-256 thumbprint (base64url) unless Kind says otherwise.
type CertBinding struct {
	Thumbprint string          `json:"thumbprint"`
	Kind       CertBindingKind `json:"kind"`
	NotAfter   time.Time       `json:"notAfter,omitempty"`
}

// spkiThumbprintLen is the decoded byte length of a canonical SPKI thumbprint
// (SHA-256 → 32 bytes).
const spkiThumbprintLen = sha256.Size

// ComputeSPKIThumbprint returns the canonical mTLS identity for a certificate:
// the SHA-256 of its SubjectPublicKeyInfo, base64url (no padding). Because it is
// computed over the public key, two certificates issued for the same key (e.g.
// a renewal) produce the same thumbprint. This is THE single algorithm for the
// whole stack — xkms and quicbaas must not reimplement it.
func ComputeSPKIThumbprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// IsValidSPKIThumbprint reports whether s is a canonical SPKI thumbprint: a
// base64url (no padding) encoding of exactly 32 bytes. Binding writes validate
// against this so the identity index only ever holds canonical thumbprints and
// a caller cannot pollute it with arbitrary or non-canonical strings (which a
// lookup using the canonical encoding would never match).
func IsValidSPKIThumbprint(s string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	return len(raw) == spkiThumbprintLen
}

// IsExpired reports whether the binding has a non-zero NotAfter that is at or
// before now. A zero NotAfter means the binding never expires.
func (b CertBinding) IsExpired(now time.Time) bool {
	return !b.NotAfter.IsZero() && !now.Before(b.NotAfter)
}

// AddCertBinding appends a cert binding, de-duplicating by thumbprint.
func (u *User) AddCertBinding(b CertBinding) {
	for i := range u.CertBindings {
		if u.CertBindings[i].Thumbprint == b.Thumbprint {
			u.CertBindings[i] = b
			return
		}
	}
	u.CertBindings = append(u.CertBindings, b)
}

// HasCertBinding reports whether the user has a currently-valid (non-expired)
// binding for the given thumbprint. Expiry is enforced on every resolution so a
// decommissioned credential cannot be replayed after its NotAfter.
func (u *User) HasCertBinding(thumbprint string) bool {
	now := time.Now().UTC()
	for i := range u.CertBindings {
		if u.CertBindings[i].Thumbprint == thumbprint && !u.CertBindings[i].IsExpired(now) {
			return true
		}
	}
	return false
}

// MatchesCert reports whether the presented certificate's SPKI thumbprint is
// bound to this user and the binding has not expired — the mTLS-auth predicate.
func (u *User) MatchesCert(cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	return u.HasCertBinding(ComputeSPKIThumbprint(cert))
}

// WireCertBindings projects the account's bindings onto the SDK wire type,
// in enrolment order and in full — expired ones included, because an expired
// binding still holds the account in the enforcing posture (an account with any
// binding admits only a matching certificate) and hiding it would misrepresent
// why the account refuses one. NotAfter is RFC 3339, or empty for a binding
// that does not expire on its own. The REST, QUIC and embedded projections
// call this; gRPC carries the expiry as seconds and MCP marshals the domain
// record, and each of those is pinned to the same shape by test.
func WireCertBindings(bindings []CertBinding) []transport.CertBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]transport.CertBinding, len(bindings))
	for i, b := range bindings {
		out[i] = transport.CertBinding{Thumbprint: b.Thumbprint, Kind: b.Kind.String()}
		if !b.NotAfter.IsZero() {
			out[i].NotAfter = b.NotAfter.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// WireRoleBindings projects the account's tenant-scoped role bindings onto
// the SDK wire type. Every transport's response projection calls this so a
// binding's shape never drifts between them (go-qrdb#197).
func WireRoleBindings(bindings []RoleBinding) []transport.RoleBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]transport.RoleBinding, len(bindings))
	for i, b := range bindings {
		out[i] = transport.RoleBinding{RoleID: b.RoleID, TenantID: b.TenantID}
	}
	return out
}

// RoleBindingsFromWire is the inverse of WireRoleBindings, used by every
// transport's UpdateUser handler to translate a wire request back into the
// domain shape before calling UpdateUser. A nil input round-trips to nil, so
// the domain's "nil leaves it alone, non-nil-empty clears it" contract
// survives the wire.
func RoleBindingsFromWire(bindings []transport.RoleBinding) []RoleBinding {
	if bindings == nil {
		return nil
	}
	out := make([]RoleBinding, len(bindings))
	for i, b := range bindings {
		out[i] = RoleBinding{RoleID: b.RoleID, TenantID: b.TenantID}
	}
	return out
}
