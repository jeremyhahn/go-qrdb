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

// Package factortest provides an authfactors.Factor for tests: a software
// stand-in for an assertion-based hardware factor such as FIDO2 or PIV. It is
// faithful to the Factor contract — enrolment records only public material,
// every challenge is a fresh single-purpose nonce with an expiry, and Verify
// accepts only a signature by the enrolled key over that nonce — so a test
// that passes against it exercises the same code paths a go-xkms hardware
// factor does. It is test infrastructure, like net/http/httptest: production
// composers register real factors.
package factortest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// ParamPublicKey is the enrolment parameter carrying the authenticator's
// Ed25519 public key, standard base64.
const ParamPublicKey = "public_key"

// Factor is an assertion factor over Ed25519 authenticator keys.
type Factor struct {
	name string
	ttl  time.Duration
}

// Compile-time interface check.
var _ authfactors.Factor = (*Factor)(nil)

// New returns a Factor registered under name whose challenges live for ttl
// (five minutes when ttl is not positive).
func New(name string, ttl time.Duration) *Factor {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Factor{name: name, ttl: ttl}
}

// Name returns the factor's registered name.
func (f *Factor) Name() string { return f.name }

// Enroll records the authenticator's public key from
// params.MethodParams[ParamPublicKey].
func (f *Factor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if params == nil {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "nil enroll params"}
	}
	encoded, _ := params.MethodParams[ParamPublicKey].(string)
	pub, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "public_key must be a base64 Ed25519 public key"}
	}
	return &authfactors.Enrollment{
		EnrollmentID: f.name + ":" + base64.RawURLEncoding.EncodeToString(pub[:8]),
		UserID:       params.UserID,
		FactorName:   f.name,
		TenantID:     params.TenantID,
		PublicKey:    pub,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

// Challenge issues a fresh 32-byte nonce for enrollment.
func (f *Factor) Challenge(_ context.Context, enrollment *authfactors.Enrollment) (*authfactors.Challenge, error) {
	if enrollment == nil || len(enrollment.PublicKey) != ed25519.PublicKeySize {
		return nil, &authfactors.EnrollmentInvalidError{Reason: "enrollment has no Ed25519 public key"}
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &authfactors.Challenge{
		ChallengeID:  authfactors.NewChallengeID(),
		EnrollmentID: enrollment.EnrollmentID,
		Nonce:        nonce,
		ExpiresAt:    time.Now().UTC().Add(f.ttl),
		MethodData:   map[string]any{ParamPublicKey: base64.StdEncoding.EncodeToString(enrollment.PublicKey)},
	}, nil
}

// Verify accepts only the enrolled key's Ed25519 signature over the nonce.
func (f *Factor) Verify(_ context.Context, challenge *authfactors.Challenge, response *authfactors.Response) error {
	if challenge == nil || response == nil {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "missing challenge or response"}
	}
	if challenge.Expired() {
		return &authfactors.ChallengeExpiredError{ChallengeID: challenge.ChallengeID}
	}
	encoded, _ := challenge.MethodData[ParamPublicKey].(string)
	pub, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "challenge carries no enrolled key"}
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), challenge.Nonce, response.Value) {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "assertion does not verify"}
	}
	return nil
}

// Authenticator is the holder's side of the factor: the private key an
// authenticator device would keep.
type Authenticator struct {
	key ed25519.PrivateKey
}

// NewAuthenticator generates an authenticator key.
func NewAuthenticator() (*Authenticator, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Authenticator{key: key}, nil
}

// EnrollParams returns the enrolment parameters for the authenticator.
func (a *Authenticator) EnrollParams() map[string]any {
	pub, _ := a.key.Public().(ed25519.PublicKey)
	return map[string]any{ParamPublicKey: base64.StdEncoding.EncodeToString(pub)}
}

// Assert answers a challenge nonce.
func (a *Authenticator) Assert(nonce []byte) []byte {
	return ed25519.Sign(a.key, nonce)
}
