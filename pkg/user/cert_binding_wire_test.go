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
