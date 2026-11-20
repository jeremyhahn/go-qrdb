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

package mtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// enrollmentsOf returns enrollments whose keys are pubs.
func enrollmentsOf(t *testing.T, pubs ...any) []*authfactors.Enrollment {
	t.Helper()
	out := make([]*authfactors.Enrollment, 0, len(pubs))
	for _, pub := range pubs {
		spki, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &authfactors.Enrollment{FactorName: Name, PublicKey: spki})
	}
	return out
}

// TestPredominantKeyKind: the most common enrolled kind wins, ties go to the
// earlier kind, and with nothing classifiable the kind is P-256.
func TestPredominantKeyKind(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p521, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := []struct {
		like []*authfactors.Enrollment
		want KeyKind
	}{
		{enrollmentsOf(t, edPub, edPub, &p256.PublicKey), KeyEd25519},
		{enrollmentsOf(t, &p521.PublicKey, &rsaKey.PublicKey), KeyP521},
		{enrollmentsOf(t, &p256.PublicKey, edPub), KeyP256},
		{enrollmentsOf(t, &rsaKey.PublicKey), KeyP256},
		{nil, KeyP256},
		{[]*authfactors.Enrollment{nil, {PublicKey: []byte("junk")}}, KeyP256},
	}
	for i, c := range cases {
		if got := predominantKeyKind(c.like); got != c.want {
			t.Errorf("case %d: predominantKeyKind = %v, want %v", i, got, c.want)
		}
	}
}

// TestDecoyEnrollment_DeterministicForEveryKind: for every kind, the decoy
// for one user is the same on every call, is of that kind, differs between
// users, and issues a challenge through the factor's own Challenge.
func TestDecoyEnrollment_DeterministicForEveryKind(t *testing.T) {
	seed := make([]byte, 32)
	f := New()
	keys := map[KeyKind]any{}
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p521, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	keys[KeyP256], keys[KeyP384], keys[KeyP521], keys[KeyEd25519] = &p256.PublicKey, &p384.PublicKey, &p521.PublicKey, edPub
	for kind, pub := range keys {
		like := enrollmentsOf(t, pub)
		first, err := f.DecoyEnrollment(seed, "mallory", like)
		if err != nil {
			t.Fatalf("%v: %v", kind, err)
		}
		again, _ := f.DecoyEnrollment(seed, "mallory", like)
		if string(first.PublicKey) != string(again.PublicKey) || first.Metadata[MetaThumbprint] != again.Metadata[MetaThumbprint] {
			t.Errorf("%v: the decoy changed between calls", kind)
		}
		if got, ok := spkiKeyKind(first.PublicKey); !ok || got != kind {
			t.Errorf("%v: decoy key kind = %v, %v", kind, got, ok)
		}
		other, _ := f.DecoyEnrollment(seed, "trent", like)
		if string(other.PublicKey) == string(first.PublicKey) {
			t.Errorf("%v: two users share a decoy key", kind)
		}
		if len(first.Metadata[MetaThumbprint]) != 64 {
			t.Errorf("%v: thumbprint is not a hex SHA-256", kind)
		}
		if _, err := f.Challenge(context.Background(), first); err != nil {
			t.Errorf("%v: the decoy enrollment does not issue a challenge: %v", kind, err)
		}
	}
}

// TestDecoyDerivationError_Message pins the typed refusal.
func TestDecoyDerivationError_Message(t *testing.T) {
	if (&DecoyDerivationError{}).Error() == "" {
		t.Fatal("DecoyDerivationError has an empty message")
	}
	if _, ok := PublicKeyKind(nil); ok {
		t.Fatal("a nil key has no kind")
	}
}
