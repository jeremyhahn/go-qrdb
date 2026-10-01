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

package seal

import (
	"crypto/sha256"
	"encoding/json"
)

// Share holders and the holder roster.
//
// A share holder is a named person who holds exactly one Shamir share of one
// barrier key: a Security Officer or a key custodian. The two roles differ in
// the RBAC rights their identity carries, never in how shares reach them or
// how they submit them.
//
// The HolderRoster is the public list of the holders a key was split to: who
// holds which share index, which authentication factors each may present, and
// the public material each factor verifies against. It is written INSIDE the
// wrapped-key blob and its SHA-256 digest is part of that blob's AEAD
// associated data, so:
//
//   - it is readable while the barrier is sealed (it holds nothing secret),
//     which is what lets a sealed node authenticate share submissions;
//   - any edit to it makes the wrapped key fail to open with
//     ErrUnsealKeyMismatch — tampering with who may submit is caught by the
//     same authenticated decrypt that checks the shares themselves;
//   - it changes only when shares are redistributed, which is the only time
//     the key it is bound to exists in memory.

// QuorumDomain names which barrier key a share set reconstructs. Every
// barrier has an unseal-key domain and, once recovery keys are generated, an
// independent recovery-key domain with its own holders, wrapped blob and
// roster.
type QuorumDomain string

const (
	// DomainUnseal is the unseal-key domain: its shares reconstruct the
	// unseal key that wraps the root key.
	DomainUnseal QuorumDomain = "unseal"

	// DomainRecovery is the recovery-key domain: an independent key that also
	// wraps the root key, split to the recovery holders. A rekey of the
	// unseal-key domain does not touch it, and vice versa.
	DomainRecovery QuorumDomain = "recovery"
)

// Valid reports whether d is a known domain.
func (d QuorumDomain) Valid() bool {
	return d == DomainUnseal || d == DomainRecovery
}

// QuorumOp names what a completed quorum does.
type QuorumOp string

const (
	// OpUnseal unseals a sealed barrier (unseal-key domain).
	OpUnseal QuorumOp = "unseal"

	// OpSeal seals an unsealed Shamir barrier (unseal-key domain).
	OpSeal QuorumOp = "seal"

	// OpRootToken mints a one-time root token (unseal-key domain).
	OpRootToken QuorumOp = "root-token"

	// OpRekey redistributes a domain's key to a new holder set or
	// threshold. It is the only way a holder is revoked.
	OpRekey QuorumOp = "rekey"

	// OpRecover unseals a sealed barrier from the recovery-key domain.
	OpRecover QuorumOp = "recover"
)

// quorumOpDomains maps each operation onto the domains it may run in.
var quorumOpDomains = map[QuorumOp]map[QuorumDomain]bool{
	OpUnseal:    {DomainUnseal: true},
	OpSeal:      {DomainUnseal: true},
	OpRootToken: {DomainUnseal: true},
	OpRekey:     {DomainUnseal: true, DomainRecovery: true},
	OpRecover:   {DomainRecovery: true},
}

// ValidIn reports whether op may run in domain.
func (op QuorumOp) ValidIn(d QuorumDomain) bool {
	return quorumOpDomains[op][d]
}

// QuorumOpCount returns how many operations may run in domain.
func QuorumOpCount(d QuorumDomain) int {
	n := 0
	for _, domains := range quorumOpDomains {
		if domains[d] {
			n++
		}
	}
	return n
}

// HolderRole is a share holder's policy role. It selects the certificate
// profile and the RBAC role the holder's identity maps to, and nothing about
// share handling.
type HolderRole string

const (
	// RoleSecurityOfficer is a holder who also carries governance rights.
	RoleSecurityOfficer HolderRole = "so"

	// RoleCustodian is a holder whose identity carries share operations only.
	RoleCustodian HolderRole = "custodian"
)

// Valid reports whether r is a known role.
func (r HolderRole) Valid() bool {
	return r == RoleSecurityOfficer || r == RoleCustodian
}

