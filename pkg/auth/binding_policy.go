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

package auth

import (
	"strconv"
	"strings"
)

// MTLSBindingPolicy is the fleet-wide posture for mTLS certificate enrolment
// (auth.mtls_binding_policy, go-qrdb#517, go-qrdb#548): whether an account
// must have enrolled a certificate binding before a certificate can log in
// as it.
//
// Its ZERO VALUE is MTLSBindingRequired (go-qrdb#548): FedRAMP, HIPAA and
// PCI DSS 4.0 require possession-based authentication of every account, not
// only the ones an operator happened to enrol, and a config that omits the
// key or sets it to the empty string must not silently leave a fleet in the
// non-compliant posture. An embedder that never sets this field gets the
// COMPLIANT behaviour, not the historical one -- the opposite of the
// pre-go-qrdb#548 zero value. Selecting the weaker posture is now an
// explicit, visible opt-out: auth.mtls_binding_policy: enrolled.
type MTLSBindingPolicy int

const (
	// MTLSBindingRequired requires EVERY certificate login to present a
	// certificate enrolled on the account it names. An account with no
	// bindings, and a subject that resolves to no account at all, is refused.
	// This is the DEFAULT and the zero value.
	//
	// It is a real lockout risk for an account nobody has enrolled yet, so a
	// node that selects it refuses to boot when its bootstrap admin could not
	// log in under it (see firstboot.VerifyAdminCertBinding). Every
	// composition in this module enrols the bootstrap admin's certificate as
	// part of first boot (go-qrdb#548) so a default boot is not locked out of
	// itself.
	MTLSBindingRequired MTLSBindingPolicy = iota

	// MTLSBindingEnrolled enforces a binding only on accounts that have one.
	// An account with no bindings authenticates on the certificate's CN and
	// tenant alone -- which makes an unenrolled account exactly as
	// impersonable as the CA's issuance paths allow.
	//
	// This does NOT meet FedRAMP, HIPAA or PCI DSS 4.0: an unenrolled
	// account is impersonable by any certificate-issuance path that can mint
	// a certificate naming it, mTLS possession proves nothing for that
	// account, and the gap is invisible until an issuance path is audited.
	// It exists as an explicit, visible, operator-chosen opt-out -- never a
	// default -- for a fleet that has not finished migrating every account's
	// enrolment yet; see docs/identity-rbac/user.md#moving-a-fleet-to-required-certificate-enrolment
	// for the (reversed, as of go-qrdb#548) migration direction: a fleet now
	// starts on "required" and opts OUT to "enrolled", rather than opting IN.
	MTLSBindingEnrolled
)

// mtlsBindingPolicyNames maps the accepted configuration spellings onto the
// policy. Empty maps to MTLSBindingRequired through the same table every
// other spelling goes through, so "omitted" cannot drift from "required"
// (go-qrdb#548).
var mtlsBindingPolicyNames = map[string]MTLSBindingPolicy{
	"":         MTLSBindingRequired,
	"enrolled": MTLSBindingEnrolled,
	"required": MTLSBindingRequired,
}

// String returns the configuration spelling of the policy.
func (p MTLSBindingPolicy) String() string {
	if p == MTLSBindingEnrolled {
		return "enrolled"
	}
	return "required"
}

// ParseMTLSBindingPolicy resolves an operator's configured spelling.
//
// An unrecognised value is an *InvalidMTLSBindingPolicyError, never a guess.
// Resolving a typo to "enrolled" would silently leave unenrolled accounts
// admissible on a fleet whose operator asked for them not to be; resolving it
// to "required" could lock that fleet out of itself. The boot fails instead.
func ParseMTLSBindingPolicy(s string) (MTLSBindingPolicy, error) {
	p, ok := mtlsBindingPolicyNames[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return MTLSBindingEnrolled, &InvalidMTLSBindingPolicyError{Value: s}
	}
	return p, nil
}

// InvalidMTLSBindingPolicyError is returned by ParseMTLSBindingPolicy when
// auth.mtls_binding_policy is not a value this build understands.
type InvalidMTLSBindingPolicyError struct {
	Value string
}

// Error names the rejected value and the accepted ones.
func (e *InvalidMTLSBindingPolicyError) Error() string {
	return "qrdb/auth: invalid mtls binding policy " + strconv.Quote(e.Value) +
		` (want "enrolled" or "required")`
}
