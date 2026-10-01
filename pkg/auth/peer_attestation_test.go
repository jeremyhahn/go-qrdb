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

import "testing"

// The failure class is the audit spelling a composer and an operator both
// read, so each value's spelling is pinned, and an out-of-range value is named
// rather than rendered as an empty attribute.
func TestPeerAttestationFailure_String(t *testing.T) {
	cases := map[PeerAttestationFailure]string{
		PeerAttestationFailureNone:          "none",
		PeerAttestationFailureAbsence:       "absence",
		PeerAttestationFailureCryptographic: "cryptographic",
		PeerAttestationFailure(99):          "unknown",
	}
	for f, want := range cases {
		if got := f.String(); got != want {
			t.Errorf("PeerAttestationFailure(%d).String() = %q, want %q", int(f), got, want)
		}
	}
}

func TestPeerAttestationFailure_ZeroValueIsNone(t *testing.T) {
	var verdict PeerAttestation
	if verdict.Failure != PeerAttestationFailureNone || verdict.Passed {
		t.Fatalf("zero verdict = %+v, want not passed with no failure class", verdict)
	}
}
