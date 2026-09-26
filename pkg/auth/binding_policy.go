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
// (auth.mtls_binding_policy, go-qrdb#517): whether an account must have
// enrolled a certificate binding before a certificate can log in as it.
//
// Its ZERO VALUE is MTLSBindingEnrolled, which is the behaviour every
// deployment had before the setting existed, so an embedder that never sets it
// is unaffected.
type MTLSBindingPolicy int

const (
	// MTLSBindingEnrolled enforces a binding only on accounts that have one.
	// An account with no bindings authenticates on the certificate's CN and
	// tenant alone -- which keeps an unenrolled account exactly as
	// impersonable as the CA's issuance paths make it.
	MTLSBindingEnrolled MTLSBindingPolicy = iota

	// MTLSBindingRequired requires EVERY certificate login to present a
	// certificate enrolled on the account it names. An account with no
	// bindings, and a subject that resolves to no account at all, is refused.
	//
	// It is a real lockout risk, so a node that selects it refuses to boot
	// when its bootstrap admin could not log in under it (see
	// firstboot.VerifyAdminCertBinding).
	MTLSBindingRequired
)

// mtlsBindingPolicyNames maps the accepted configuration spellings onto the
// policy. Empty maps to MTLSBindingEnrolled through the same table every other
// spelling goes through, so "omitted" cannot drift from "enrolled".
var mtlsBindingPolicyNames = map[string]MTLSBindingPolicy{
	"":         MTLSBindingEnrolled,
	"enrolled": MTLSBindingEnrolled,
	"required": MTLSBindingRequired,
}

// String returns the configuration spelling of the policy.
func (p MTLSBindingPolicy) String() string {
	if p == MTLSBindingRequired {
		return "required"
	}
	return "enrolled"
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
