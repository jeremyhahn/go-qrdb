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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// AuthFactorClient implementation
// ---------------------------------------------------------------------------

// EnrollFactor enrolls a custodian in the given auth factor type by
// delegating to the injected AuthFactorServicer.
func (t *Transport) EnrollFactor(ctx context.Context, req *transport.EnrollFactorRequest) (*transport.EnrollFactorResponse, error) {
	ctx, err := t.admit(ctx, "EnrollFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "EnrollFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.authFactorSvc == nil {
		return nil, &NilServiceError{Service: "AuthFactorServicer"}
	}

	params := authfactors.EnrollParams{
		UserID:       req.Subject,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	}
	if params.MethodParams == nil {
		params.MethodParams = make(map[string]any)
	}

	enrollment, err := t.authFactorSvc.EnrollFactor(ctx, req.FactorName, req.Subject, params)
	if err != nil {
		return nil, err
	}

	meta := make(map[string]any, len(enrollment.Metadata))
	for k, v := range enrollment.Metadata {
		meta[k] = v
	}

	return &transport.EnrollFactorResponse{
		EnrollmentID: enrollment.EnrollmentID,
		FactorName:   enrollment.FactorName,
		UserID:       enrollment.UserID,
		TenantID:     enrollment.TenantID,
		Metadata:     meta,
		EnrolledAt:   enrollment.EnrolledAt,
	}, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor by
// delegating to the injected AuthFactorServicer.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	ctx, err := t.admit(ctx, "ChallengeFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ChallengeFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.authFactorSvc == nil {
		return nil, &NilServiceError{Service: "AuthFactorServicer"}
	}

	challenge, err := t.authFactorSvc.ChallengeFactor(ctx, req.EnrollmentID)
	if err != nil {
		return nil, err
	}

	return &transport.ChallengeFactorResponse{
		ChallengeID:  challenge.ChallengeID,
		EnrollmentID: challenge.EnrollmentID,
		Nonce:        challenge.Nonce,
		ExpiresAt:    challenge.ExpiresAt,
		MethodData:   challenge.MethodData,
	}, nil
}

// VerifyFactor verifies the custodian's challenge response by delegating
// to the injected AuthFactorServicer.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	ctx, err := t.admit(ctx, "VerifyFactor")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "VerifyFactor"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.authFactorSvc == nil {
		return &NilServiceError{Service: "AuthFactorServicer"}
	}

	response := &authfactors.Response{
		Value:    req.Value,
		Metadata: req.Metadata,
	}
	if response.Metadata == nil {
		response.Metadata = make(map[string]string)
	}

	return t.authFactorSvc.VerifyFactor(ctx, req.FactorName, req.ChallengeID, response)
}

// ListFactors lists enrolled auth factors by delegating to the injected
// AuthFactorServicer.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	ctx, err := t.admit(ctx, "ListFactors")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListFactors"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.authFactorSvc == nil {
		return nil, &NilServiceError{Service: "AuthFactorServicer"}
	}

	var subject string
	if req != nil {
		subject = req.Subject
	}

	enrollments, err := t.authFactorSvc.ListFactors(ctx, subject)
	if err != nil {
		return nil, err
	}

	entries := make([]transport.EnrollmentEntry, len(enrollments))
	for i, e := range enrollments {
		meta := make(map[string]any, len(e.Metadata))
		for k, v := range e.Metadata {
			meta[k] = v
		}
		entry := transport.EnrollmentEntry{
			EnrollmentID: e.EnrollmentID,
			FactorName:   e.FactorName,
			UserID:       e.UserID,
			TenantID:     e.TenantID,
			Metadata:     meta,
			EnrolledAt:   e.EnrolledAt,
		}
		if !e.RevokedAt.IsZero() {
			t := e.RevokedAt
			entry.RevokedAt = &t
		}
		entries[i] = entry
	}

	return &transport.ListFactorsResponse{
		Enrollments: entries,
		Total:       len(entries),
	}, nil
}

// RevokeFactor permanently revokes an enrollment by delegating to the
// injected AuthFactorServicer.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	ctx, err := t.admit(ctx, "RevokeFactor")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "RevokeFactor"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.authFactorSvc == nil {
		return &NilServiceError{Service: "AuthFactorServicer"}
	}

	return t.authFactorSvc.RevokeFactor(ctx, req.EnrollmentID)
}

// ---------------------------------------------------------------------------
// Functional option
// ---------------------------------------------------------------------------

// WithAuthFactorService injects an AuthFactorServicer into the embedded transport.
func WithAuthFactorService(svc service.AuthFactorServicer) Option {
	return func(t *Transport) {
		t.authFactorSvc = svc
	}
}
