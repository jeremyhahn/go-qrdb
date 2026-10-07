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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"
)

// TestSuiteNames_AreTheRequiredSet pins the registry: the three classical
// DHKEMs and the post-quantum hybrid, and nothing else.
func TestSuiteNames_AreTheRequiredSet(t *testing.T) {
	want := []string{KEMP256, KEMP384, KEMX25519, KEMMLKEM768X25519}
	got := SuiteNames()
	if len(got) != len(want) {
		t.Fatalf("SuiteNames = %v, want %d suites", got, len(want))
	}
	have := map[string]bool{}
	for _, n := range got {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Fatalf("suite %q missing from %v", n, got)
		}
	}
}

// TestSuite_RoundTripEveryKEM seals and opens under every registered suite
// with a freshly generated key.
func TestSuite_RoundTripEveryKEM(t *testing.T) {
	for _, name := range SuiteNames() {
		t.Run(name, func(t *testing.T) {
			s, err := LookupSuite(name)
			if err != nil {
				var notApproved *SuiteNotApprovedError
				if errors.As(err, &notApproved) {
					t.Skipf("%s is not FIPS 140-3 approved; refused by LookupSuite under GODEBUG=fips140=only, see docs/seal/fips140-mode.md", name)
				}
				t.Fatal(err)
			}
			sk, err := s.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			ct, err := s.Seal(sk.PublicKey().Bytes(), []byte("info"), []byte("share"))
			if err != nil {
				t.Fatal(err)
			}
			pt, err := s.Open(sk, []byte("info"), ct)
			if err != nil {
				t.Fatal(err)
			}
			if string(pt) != "share" {
				t.Fatalf("round trip = %q", pt)
			}
			// The info binding is authenticated.
			if _, err := s.Open(sk, []byte("other"), ct); !errors.Is(err, ErrDecrypt) {
				t.Fatalf("Open with different info = %v, want ErrDecrypt", err)
			}
			// A serialized private key round-trips through PrivateKey.
			raw, err := sk.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			sk2, err := s.PrivateKey(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Open(sk2, []byte("info"), ct); err != nil {
				t.Fatalf("Open with deserialized key: %v", err)
			}
		})
	}
}

// TestSuite_OpenRefusesAKeyFromAnotherKEM pins that a suite never decrypts
// with a key of a different KEM, instead of failing opaquely deeper in.
func TestSuite_OpenRefusesAKeyFromAnotherKEM(t *testing.T) {
	// P-384 vs. P-256 rather than X25519 vs. P-256: both are FIPS 140-3
	// approved, so this proves the generic KEM-identity mismatch (Open
	// compares sk.KEM().ID() against the suite's own) without depending on
	// a KEM that GODEBUG=fips140=only refuses to even generate a key for.
	p256s, _ := LookupSuite(KEMP256)
	p384s, _ := LookupSuite(KEMP384)
	sk, err := p384s.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *KEMKeyMismatchError
	if _, err := p256s.Open(sk, nil, []byte("x")); !errors.As(err, &mismatch) {
		t.Fatalf("Open = %v, want KEMKeyMismatchError", err)
	}
	if _, err := p256s.Open(nil, nil, []byte("x")); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("Open(nil) = %v, want ErrNilRecipientKey", err)
	}
}

// TestSuite_RejectsBadInput covers the unknown-name, empty-key, malformed-key
// and empty-plaintext refusals.
func TestSuite_RejectsBadInput(t *testing.T) {
	var unsupported *UnsupportedKEMError
	if _, err := LookupSuite("rsa-oaep"); !errors.As(err, &unsupported) || unsupported.Name != "rsa-oaep" {
		t.Fatalf("LookupSuite(rsa-oaep) = %v", err)
	}
	s, _ := LookupSuite(KEMP256)
	if _, err := s.PublicKey(nil); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("PublicKey(nil) = %v", err)
	}
	var badKey *UnsupportedRecipientKeyError
	if _, err := s.PublicKey([]byte{1, 2, 3}); !errors.As(err, &badKey) {
		t.Fatalf("PublicKey(garbage) = %v", err)
	}
	if _, err := s.PrivateKey(nil); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("PrivateKey(nil) = %v", err)
	}
	if _, err := s.PrivateKey([]byte{1}); !errors.As(err, &badKey) {
		t.Fatalf("PrivateKey(garbage) = %v", err)
	}
	sk, _ := s.GenerateKey()
	if _, err := s.Seal(sk.PublicKey().Bytes(), nil, nil); !errors.Is(err, ErrEmptyPlaintext) {
		t.Fatalf("Seal(empty) = %v", err)
	}
	if _, err := s.Seal([]byte{9}, nil, []byte("x")); !errors.As(err, &badKey) {
		t.Fatalf("Seal to garbage key = %v", err)
	}
	if s.Name() != KEMP256 {
		t.Fatalf("Name = %q", s.Name())
	}
}

