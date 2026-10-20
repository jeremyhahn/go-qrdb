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

package spiffe

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestValidateTrustDomain(t *testing.T) {
	for _, ok := range []string{"db.example.com", "example.org", "qrdb.cluster", "a-b.example", "acme_1", strings.Repeat("a", maxTrustDomainLen)} {
		if err := ValidateTrustDomain(ok); err != nil {
			t.Errorf("ValidateTrustDomain(%q) = %v, want nil", ok, err)
		}
	}
	cases := map[string]error{
		"":                            ErrTrustDomainEmpty,
		"   ":                         ErrTrustDomainInvalid,
		"DB.example.com":              ErrTrustDomainInvalid,
		"db.example.com:8443":         ErrTrustDomainInvalid,
		"db%2Eexample.com":            ErrTrustDomainInvalid,
		strings.Repeat("a", 256):      ErrTrustDomainInvalid,
		"db.example.com/tenant/other": ErrTrustDomainInvalid,
		"db.example.com?x=1":          ErrTrustDomainInvalid,
		"db.example.com#frag":         ErrTrustDomainInvalid,
		"user@db.example.com":         ErrTrustDomainInvalid,
		"db example com":              ErrTrustDomainInvalid,
	}
	for td, want := range cases {
		err := ValidateTrustDomain(td)
		if !errors.Is(err, want) {
			t.Errorf("ValidateTrustDomain(%q) = %v, want %v", td, err, want)
		}
		// The refusal carries the rejected value, verbatim.
		var tde *TrustDomainError
		if !errors.As(err, &tde) || tde.Value != td {
			t.Errorf("ValidateTrustDomain(%q) = %#v, want *TrustDomainError{Value: %q}", td, err, td)
			continue
		}
		if !strings.Contains(err.Error(), strconv.Quote(td)) {
			t.Errorf("message %q does not quote the rejected value %q", err.Error(), td)
		}
	}
}
