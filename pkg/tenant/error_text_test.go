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

package tenant

import (
	"errors"
	"strings"
	"testing"
)

// TestErrorText pins that each refusal names what an operator needs to act
// on: the tenant, the value refused and, where one exists, the cause.
func TestErrorText(t *testing.T) {
	cause := errors.New("store offline")
	for name, tc := range map[string]struct {
		err  error
		want []string
	}{
		"platform operator": {&PlatformOperatorRequiredError{IdentityTenantID: "acme", Op: "set quotas"}, []string{"acme", "set quotas", "platform operator"}},
		"volume pool":       {&VolumePoolReserveError{TenantID: "acme", Pool: "fast", Err: cause}, []string{"acme", "fast", "store offline"}},
		"roster":            {&RosterRequiresShamirError{Strategy: "passphrase"}, []string{"shamir", "passphrase"}},
		"factor policy":     {&InvalidFactorPolicyError{Policy: "any-two"}, []string{"any-two"}},
		"deleted, no time":  {&TenantDeletedError{TenantID: "acme"}, []string{"acme", "cannot be reused"}},
		"keyspace reclaim":  {&KeyspaceReclaimError{TenantID: "acme", Err: cause}, []string{"acme", "store offline"}},
		"database list":     {&DatabaseListError{TenantID: "acme", Err: cause}, []string{"acme", "store offline"}},
		"isolation change":  {&IsolationPolicyChangeError{From: "strict", To: "operator-access"}, []string{"strict", "operator-access", "tighten only"}},
		"isolation invalid": {&InvalidIsolationPolicyError{Value: "loose"}, []string{`"loose"`}},
		"claim conflict":    {&IdentityClaimConflictError{Kind: IdentityUser, ID: "7"}, []string{"user", "7", "already claimed"}},
		"claim stale":       {&IdentityClaimStaleError{Kind: IdentityRole, ID: "9"}, []string{"role", "9"}},
		"identity index":    {&IdentityIndexError{Op: "lookup", Kind: IdentityUser, ID: "7", Err: cause}, []string{"lookup", "user", "7", "store offline"}},
	} {
		t.Run(name, func(t *testing.T) {
			msg := tc.err.Error()
			if !strings.HasPrefix(msg, "tenant: ") {
				t.Errorf("Error() = %q, want the tenant: prefix", msg)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("Error() = %q, want it to name %q", msg, w)
				}
			}
		})
	}
}

// TestIdentityID pins the decimal form identity ids are indexed under.
func TestIdentityID(t *testing.T) {
	for id, want := range map[uint64]string{0: "0", 42: "42", 18446744073709551615: "18446744073709551615"} {
		if got := IdentityID(id); got != want {
			t.Errorf("IdentityID(%d) = %q, want %q", id, got, want)
		}
	}
}
