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

package transport

import "testing"

func TestCertBindingKindLabel(t *testing.T) {
	cases := map[int]string{
		0:  CertBindingKindSPKIThumbprint,
		1:  CertBindingKindFullCertFingerprint,
		2:  CertBindingKindPIVAttested,
		3:  "unknown(3)",
		-1: "unknown(-1)",
	}
	for ordinal, want := range cases {
		if got := CertBindingKindLabel(ordinal); got != want {
			t.Errorf("CertBindingKindLabel(%d) = %q, want %q", ordinal, got, want)
		}
	}
}

// The labels are the wire contract; renaming one silently breaks every client
// that branches on it.
func TestCertBindingKindLabels_ArePinned(t *testing.T) {
	if CertBindingKindSPKIThumbprint != "spki_thumbprint" ||
		CertBindingKindFullCertFingerprint != "full_cert_fingerprint" ||
		CertBindingKindPIVAttested != "piv_attested" {
		t.Fatal("a CertBindingKind label changed; that is a wire-contract change")
	}
}
