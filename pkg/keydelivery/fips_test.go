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

package keydelivery

import (
	"crypto/ecdh"
	"crypto/fips140"
	"crypto/rand"
	"errors"
	"testing"
)

// withFIPSMode runs the test with the FIPS predicate forced to on.
func withFIPSMode(t *testing.T, on bool) {
	t.Helper()
	saved := fipsMode
	fipsMode = func() bool { return on }
	t.Cleanup(func() { fipsMode = saved })
}

// TestLookupSuite_FIPSModeRefusesX25519Suites proves a deployment in FIPS
// 140-3 mode is never handed an envelope built on X25519, which is not an
// approved key agreement: both the classic DHKEM(X25519) suite and the
// ML-KEM-768 + X25519 hybrid are refused, while the NIST-curve suites remain.
func TestLookupSuite_FIPSModeRefusesX25519Suites(t *testing.T) {
	withFIPSMode(t, true)
	for _, name := range []string{KEMX25519, KEMMLKEM768X25519} {
		_, err := LookupSuite(name)
		var notApproved *SuiteNotApprovedError
		if !errors.As(err, &notApproved) || notApproved.Name != name {
			t.Errorf("LookupSuite(%s) = %v, want SuiteNotApprovedError", name, err)
		}
	}
	for _, name := range []string{KEMP256, KEMP384} {
		if _, err := LookupSuite(name); err != nil {
			t.Errorf("LookupSuite(%s) = %v in FIPS mode, want the suite", name, err)
		}
	}
}

// TestLookupSuite_StandardModeAdmitsEverySuite proves the refusal is scoped
// to FIPS mode.
func TestLookupSuite_StandardModeAdmitsEverySuite(t *testing.T) {
	withFIPSMode(t, false)
	for _, name := range SuiteNames() {
		if _, err := LookupSuite(name); err != nil {
			t.Errorf("LookupSuite(%s) = %v, want the suite", name, err)
		}
	}
}

// TestRecipientFromPublicKey_FIPSModeRefusesX25519 proves an X25519
// recipient key is refused in FIPS mode rather than mapped onto a suite that
// would fail at seal time.
func TestRecipientFromPublicKey_FIPSModeRefusesX25519(t *testing.T) {
	if fips140.Enforced() {
		// Under real GODEBUG=fips140=only, crypto/ecdh refuses X25519 at
		// GenerateKey itself, so there is no X25519 public key this test
		// could construct to feed RecipientFromPublicKey -- the real
		// enforcement makes the simulated case below unreachable. See
		// docs/seal/fips140-mode.md "Skipped tests under fips140=only".
		t.Skip("X25519 key generation is itself refused under fips140=only; nothing to construct")
	}
	withFIPSMode(t, true)
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var notApproved *SuiteNotApprovedError
	if _, _, err := RecipientFromPublicKey(k.PublicKey()); !errors.As(err, &notApproved) {
		t.Fatalf("err = %v, want SuiteNotApprovedError", err)
	}
	if (&SuiteNotApprovedError{Name: KEMX25519}).Error() == "" {
		t.Fatal("SuiteNotApprovedError must describe itself")
	}
}