// FactorPolicy says which of a holder's enrolled factors may authenticate a
// share submission. It is chosen per domain by the site administrator when
// the key is distributed and is bound into the roster, so changing it
// requires a redistribution like any other roster change.
type FactorPolicy string

const (
	// FactorPolicyDesignated admits exactly one factor per holder, fixed at
	// enrolment. It is the default: a holder who enrolled a hardware factor
	// cannot be authenticated by a weaker one.
	FactorPolicyDesignated FactorPolicy = "designated"

	// FactorPolicyAnyEnrolled admits any factor the holder has enrolled.
	FactorPolicyAnyEnrolled FactorPolicy = "any-enrolled"
)

// OrDefault returns p, or FactorPolicyDesignated when p is empty.
func (p FactorPolicy) OrDefault() FactorPolicy {
	if p == "" {
		return FactorPolicyDesignated
	}
	return p
}

// Valid reports whether p (after defaulting) is a known policy.
func (p FactorPolicy) Valid() bool {
	d := p.OrDefault()
	return d == FactorPolicyDesignated || d == FactorPolicyAnyEnrolled
}

// rosterVersion is the current roster format version.
const rosterVersion = 1

// RosterFactor is one enrolled authentication factor's public material.
type RosterFactor struct {
	// Name is the factor name ("mtls", "fido2", "piv", ...).
	Name string `json:"name"`

	// EnrollmentID identifies the enrollment the factor produced.
	EnrollmentID string `json:"enrollment_id"`

	// PublicKey is the factor's public credential (for "mtls", the enrolled
	// certificate's SubjectPublicKeyInfo).
	PublicKey []byte `json:"public_key"`

	// Metadata carries the factor's public verification attributes.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RosterHolder is one holder's entry.
type RosterHolder struct {
	// Username names the holder.
	Username string `json:"username"`

	// Role is the holder's policy role.
	Role HolderRole `json:"role"`

	// ShareIndex is the holder's 1-based Shamir share index: the
	// x-coordinate every share they submit must carry.
	ShareIndex int `json:"share_index"`

	// CertThumbprint is the lowercase hex SHA-256 of the holder's
	// certificate DER.
	CertThumbprint string `json:"cert_thumbprint"`

	// Designated names the one factor admitted under FactorPolicyDesignated.
	Designated string `json:"designated"`

	// Factors is every factor the holder enrolled, in enrolment order.
	Factors []RosterFactor `json:"factors"`
}

// HolderRoster is the public, sealed-readable list of the holders a domain's
// key was split to.
type HolderRoster struct {
	// Version is the roster format version.
	Version int `json:"version"`

	// Domain is the key domain the roster belongs to.
	Domain QuorumDomain `json:"domain"`

	// Policy is the factor policy in force for this share set.
	Policy FactorPolicy `json:"policy"`

	// Threshold is M, the number of distinct holders a quorum needs.
	Threshold int `json:"threshold"`

	// Total is N, the number of holders and shares.
	Total int `json:"total"`

	// Distribution is a random identifier the barrier assigns to every
	// distribution. It makes each share set's roster — and so its digest —
	// unique even when a rekey keeps the same holders, threshold and policy,
	// so shares gathered for one distribution can never be mistaken for
	// another's.
	Distribution string `json:"distribution"`

	// Holders lists every holder, ordered by share index.
	Holders []RosterHolder `json:"holders"`
}

// NewHolderRoster returns an empty roster for domain with the given policy
// and threshold. Holders are appended by the distributor that prepares it.
func NewHolderRoster(domain QuorumDomain, policy FactorPolicy, threshold int) *HolderRoster {
	return &HolderRoster{
		Version:   rosterVersion,
		Domain:    domain,
		Policy:    policy.OrDefault(),
		Threshold: threshold,
	}
}

// Validate checks the roster is internally consistent: a known domain and
// policy, 2 <= Threshold <= Total == len(Holders), share indexes exactly
// 1..Total, unique usernames and certificates, and at least one factor per
// holder with the designated factor among them.
func (r *HolderRoster) Validate() error {
	if r == nil || r.Version != rosterVersion || !r.Domain.Valid() || !r.Policy.Valid() {
		return &InvalidRosterError{Reason: "unknown version, domain or policy"}
	}
	if r.Distribution == "" {
		return &InvalidRosterError{Reason: "no distribution identifier"}
	}
	if r.Threshold < 2 || r.Threshold > r.Total || r.Total != len(r.Holders) {
		return &InvalidRosterError{Reason: "threshold must be in [2, total] and total must equal the holder count"}
	}
	seenIndex := make(map[int]bool, len(r.Holders))
	seenName := make(map[string]bool, len(r.Holders))
	seenCert := make(map[string]bool, len(r.Holders))
	for i := range r.Holders {
		h := &r.Holders[i]
		switch {
		case h.Username == "" || seenName[h.Username]:
			return &InvalidRosterError{Reason: "holder usernames must be present and unique"}
		case h.ShareIndex < 1 || h.ShareIndex > r.Total || seenIndex[h.ShareIndex]:
			return &InvalidRosterError{Reason: "share indexes must be exactly 1..total"}
		case h.CertThumbprint == "" || seenCert[h.CertThumbprint]:
			return &InvalidRosterError{Reason: "holder certificates must be present and unique"}
		case !h.Role.Valid():
			return &InvalidRosterError{Reason: "holder " + h.Username + " has no valid role"}
		case len(h.Factors) == 0:
			return &InvalidRosterError{Reason: "holder " + h.Username + " has no enrolled factor"}
		}
		if _, ok := h.factor(h.Designated); !ok {
			return &InvalidRosterError{Reason: "holder " + h.Username + " designates a factor it has not enrolled"}
		}
		seenIndex[h.ShareIndex] = true
		seenName[h.Username] = true
		seenCert[h.CertThumbprint] = true
	}
	return nil
}

// Holder returns the named holder's entry.
func (r *HolderRoster) Holder(username string) (*RosterHolder, bool) {
	for i := range r.Holders {
		if r.Holders[i].Username == username {
			return &r.Holders[i], true
		}
	}
	return nil, false
}

// AdmittedFactor returns the named holder's factor called name, if the
// roster's policy admits it for share submission. Under
// FactorPolicyDesignated only the designated factor is admitted.
func (r *HolderRoster) AdmittedFactor(username, name string) (*RosterFactor, error) {
	h, ok := r.Holder(username)
	if !ok {
		return nil, &HolderNotInRosterError{Holder: username}
	}
	if name == "" {
		name = h.Designated
	}
	if r.Policy.OrDefault() == FactorPolicyDesignated && name != h.Designated {
		return nil, &FactorNotAdmittedError{Holder: username, Factor: name, Policy: r.Policy.OrDefault()}
	}
	f, ok := h.factor(name)
	if !ok {
		return nil, &FactorNotAdmittedError{Holder: username, Factor: name, Policy: r.Policy.OrDefault()}
	}
	return f, nil
}

// factor returns the holder's enrolled factor called name.
func (h *RosterHolder) factor(name string) (*RosterFactor, bool) {
	for i := range h.Factors {
		if h.Factors[i].Name == name {
			return &h.Factors[i], true
		}
	}
	return nil, false
}

// encodeRoster serializes the roster to the exact bytes stored in the
// wrapped blob and returns them with their SHA-256 digest. encoding/json is
// deterministic for these types (struct fields in declaration order, map keys
// sorted), but the digest is always taken over the STORED bytes, so it never
// depends on re-encoding agreeing with itself.
func encodeRoster(r *HolderRoster) ([]byte, []byte, error) {
	if err := r.Validate(); err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, nil, &InvalidRosterError{Reason: err.Error()}
	}
	sum := sha256.Sum256(raw)
	return raw, sum[:], nil
}

// decodeRoster parses and validates stored roster bytes and returns the
// roster with the digest of exactly those bytes.
func decodeRoster(raw []byte) (*HolderRoster, []byte, error) {
	if len(raw) == 0 {
		return nil, nil, ErrHolderRosterMissing
	}
	var r HolderRoster
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, &InvalidRosterError{Reason: err.Error()}
	}
	if err := r.Validate(); err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	return &r, sum[:], nil
}