// TestRecipientFromPublicKey_MapsCurvesAndRefusesTheRest pins which key
// types can be sealed to, and that RSA and Ed25519 are refused rather than
// silently mapped.
func TestRecipientFromPublicKey_MapsCurvesAndRefusesTheRest(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p521, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	type recipientCase struct {
		name string
		pub  any
		want string
	}
	cases := []recipientCase{
		{"ecdsa p256", &p256.PublicKey, KEMP256},
		{"ecdsa p384", &p384.PublicKey, KEMP384},
	}
	// X25519 key generation is itself refused under GODEBUG=fips140=only
	// (crypto/ecdh), so this positive case is unreachable there; see
	// docs/seal/fips140-mode.md "Skipped tests under fips140=only". It is
	// included whenever generation succeeds, which is every other build.
	if x, xerr := ecdh.X25519().GenerateKey(rand.Reader); xerr == nil {
		cases = append(cases, recipientCase{"ecdh x25519", x.PublicKey(), KEMX25519})
	} else if !fips140.Enforced() {
		t.Fatalf("ecdh.X25519().GenerateKey: %v", xerr)
	}
	for _, tc := range cases {
		got, raw, err := RecipientFromPublicKey(tc.pub)
		if err != nil || got != tc.want || len(raw) == 0 {
			t.Fatalf("%s: RecipientFromPublicKey = %q, %d bytes, %v", tc.name, got, len(raw), err)
		}
		// The encoding is the one the suite deserializes.
		s, _ := LookupSuite(got)
		if _, err := s.PublicKey(raw); err != nil {
			t.Fatalf("%s: suite rejects its own encoding: %v", tc.name, err)
		}
	}

	var bad *UnsupportedRecipientKeyError
	for name, pub := range map[string]any{"rsa": &rk.PublicKey, "ed25519": edPub, "ecdsa p521": &p521.PublicKey} {
		if _, _, err := RecipientFromPublicKey(pub); !errors.As(err, &bad) {
			t.Fatalf("%s: RecipientFromPublicKey = %v, want UnsupportedRecipientKeyError", name, err)
		}
	}

	der, err := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := RecipientFromPKIX(der); err != nil || got != KEMP384 {
		t.Fatalf("RecipientFromPKIX = %q, %v", got, err)
	}
	if _, _, err := RecipientFromPKIX([]byte("not der")); !errors.As(err, &bad) {
		t.Fatalf("RecipientFromPKIX(garbage) = %v", err)
	}
}

// TestPrivateKeyFor_EnforcesTheSuite pins the private-key side of the same
// mapping, including the refusal to use a P-384 key under the P-256 suite.
func TestPrivateKeyFor_EnforcesTheSuite(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	type suiteCase struct {
		suite string
		priv  any
		pub   any
	}
	cases := []suiteCase{
		{KEMP256, p256, &p256.PublicKey},
		{KEMP384, p384, &p384.PublicKey},
	}
	// X25519 key generation is itself refused under GODEBUG=fips140=only,
	// so this case is unreachable there; see docs/seal/fips140-mode.md
	// "Skipped tests under fips140=only".
	if x, xerr := ecdh.X25519().GenerateKey(rand.Reader); xerr == nil {
		cases = append(cases, suiteCase{KEMX25519, x, x.PublicKey()})
	} else if !fips140.Enforced() {
		t.Fatalf("ecdh.X25519().GenerateKey: %v", xerr)
	}

	for _, tc := range cases {
		sk, err := PrivateKeyFor(tc.suite, tc.priv)
		if err != nil {
			t.Fatalf("%s: %v", tc.suite, err)
		}
		_, raw, _ := RecipientFromPublicKey(tc.pub)
		s, _ := LookupSuite(tc.suite)
		ct, err := s.Seal(raw, []byte("i"), []byte("p"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Open(sk, []byte("i"), ct); err != nil {
			t.Fatalf("%s: open with converted key: %v", tc.suite, err)
		}
	}

	var mismatch *KEMKeyMismatchError
	if _, err := PrivateKeyFor(KEMP256, p384); !errors.As(err, &mismatch) {
		t.Fatalf("P-384 key under P-256 = %v", err)
	}
	var bad *UnsupportedRecipientKeyError
	if _, err := PrivateKeyFor(KEMP256, edPriv); !errors.As(err, &bad) {
		t.Fatalf("ed25519 key = %v", err)
	}
	var unsupported *UnsupportedKEMError
	if _, err := PrivateKeyFor("nope", p256); !errors.As(err, &unsupported) {
		t.Fatalf("unknown suite = %v", err)
	}
}

// TestSuite_ErrorMessages keeps the typed errors' messages meaningful.
func TestSuite_ErrorMessages(t *testing.T) {
	for _, err := range []error{
		&UnsupportedKEMError{Name: "x"},
		&UnsupportedRecipientKeyError{Reason: "r"},
		&KEMKeyMismatchError{Suite: "s"},
	} {
		if err.Error() == "" {
			t.Fatalf("%T has an empty message", err)
		}
	}
}
