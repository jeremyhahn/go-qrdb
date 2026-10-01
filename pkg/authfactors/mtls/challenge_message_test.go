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
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// TestFactor_RefusesABareNonceSignature: the answer to an mtls challenge is a
// signature over a domain-separated message, never over the bare nonce. A
// bare-nonce signature is what any other protocol that has the holder sign
// 32 server-chosen bytes would produce, so accepting it would let such a
// signature stand in for a quorum answer.
func TestFactor_RefusesABareNonceSignature(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := New()
	ch, err := f.Challenge(context.Background(), enrollPEM(t, f, selfSigned(t, key)))
	if err != nil {
		t.Fatal(err)
	}
	bare, err := SignNonce(key, ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	var failed *authfactors.VerificationFailedError
	if err := f.Verify(context.Background(), ch, &authfactors.Response{Value: bare}); !errors.As(err, &failed) {
		t.Fatalf("a bare-nonce signature answered the challenge: Verify = %v", err)
	}
}

// TestSignChallenge_BindsIDAndPurpose: an answer made with SignChallenge
// verifies for exactly the challenge and purpose it was made for; moved to
// another challenge ID or another purpose, it does not.
func TestSignChallenge_BindsIDAndPurpose(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := New()
	ch, err := f.Challenge(context.Background(), enrollPEM(t, f, selfSigned(t, key)))
	if err != nil {
		t.Fatal(err)
	}
	ch.MethodData[MethodDataPurpose] = "quorum:system:unseal:seal"
	sig, err := SignChallenge(key, ch.ChallengeID, "quorum:system:unseal:seal", ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(context.Background(), ch, &authfactors.Response{Value: sig}); err != nil {
		t.Fatalf("Verify = %v", err)
	}

	otherOp := *ch
	otherOp.MethodData = map[string]any{MethodDataPublicKey: ch.MethodData[MethodDataPublicKey], MetaThumbprint: ch.MethodData[MetaThumbprint], MethodDataPurpose: "quorum:system:unseal:rekey"}
	otherID := *ch
	otherID.ChallengeID = authfactors.NewChallengeID()
	for name, c := range map[string]*authfactors.Challenge{"another purpose": &otherOp, "another challenge": &otherID} {
		var failed *authfactors.VerificationFailedError
		if err := f.Verify(context.Background(), c, &authfactors.Response{Value: sig}); !errors.As(err, &failed) {
			t.Errorf("%s: Verify = %v, want VerificationFailedError", name, err)
		}
	}
}

// TestChallengeMessage_Layout pins the signed message and the refusal of an
// empty nonce.
func TestChallengeMessage_Layout(t *testing.T) {
	got := string(ChallengeMessage("id-1", "p", []byte("n")))
	if want := ChallengeContext + "\x00id-1\x00p\x00n"; got != want {
		t.Fatalf("ChallengeMessage = %q, want %q", got, want)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var unsupported *UnsupportedSignerError
	if _, err := SignChallenge(key, "id-1", "p", nil); !errors.As(err, &unsupported) {
		t.Fatalf("empty nonce: %v", err)
	}
}
