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

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// signingMockFactor is an in-package, real-ECDSA Factor implementation used
// only by authfactor_service_test.go to exercise the AuthFactorService
// without depending on a deleted go-qrdb hardware-factor subpackage.
//
// Per the Phase-1 architecture decision (qrdb owns the Factor interface;
// hardware impls live exclusively in go-xkms), the qrdb service tests must
// not import qrdb-internal PIV/FIDO2/PKCS#11 packages. The xkms canonical
// adapters are tested in xkms/pkg/factor/{...}/factor_test.go end-to-end;
// here we only need a minimal Factor that does real ECDSA so the service's
// routing + persistence + replay-prevention logic can be verified.
//
// The mock advertises Name() == "piv" purely so the existing test
// assertions ("factor name = piv") keep their semantics; it has no
// dependency on a real PIV card.
type signingMockFactor struct {
	name      string
	key       *ecdsa.PrivateKey
	certPEM   []byte
	ttl       int
	defaultCN string
}

const (
	signingMockChallengeTTLSeconds = 300
	signingMockNonceLen            = 32
	signingMockValidityDays        = 365
)

// newSigningMockFactor returns a Factor that generates ECDSA-P256 enrollments,
// issues nonce challenges, and verifies DER-encoded ECDSA signatures over
// the challenge nonce. The factor's Name() is the supplied name (typically
// "piv" so test assertions match).
func newSigningMockFactor(t *testing.T, name string) *signingMockFactor {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	cn := "Test " + name + " Subject"
	cert, err := buildMockSelfSignedCert(key, pkix.Name{CommonName: cn}, signingMockValidityDays)
	if err != nil {
		t.Fatalf("buildMockSelfSignedCert: %v", err)
	}
	return &signingMockFactor{
		name:      name,
		key:       key,
		certPEM:   cert,
		ttl:       signingMockChallengeTTLSeconds,
		defaultCN: cn,
	}
}

func (f *signingMockFactor) Name() string { return f.name }

func (f *signingMockFactor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if params == nil {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "params must not be nil"}
	}
	return &authfactors.Enrollment{
		EnrollmentID: authfactors.NewEnrollmentID(),
		UserID:       params.UserID,
		FactorName:   f.name,
		TenantID:     params.TenantID,
		PublicKey:    f.certPEM,
		Metadata: map[string]string{
			"slot":      "9a",
			"algorithm": "ecdsap256",
			"cn":        f.defaultCN,
			"ttl":       strconv.Itoa(f.ttl),
		},
		EnrolledAt: time.Now().UTC(),
	}, nil
}

func (f *signingMockFactor) Challenge(_ context.Context, enrollment *authfactors.Enrollment) (*authfactors.Challenge, error) {
	if enrollment == nil {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "enrollment must not be nil"}
	}
	nonce := make([]byte, signingMockNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, &authfactors.EnrollmentInvalidError{
			EnrollmentID: enrollment.EnrollmentID,
			Reason:       "mock: rand.Read nonce: " + err.Error(),
		}
	}
	return &authfactors.Challenge{
		ChallengeID:  authfactors.NewChallengeID(),
		EnrollmentID: enrollment.EnrollmentID,
		Nonce:        nonce,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(f.ttl) * time.Second),
		MethodData: map[string]any{
			"slot":      enrollment.Metadata["slot"],
			"algorithm": enrollment.Metadata["algorithm"],
		},
	}, nil
}

func (f *signingMockFactor) Verify(_ context.Context, challenge *authfactors.Challenge, response *authfactors.Response) error {
	if challenge == nil {
		return &authfactors.EnrollmentInvalidError{Reason: "challenge must not be nil"}
	}
	if response == nil {
		return &authfactors.EnrollmentInvalidError{Reason: "response must not be nil"}
	}
	if challenge.Expired() {
		return &authfactors.ChallengeExpiredError{ChallengeID: challenge.ChallengeID}
	}
	pubPEM, ok := response.Metadata["public_key_pem"]
	if !ok {
		return &authfactors.VerificationFailedError{
			FactorName: f.name,
			Reason:     "public_key_pem missing from response metadata",
		}
	}
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "decode cert PEM"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "parse cert: " + err.Error()}
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "cert public key is not ECDSA"}
	}
	digest := sha256.Sum256(challenge.Nonce)
	if !ecdsa.VerifyASN1(pub, digest[:], response.Value) {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "ECDSA signature verify failed"}
	}
	return nil
}

// SignNonce signs a nonce with the mock factor's private key. Tests call
// this to produce a Response.Value that Verify will accept.
func (f *signingMockFactor) SignNonce(_ context.Context, _ string, nonce []byte) ([]byte, error) {
	digest := sha256.Sum256(nonce)
	return ecdsa.SignASN1(rand.Reader, f.key, digest[:])
}

// buildMockSelfSignedCert builds a minimal self-signed cert PEM for the
// supplied ECDSA key. Lifted from the previously-existing
// pkg/authfactors/piv/piv.go pattern but kept local to avoid the dependency.
func buildMockSelfSignedCert(key *ecdsa.PrivateKey, subject pkix.Name, validityDays int) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    time.Now().UTC(),
		NotAfter:     time.Now().UTC().AddDate(0, 0, validityDays),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
