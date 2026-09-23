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
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// HasMFA reports whether the account has at least one enrolled auth factor
// registered through pkg/authfactors. MFA state is DERIVED from Factors,
// never stored as a parallel field (go-qrdb#192): an enrolled factor is a
// fact about what the account HAS, not a policy statement that MFA is
// REQUIRED for it — a requirement bit, if one is ever needed, belongs in
// policy, not on the account record.
func (u *User) HasMFA() bool {
	if u == nil {
		return false
	}
	return len(u.Factors) > 0
}

// MFAMethods returns the distinct pkg/authfactors names of the account's
// currently enrolled factors, in first-enrollment order with duplicates
// removed. Empty (nil) when HasMFA is false.
func (u *User) MFAMethods() []string {
	if u == nil || len(u.Factors) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(u.Factors))
	methods := make([]string, 0, len(u.Factors))
	for _, f := range u.Factors {
		if f.FactorName == "" {
			continue
		}
		if _, ok := seen[f.FactorName]; ok {
			continue
		}
		seen[f.FactorName] = struct{}{}
		methods = append(methods, f.FactorName)
	}
	if len(methods) == 0 {
		return nil
	}
	return methods
}

// MFAEnrolledAt returns the earliest enrollment time among the account's
// currently enrolled factors, and false when none are enrolled. The account's
// MFA posture is dated to its FIRST factor's enrollment, not its most recent.
func (u *User) MFAEnrolledAt() (time.Time, bool) {
	if u == nil || len(u.Factors) == 0 {
		return time.Time{}, false
	}
	earliest := u.Factors[0].CreatedAt
	for _, f := range u.Factors[1:] {
		if f.CreatedAt.Before(earliest) {
			earliest = f.CreatedAt
		}
	}
	return earliest, true
}

// WireFactors projects the account's factor enrollments onto the SDK wire
// type, METADATA ONLY: the opaque Data blob a registered pkg/authfactors.Factor
// interprets NEVER crosses this or any other transport, on read or write
// (go-qrdb#192/#197). Every transport's response projection calls this so the
// omission is enforced in one place rather than re-decided per handler.
func WireFactors(factors []FactorEnrollment) []transport.FactorMetadata {
	if len(factors) == 0 {
		return nil
	}
	out := make([]transport.FactorMetadata, len(factors))
	for i, f := range factors {
		out[i] = transport.FactorMetadata{ID: f.ID, FactorName: f.FactorName}
		if !f.CreatedAt.IsZero() {
			out[i].CreatedAt = f.CreatedAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// WireMFA projects the account's derived MFA state onto the three SDK wire
// fields (enabled, methods, enrolled-at). Every transport's response
// projection calls this so the derivation lives in exactly one place.
func WireMFA(u *User) (enabled bool, methods []string, enrolledAt string) {
	if u == nil {
		return false, nil, ""
	}
	enabled = u.HasMFA()
	methods = u.MFAMethods()
	if at, ok := u.MFAEnrolledAt(); ok {
		enrolledAt = at.UTC().Format(time.RFC3339)
	}
	return enabled, methods, enrolledAt
}
