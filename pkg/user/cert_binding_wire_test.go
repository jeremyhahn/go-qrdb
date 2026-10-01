// Copyright 2026 Jeremy Hahn
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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestCertBindingKind_String(t *testing.T) {
	cases := []struct {
		kind CertBindingKind
		want string
	}{
		{CertBindingSPKIThumbprint, transport.CertBindingKindSPKIThumbprint},
		{CertBindingFullCertFingerprint, transport.CertBindingKindFullCertFingerprint},
		{CertBindingPIVAttested, transport.CertBindingKindPIVAttested},
		{CertBindingKind(99), "unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("CertBindingKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// Every binding is listed, in enrolment order, EXPIRED ONES INCLUDED: an
// expired binding no longer authenticates but still holds the account in the
// enforcing posture, so omitting it would misrepresent why the account refuses
// a certificate (go-qrdb#535).
func TestWireCertBindings_ListsEveryBindingIncludingExpired(t *testing.T) {
	expired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.FixedZone("plus2", 2*3600))
	got := WireCertBindings([]CertBinding{
		{Thumbprint: "AAAA", Kind: CertBindingSPKIThumbprint},
		{Thumbprint: "BBBB", Kind: CertBindingFullCertFingerprint, NotAfter: expired},
		{Thumbprint: "CCCC", Kind: CertBindingPIVAttested, NotAfter: future},
	})
	want := []transport.CertBinding{
		{Thumbprint: "AAAA", Kind: "spki_thumbprint"},
		{Thumbprint: "BBBB", Kind: "full_cert_fingerprint", NotAfter: "2020-01-01T00:00:00Z"},
		{Thumbprint: "CCCC", Kind: "piv_attested", NotAfter: "2029-12-31T22:00:00Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d bindings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("binding %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWireCertBindings_NoneIsNil(t *testing.T) {
	if got := WireCertBindings(nil); got != nil {
		t.Fatalf("WireCertBindings(nil) = %v, want nil", got)
	}
	if got := WireCertBindings([]CertBinding{}); got != nil {
		t.Fatalf("WireCertBindings(empty) = %v, want nil", got)
	}
}

func TestWireRoleBindings_RoundTrip(t *testing.T) {
	domain := []RoleBinding{
		{RoleID: "role-a", TenantID: "acme"},
		{RoleID: "role-b", TenantID: "beta"},
	}
	wire := WireRoleBindings(domain)
	if len(wire) != len(domain) {
		t.Fatalf("WireRoleBindings() len = %d, want %d", len(wire), len(domain))
	}
	for i := range domain {
		if wire[i].RoleID != domain[i].RoleID || wire[i].TenantID != domain[i].TenantID {
			t.Errorf("WireRoleBindings()[%d] = %+v, want %+v", i, wire[i], domain[i])
		}
	}

	back := RoleBindingsFromWire(wire)
	if len(back) != len(domain) {
		t.Fatalf("RoleBindingsFromWire() len = %d, want %d", len(back), len(domain))
	}
	for i := range domain {
		if back[i] != domain[i] {
			t.Errorf("RoleBindingsFromWire()[%d] = %+v, want %+v", i, back[i], domain[i])
		}
	}
}

func TestWireRoleBindings_NoneIsNil(t *testing.T) {
	if got := WireRoleBindings(nil); got != nil {
		t.Fatalf("WireRoleBindings(nil) = %v, want nil", got)
	}
	if got := WireRoleBindings([]RoleBinding{}); got != nil {
		t.Fatalf("WireRoleBindings(empty) = %v, want nil", got)
	}
}

func TestRoleBindingsFromWire_NilVsEmpty(t *testing.T) {
	// nil MUST stay nil (leave-alone semantics on UpdateUserRequest); an
	// explicit empty slice MUST stay a non-nil empty slice (clear semantics).
	if got := RoleBindingsFromWire(nil); got != nil {
		t.Fatalf("RoleBindingsFromWire(nil) = %v, want nil", got)
	}
	got := RoleBindingsFromWire([]transport.RoleBinding{})
	if got == nil {
		t.Fatal("RoleBindingsFromWire(empty) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("RoleBindingsFromWire(empty) len = %d, want 0", len(got))
	}
}
