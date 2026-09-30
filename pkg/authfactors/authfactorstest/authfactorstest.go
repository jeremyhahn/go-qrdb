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

// Package authfactorstest holds a software authfactors.Factor for tests that
// drive the real auth-factor service through a transport.
//
// qrdb ships no Factor implementation (the hardware factors live in go-xkms),
// so a test that wants the real service end to end needs a factor that
// behaves like one: it enrols, issues a fresh random nonce per challenge, and
// verifies a possession proof over that nonce. Factor does exactly that with
// an HMAC key standing in for the device's private key. NewPermissive builds
// the one deliberately weak variant, whose Verify accepts any non-empty
// response: it models a factor whose check is weaker than another's, which is
// what a factor-confusion test needs to be able to exploit. It is imported by
// tests only; a production import of it is a defect.
package authfactorstest

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// challengeTTL is how long a challenge this factor issues stays answerable.
const challengeTTL = 5 * time.Minute

// Factor is a software authfactors.Factor. It is safe for concurrent use.
type Factor struct {
	name       string
	key        []byte
	permissive bool
	verifies   atomic.Int64
}

var _ authfactors.Factor = (*Factor)(nil)

// New returns a strict Factor named name: Verify accepts exactly the proof
// Respond computes for the challenge.
func New(name string) *Factor {
	return &Factor{name: name, key: random(32)}
}

// NewPermissive returns a Factor named name whose Verify accepts any
// non-empty response.
func NewPermissive(name string) *Factor {
	f := New(name)
	f.permissive = true
	return f
}

// Name returns the factor's registry name.
func (f *Factor) Name() string { return f.name }

// Enroll records an enrollment for params.UserID under this factor.
func (f *Factor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	pub := sha256.Sum256(f.key)
	return &authfactors.Enrollment{
		EnrollmentID: hex.EncodeToString(random(16)),
		UserID:       params.UserID,
		FactorName:   f.name,
		TenantID:     params.TenantID,
		PublicKey:    pub[:],
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

// Challenge issues a fresh random nonce for enrollment.
func (f *Factor) Challenge(_ context.Context, enrollment *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  hex.EncodeToString(random(16)),
		EnrollmentID: enrollment.EnrollmentID,
		Nonce:        random(32),
		ExpiresAt:    time.Now().Add(challengeTTL).UTC(),
	}, nil
}

// Verify checks response against challenge and counts the call.
func (f *Factor) Verify(_ context.Context, challenge *authfactors.Challenge, response *authfactors.Response) error {
	f.verifies.Add(1)
	if response == nil || len(response.Value) == 0 {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "empty response"}
	}
	if f.permissive {
		return nil
	}
	if !hmac.Equal(response.Value, f.proof(challenge.Nonce)) {
		return &authfactors.VerificationFailedError{FactorName: f.name, Reason: "proof does not match"}
	}
	return nil
}

// Respond returns the possession proof for nonce, as the holder of this
// factor's device would compute it.
func (f *Factor) Respond(nonce []byte) []byte { return f.proof(nonce) }

// Verifies reports how many times Verify has been called.
func (f *Factor) Verifies() int64 { return f.verifies.Load() }

func (f *Factor) proof(nonce []byte) []byte {
	m := hmac.New(sha256.New, f.key)
	m.Write(nonce)
	return m.Sum(nil)
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error.
	return b
}
