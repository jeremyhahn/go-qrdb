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

package clusterca

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
)

// TestCheckPublicKeyStrength_ECDSA proves a P-256 (or stronger) ECDSA key
// is accepted and an ECDSA key on a weaker curve is refused.
func TestCheckPublicKeyStrength_ECDSA(t *testing.T) {
	strong, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (P-256): %v", err)
	}
	if err := CheckPublicKeyStrength(&strong.PublicKey); err != nil {
		t.Fatalf("CheckPublicKeyStrength(P-256) = %v, want nil", err)
	}

	stronger, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (P-384): %v", err)
	}
	if err := CheckPublicKeyStrength(&stronger.PublicKey); err != nil {
		t.Fatalf("CheckPublicKeyStrength(P-384) = %v, want nil", err)
	}

	weak, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (P-224): %v", err)
	}
	var weakErr *WeakKeyError
	if err := CheckPublicKeyStrength(&weak.PublicKey); !errors.As(err, &weakErr) {
		t.Fatalf("CheckPublicKeyStrength(P-224) = %v, want *WeakKeyError", err)
	} else if weakErr.KeyType != "ECDSA/P-224" {
		t.Fatalf("WeakKeyError.KeyType = %q, want %q", weakErr.KeyType, "ECDSA/P-224")
	}
}

// TestCheckPublicKeyStrength_Ed25519 proves an Ed25519 key is always
// accepted: it has one fixed, strong size.
func TestCheckPublicKeyStrength_Ed25519(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := CheckPublicKeyStrength(pub); err != nil {
		t.Fatalf("CheckPublicKeyStrength(Ed25519) = %v, want nil", err)
	}
}

// TestCheckPublicKeyStrength_RSA proves a >=2048-bit RSA key is accepted
// and a weaker one is refused.
func TestCheckPublicKeyStrength_RSA(t *testing.T) {
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey (2048): %v", err)
	}
	if err := CheckPublicKeyStrength(&strong.PublicKey); err != nil {
		t.Fatalf("CheckPublicKeyStrength(RSA-2048) = %v, want nil", err)
	}

	weak, err := rsa.GenerateKey(rand.Reader, 1024) // #nosec G403 -- deliberately weak key to prove CheckPublicKeyStrength refuses it; the test IS the rejection path
	if err != nil {
		// crypto/rsa itself refuses to generate a sub-2048-bit key under
		// GODEBUG=fips140=only, which makes CheckPublicKeyStrength's own
		// weak-key refusal unreachable there: see
		// docs/seal/fips140-mode.md "Skipped tests under fips140=only".
		t.Skipf("cannot generate RSA-1024 in this toolchain: %v", err)
	}
	var weakErr *WeakKeyError
	if err := CheckPublicKeyStrength(&weak.PublicKey); !errors.As(err, &weakErr) {
		t.Fatalf("CheckPublicKeyStrength(RSA-1024) = %v, want *WeakKeyError", err)
	} else if weakErr.KeyType != "RSA/1024" {
		t.Fatalf("WeakKeyError.KeyType = %q, want %q", weakErr.KeyType, "RSA/1024")
	}
}

// TestCheckPublicKeyStrength_UnsupportedType proves a key type this
// package does not itself classify as ECDSA, Ed25519 or RSA is refused
// outright, rather than silently accepted.
func TestCheckPublicKeyStrength_UnsupportedType(t *testing.T) {
	var weakErr *WeakKeyError
	if err := CheckPublicKeyStrength("not a key"); !errors.As(err, &weakErr) {
		t.Fatalf("CheckPublicKeyStrength(unsupported) = %v, want *WeakKeyError", err)
	} else if weakErr.KeyType != "string" {
		t.Fatalf("WeakKeyError.KeyType = %q, want %q", weakErr.KeyType, "string")
	}
}

// TestWeakKeyError_Error proves the message names the offending key type.
func TestWeakKeyError_Error(t *testing.T) {
	err := &WeakKeyError{KeyType: "RSA/1024"}
	got := err.Error()
	if got != "clusterca: CSR public key is not strong enough for a cluster-member leaf (RSA/1024)" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestIssueLeafForKey_WeakKeyRefused proves IssueLeafForKey itself refuses
// a weak CSR key before ever touching provider's signer -- the entry
// point HandleJoinRequest calls, so a weak joiner-supplied key never
// reaches the signing step at all.
func TestIssueLeafForKey_WeakKeyRefused(t *testing.T) {
	p := newRealProvider(t)
	weak, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (P-224): %v", err)
	}
	var weakErr *WeakKeyError
	if _, err := IssueLeafForKey(context.Background(), p, &weak.PublicKey, "spiffe://qrdb.cluster/node/10.0.0.3:9000"); !errors.As(err, &weakErr) {
		t.Fatalf("IssueLeafForKey(weak key) = %v, want *WeakKeyError", err)
	}
}
