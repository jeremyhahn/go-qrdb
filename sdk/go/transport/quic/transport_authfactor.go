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

package quic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.AuthFactorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// AuthFactorClient implementation
// QUIC authfactor frames use JSON-encoded payloads (not protobuf).
// Method names: authfactor.Enroll, authfactor.Challenge, authfactor.Verify,
//               authfactor.List, authfactor.Revoke
// ---------------------------------------------------------------------------

// quicEnrollFactorRequest is the JSON payload for authfactor.Enroll frames.
type quicEnrollFactorRequest struct {
	FactorName   string         `json:"factor_name"`
	Subject      string         `json:"subject"`
	TenantID     string         `json:"tenant_id,omitempty"`
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// quicEnrollFactorResponse is the JSON response for authfactor.Enroll frames.
type quicEnrollFactorResponse struct {
	EnrollmentID string         `json:"enrollment_id"`
	FactorName   string         `json:"factor_name"`
	UserID       string         `json:"user_id"`
	TenantID     string         `json:"tenant_id,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	EnrolledAt   string         `json:"enrolled_at"`
}

// quicChallengeFactorRequest is the JSON payload for authfactor.Challenge frames.
type quicChallengeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
}

// quicChallengeFactorResponse is the JSON response for authfactor.Challenge frames.
type quicChallengeFactorResponse struct {
	ChallengeID  string         `json:"challenge_id"`
	EnrollmentID string         `json:"enrollment_id"`
	Nonce        []byte         `json:"nonce"`
	ExpiresAt    string         `json:"expires_at"`
	MethodData   map[string]any `json:"method_data,omitempty"`
}

// quicVerifyFactorRequest is the JSON payload for authfactor.Verify frames.
type quicVerifyFactorRequest struct {
	FactorName  string            `json:"factor_name,omitempty"`
	ChallengeID string            `json:"challenge_id"`
	Value       []byte            `json:"value"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// quicListFactorsRequest is the JSON payload for authfactor.List frames.
type quicListFactorsRequest struct {
	Subject string `json:"subject,omitempty"`
}

// quicListFactorsResponse is the JSON response for authfactor.List frames.
type quicListFactorsResponse struct {
	Enrollments []quicEnrollmentEntry `json:"enrollments"`
	Total       int                   `json:"total"`
}

// quicEnrollmentEntry is a single enrollment record in a list response.
type quicEnrollmentEntry struct {
	EnrollmentID string         `json:"enrollment_id"`
	FactorName   string         `json:"factor_name"`
	UserID       string         `json:"user_id"`
	TenantID     string         `json:"tenant_id,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	EnrolledAt   string         `json:"enrolled_at"`
	RevokedAt    string         `json:"revoked_at,omitempty"`
}

// quicRevokeFactorRequest is the JSON payload for authfactor.Revoke frames.
type quicRevokeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
}

// EnrollFactor enrolls a custodian in the given auth factor type via QUIC.
func (t *Transport) EnrollFactor(ctx context.Context, req *transport.EnrollFactorRequest) (*transport.EnrollFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(quicEnrollFactorRequest{
		FactorName:   req.FactorName,
		Subject:      req.Subject,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "authfactor.Enroll", payload)
	if err != nil {
		return nil, err
	}

	var out quicEnrollFactorResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	enrolledAt, _ := time.Parse(time.RFC3339, out.EnrolledAt)
	return &transport.EnrollFactorResponse{
		EnrollmentID: out.EnrollmentID,
		FactorName:   out.FactorName,
		UserID:       out.UserID,
		TenantID:     out.TenantID,
		Metadata:     out.Metadata,
		EnrolledAt:   enrolledAt,
	}, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor via QUIC.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(quicChallengeFactorRequest{
		EnrollmentID: req.EnrollmentID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "authfactor.Challenge", payload)
	if err != nil {
		return nil, err
	}

	var out quicChallengeFactorResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	expiresAt, _ := time.Parse(time.RFC3339, out.ExpiresAt)
	return &transport.ChallengeFactorResponse{
		ChallengeID:  out.ChallengeID,
		EnrollmentID: out.EnrollmentID,
		Nonce:        out.Nonce,
		ExpiresAt:    expiresAt,
		MethodData:   out.MethodData,
	}, nil
}

// VerifyFactor verifies the custodian's challenge response via QUIC.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(quicVerifyFactorRequest{
		FactorName:  req.FactorName,
		ChallengeID: req.ChallengeID,
		Value:       req.Value,
		Metadata:    req.Metadata,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "authfactor.Verify", payload)
	return err
}

// ListFactors lists enrolled auth factors via QUIC.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var subject string
	if req != nil {
		subject = req.Subject
	}

	payload, err := json.Marshal(quicListFactorsRequest{Subject: subject})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "authfactor.List", payload)
	if err != nil {
		return nil, err
	}

	var out quicListFactorsResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	entries := make([]transport.EnrollmentEntry, len(out.Enrollments))
	for i, e := range out.Enrollments {
		entries[i] = transport.EnrollmentEntry{
			EnrollmentID: e.EnrollmentID,
			FactorName:   e.FactorName,
			UserID:       e.UserID,
			TenantID:     e.TenantID,
			Metadata:     e.Metadata,
		}
		entries[i].EnrolledAt, _ = time.Parse(time.RFC3339, e.EnrolledAt)
		if e.RevokedAt != "" {
			revokedAt, _ := time.Parse(time.RFC3339, e.RevokedAt)
			entries[i].RevokedAt = &revokedAt
		}
	}

	return &transport.ListFactorsResponse{
		Enrollments: entries,
		Total:       out.Total,
	}, nil
}

// RevokeFactor permanently revokes an enrollment via QUIC.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(quicRevokeFactorRequest{
		EnrollmentID: req.EnrollmentID,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "authfactor.Revoke", payload)
	return err
}
