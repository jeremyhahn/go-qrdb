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

package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
)

func TestHolderProfile(t *testing.T) {
	tests := []struct {
		name    string
		ou      []string
		want    string
		holding bool
	}{
		{"tenant then custodian", []string{"acme", ProfileCustodian}, ProfileCustodian, true},
		{"tenant then security officer", []string{"system", ProfileSecurityOfficer}, ProfileSecurityOfficer, true},
		{"legacy layout, profile first", []string{ProfileCustodian}, ProfileCustodian, true},
		{"user certificate", []string{"acme"}, "", false},
		{"user profile is not a holder", []string{"system", "operator"}, "", false},
		{"no OU", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := HolderProfile(&x509.Certificate{Subject: pkix.Name{OrganizationalUnit: tc.ou}})
			if got != tc.want || ok != tc.holding {
				t.Errorf("HolderProfile = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.holding)
			}
		})
	}
	if _, ok := HolderProfile(nil); ok {
		t.Error("a nil certificate is a holder certificate")
	}
}

func TestIsHolderIdentity(t *testing.T) {
	if IsHolderIdentity(nil) {
		t.Error("a nil identity is a holder")
	}
	if IsHolderIdentity(&Identity{Subject: "u"}) {
		t.Error("an identity with no metadata is a holder")
	}
	if !IsHolderIdentity(&Identity{Subject: "u", Metadata: map[string]string{HolderProfileMetadataKey: ProfileCustodian}}) {
		t.Error("a marked identity is not a holder")
	}
}
