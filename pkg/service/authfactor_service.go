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
	"encoding/json"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// sysEnrollmentPrefix is the KV namespace for persisted enrollment records.
const sysEnrollmentPrefix = "__sys:authfactor/"

// AuthFactorServicer orchestrates enrollment, challenge, and verification
// across all registered factor types (PKCS#11, PIV, FIDO2).
// Enrollment records are persisted under __sys:authfactor/<id> via the KV
// service. No secret key material is ever persisted.
type AuthFactorServicer interface {
	// EnrollFactor registers a user with a named factor.
	// method must be one of "pkcs11", "piv", "fido2".
	// Returns an Enrollment containing the public credential and metadata.
	EnrollFactor(ctx context.Context, factorName string, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)

	// ChallengeFactor issues a nonce-based challenge for the enrollment identified
	// by enrollmentID. The caller must submit a Response within the challenge TTL.
	ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)

	// VerifyFactor verifies the custodian's response against the stored challenge.
	VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error

	// ListFactors returns all active (non-revoked) enrollments for the given subject.
	// An empty subject returns all enrollments across all subjects.
	ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)

	// RevokeFactor marks the enrollment identified by enrollmentID as revoked.
	// Revoked enrollments cannot issue new challenges.
	RevokeFactor(ctx context.Context, enrollmentID string) error
}

// authFactorService is the default implementation of AuthFactorServicer.
type authFactorService struct {
	factors    map[string]authfactors.Factor // keyed by factor.Name()
	challenges map[string]*challengeEntry    // in-memory challenge cache
	kv         KVServicer
}

// challengeEntry holds a pending challenge alongside its associated enrollment.
type challengeEntry struct {
	challenge  *authfactors.Challenge
	enrollment *authfactors.Enrollment
}

// AuthFactorServiceConfig holds the dependencies for NewAuthFactorService.
type AuthFactorServiceConfig struct {
	// Factors is the list of factor implementations to register.
	// Each factor is keyed by its Name() return value.
	Factors []authfactors.Factor

	// KV is the KV service used to persist enrollment records.
	KV KVServicer
}

// NewAuthFactorService constructs an authFactorService from the provided config.
// The service is safe for concurrent use.
func NewAuthFactorService(cfg AuthFactorServiceConfig) (AuthFactorServicer, error) {
	factors := make(map[string]authfactors.Factor, len(cfg.Factors))
	for _, f := range cfg.Factors {
		factors[f.Name()] = f
	}
	return &authFactorService{
		factors:    factors,
		challenges: make(map[string]*challengeEntry),
		kv:         cfg.KV,
	}, nil
}

// EnrollFactor registers a user with the named factor and persists the
// resulting enrollment under __sys:authfactor/<id>.
func (s *authFactorService) EnrollFactor(ctx context.Context, factorName string, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	f, err := s.factor(factorName)
	if err != nil {
		return nil, err
	}

	params.UserID = subject

	enrollment, err := f.Enroll(ctx, &params)
	if err != nil {
		return nil, err
	}

	if err := s.persistEnrollment(ctx, enrollment); err != nil {
		return nil, err
	}

	return enrollment, nil
}

// ChallengeFactor loads the enrollment from the KV store and issues a challenge
// via the appropriate factor. The challenge is cached in memory until Verify or
// TTL expiry.
func (s *authFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	enrollment, err := s.loadEnrollment(ctx, enrollmentID)
	if err != nil {
		return nil, err
	}

	if !enrollment.RevokedAt.IsZero() {
		return nil, &authfactors.EnrollmentRevokedError{EnrollmentID: enrollmentID}
	}

	f, err := s.factor(enrollment.FactorName)
	if err != nil {
		return nil, err
	}

	challenge, err := f.Challenge(ctx, enrollment)
	if err != nil {
		return nil, err
	}

	// Cache for Verify.
	s.challenges[challenge.ChallengeID] = &challengeEntry{
		challenge:  challenge,
		enrollment: enrollment,
	}

	return challenge, nil
}

// VerifyFactor looks up the pending challenge by challengeID, locates the
// factor, and calls Verify.
func (s *authFactorService) VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error {
	entry, ok := s.challenges[challengeID]
	if !ok {
		return &authfactors.EnrollmentNotFoundError{EnrollmentID: challengeID}
	}

	// Clean up the challenge whether verify succeeds or fails to prevent replay.
	defer delete(s.challenges, challengeID)

	if entry.challenge.Expired() {
		return &authfactors.ChallengeExpiredError{ChallengeID: challengeID}
	}

	name := factorName
	if name == "" {
		name = entry.enrollment.FactorName
	}

	f, err := s.factor(name)
	if err != nil {
		return err
	}

	return f.Verify(ctx, entry.challenge, response)
}

// ListFactors returns all persisted enrollment records for the given subject.
// An empty subject returns all enrollments.
func (s *authFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if s.kv == nil {
		return nil, nil
	}

	keys, err := s.kv.List(ctx, sysEnrollmentPrefix)
	if err != nil {
		return nil, err
	}

	enrollments := make([]*authfactors.Enrollment, 0, len(keys))
	for _, key := range keys {
		data, err := s.kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var e authfactors.Enrollment
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		if subject != "" && e.UserID != subject {
			continue
		}
		enrollments = append(enrollments, &e)
	}

	return enrollments, nil
}

// RevokeFactor marks the enrollment as revoked (sets RevokedAt to now) and
// updates the persisted record.
func (s *authFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	enrollment, err := s.loadEnrollment(ctx, enrollmentID)
	if err != nil {
		return err
	}

	if !enrollment.RevokedAt.IsZero() {
		return &authfactors.EnrollmentRevokedError{EnrollmentID: enrollmentID}
	}

	enrollment.RevokedAt = time.Now().UTC()
	return s.persistEnrollment(ctx, enrollment)
}

// factor returns the registered factor for the given name or ErrFactorNotFound.
func (s *authFactorService) factor(name string) (authfactors.Factor, error) {
	f, ok := s.factors[strings.ToLower(name)]
	if !ok {
		return nil, &authfactors.FactorNotFoundError{Name: name}
	}
	return f, nil
}

// persistEnrollment serialises the enrollment to JSON and writes it to the KV store.
func (s *authFactorService) persistEnrollment(ctx context.Context, e *authfactors.Enrollment) error {
	if s.kv == nil {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.kv.Put(ctx, sysEnrollmentPrefix+e.EnrollmentID, data)
}

// loadEnrollment reads an enrollment from the KV store by ID.
func (s *authFactorService) loadEnrollment(ctx context.Context, enrollmentID string) (*authfactors.Enrollment, error) {
	if s.kv == nil {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: enrollmentID}
	}
	data, err := s.kv.Get(ctx, sysEnrollmentPrefix+enrollmentID)
	if err != nil {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: enrollmentID}
	}
	var e authfactors.Enrollment
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, &authfactors.EnrollmentInvalidError{
			EnrollmentID: enrollmentID,
			Reason:       "unmarshal failed: " + err.Error(),
		}
	}
	return &e, nil
}
