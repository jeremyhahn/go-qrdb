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

package mtls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// selfSigned returns a self-signed certificate over signer's public key.
func selfSigned(t *testing.T, signer crypto.Signer) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "holder"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// answer produces the signature the factor expects from key for ch: the raw
// signature format over ChallengeMessage, built here by hand so the test does
// not lean on SignChallenge.
func answer(t *testing.T, key crypto.Signer, ch *authfactors.Challenge) []byte {
	t.Helper()
	purpose, _ := ch.MethodData[MethodDataPurpose].(string)
	msg := ChallengeMessage(ch.ChallengeID, purpose, ch.Nonce)
	if _, ok := key.(ed25519.PrivateKey); ok {
		sig, err := key.Sign(rand.Reader, msg, crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	digest := sha256.Sum256(msg)
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func enrollPEM(t *testing.T, f *Factor, cert *x509.Certificate) *authfactors.Enrollment {
	t.Helper()
	p := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	e, err := f.Enroll(context.Background(), &authfactors.EnrollParams{
		UserID:       "alice",
		MethodParams: map[string]any{ParamCertificatePEM: string(p)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestFactor_ProvesPossessionForEveryKeyType is the positive path for each
// certificate key type a holder may enrol.
func TestFactor_ProvesPossessionForEveryKeyType(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	for name, key := range map[string]crypto.Signer{"ecdsa": ec, "rsa": rk, "ed25519": ed} {
		t.Run(name, func(t *testing.T) {
			f := New()
			cert := selfSigned(t, key)
			e := enrollPEM(t, f, cert)
			if e.FactorName != Name || e.Metadata[MetaThumbprint] != Thumbprint(cert.Raw) || e.UserID != "alice" {
				t.Fatalf("enrollment = %+v", e)
			}
			ch, err := f.Challenge(context.Background(), e)
			if err != nil {
				t.Fatal(err)
			}
			resp := &authfactors.Response{Value: answer(t, key, ch)}
			if err := f.Verify(context.Background(), ch, resp); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

// TestFactor_RefusesAnotherKeysSignature is the core refusal: a signature by
// any key other than the enrolled one does not verify.
func TestFactor_RefusesAnotherKeysSignature(t *testing.T) {
	enrolled, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := New()
	e := enrollPEM(t, f, selfSigned(t, enrolled))
	ch, _ := f.Challenge(context.Background(), e)
	err := f.Verify(context.Background(), ch, &authfactors.Response{Value: answer(t, other, ch)})
	var failed *authfactors.VerificationFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Verify = %v, want VerificationFailedError", err)
	}
}

// TestFactor_BindsTheTLSPeerCertificate pins the peer binding: a valid
// signature presented over a TLS session authenticated by a DIFFERENT
// certificate is refused, and the enrolled certificate's own session passes.
func TestFactor_BindsTheTLSPeerCertificate(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := New()
	cert := selfSigned(t, key)
	e := enrollPEM(t, f, cert)
	ch, _ := f.Challenge(context.Background(), e)
	sig := answer(t, key, ch)

	wrong := &authfactors.Response{Value: sig, Metadata: map[string]string{
		authfactors.ResponseMetaPeerCertThumbprint: Thumbprint([]byte("some other certificate")),
	}}
	var failed *authfactors.VerificationFailedError
	if err := f.Verify(context.Background(), ch, wrong); !errors.As(err, &failed) {
		t.Fatalf("Verify over a foreign TLS session = %v, want refusal", err)
	}

	right := &authfactors.Response{Value: sig, Metadata: map[string]string{
		authfactors.ResponseMetaPeerCertThumbprint: Thumbprint(cert.Raw),
	}}
	if err := f.Verify(context.Background(), ch, right); err != nil {
		t.Fatalf("Verify over the enrolled certificate's session: %v", err)
	}
}

// TestFactor_RefusesExpiredAndMalformedChallenges covers the other refusals.
func TestFactor_RefusesExpiredAndMalformedChallenges(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &Factor{TTL: time.Minute}
	e := enrollPEM(t, f, selfSigned(t, key))
	ch, _ := f.Challenge(context.Background(), e)
	if ch.ExpiresAt.Sub(time.Now()) > time.Minute+time.Second {
		t.Fatalf("TTL override ignored: %v", ch.ExpiresAt)
	}
	sig := answer(t, key, ch)

	expired := *ch
	expired.ExpiresAt = time.Now().Add(-time.Second)
	var exp *authfactors.ChallengeExpiredError
	if err := f.Verify(context.Background(), &expired, &authfactors.Response{Value: sig}); !errors.As(err, &exp) {
		t.Fatalf("expired = %v", err)
	}

	var failed *authfactors.VerificationFailedError
	for name, md := range map[string]map[string]any{
		"no key":      {MetaThumbprint: "x"},
		"bad base64":  {MetaThumbprint: "x", MethodDataPublicKey: "***"},
		"bad der":     {MetaThumbprint: "x", MethodDataPublicKey: "AAAA"},
		"no thumb":    {MethodDataPublicKey: ch.MethodData[MethodDataPublicKey]},
		"empty chall": {},
	} {
		c := *ch
		c.MethodData = md
		if err := f.Verify(context.Background(), &c, &authfactors.Response{Value: sig}); !errors.As(err, &failed) {
			t.Fatalf("%s: Verify = %v", name, err)
		}
	}
	if err := f.Verify(context.Background(), nil, &authfactors.Response{}); !errors.As(err, &failed) {
		t.Fatalf("nil challenge = %v", err)
	}
	if err := f.Verify(context.Background(), ch, nil); !errors.As(err, &failed) {
		t.Fatalf("nil response = %v", err)
	}
}

// TestFactor_EnrollAndChallengeRefuseBadInput covers enrolment validation.
func TestFactor_EnrollAndChallengeRefuseBadInput(t *testing.T) {
	f := New()
	var invalid *authfactors.EnrollmentInvalidError
	for name, params := range map[string]*authfactors.EnrollParams{
		"nil":       nil,
		"no cert":   {MethodParams: map[string]any{}},
		"not pem":   {MethodParams: map[string]any{ParamCertificatePEM: "junk"}},
		"wrong pem": {MethodParams: map[string]any{ParamCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}))}},
		"key pem":   {MethodParams: map[string]any{ParamCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte{1}}))}},
	} {
		if _, err := f.Enroll(context.Background(), params); !errors.As(err, &invalid) {
			t.Fatalf("%s: Enroll = %v", name, err)
		}
	}
	for name, e := range map[string]*authfactors.Enrollment{
		"nil":     nil,
		"no key":  {Metadata: map[string]string{MetaThumbprint: "x"}},
		"no meta": {PublicKey: []byte{1}},
		"bad key": {PublicKey: []byte{1}, Metadata: map[string]string{MetaThumbprint: "x"}},
	} {
		if _, err := f.Challenge(context.Background(), e); !errors.As(err, &invalid) {
			t.Fatalf("%s: Challenge = %v", name, err)
		}
	}
	if f.Name() != "mtls" {
		t.Fatal(f.Name())
	}
}

// TestVerifyNonceSignature_RefusesEmptyAndUnknown pins the primitive's
// refusals.
func TestVerifyNonceSignature_RefusesEmptyAndUnknown(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if VerifyNonceSignature(&key.PublicKey, nil, []byte{1}) || VerifyNonceSignature(&key.PublicKey, []byte{1}, nil) {
		t.Fatal("empty input verified")
	}
	if VerifyNonceSignature("not a key", []byte{1}, []byte{1}) {
		t.Fatal("unknown key type verified")
	}
}
