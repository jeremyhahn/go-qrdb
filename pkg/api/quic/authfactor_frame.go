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

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// ---------------------------------------------------------------------------
// Auth factor QUIC frame handlers
// All frames use JSON-encoded payloads serialised into protobuf bytes.
// Frame method names: authfactor.Enroll, authfactor.Challenge,
//   authfactor.Verify, authfactor.List, authfactor.Revoke
// ---------------------------------------------------------------------------

// quicEnrollFactorRequest is the JSON payload for authfactor.Enroll frames.
type quicEnrollFactorRequest struct {
	FactorName   string         `json:"factor_name"`
	Subject      string         `json:"subject"`
	TenantID     string         `json:"tenant_id,omitempty"`
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// quicChallengeFactorRequest is the JSON payload for authfactor.Challenge frames.
type quicChallengeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
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

// quicRevokeFactorRequest is the JSON payload for authfactor.Revoke frames.
type quicRevokeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
}

// handleAuthFactorEnroll handles authfactor.Enroll QUIC frames.
//
// TODO: apply permission.AuthFactorEnroll once Phase 2 ships.
func (s *Server) handleAuthFactorEnroll(ctx context.Context, payload []byte) ([]byte, error) {
	if s.authFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "authfactor"}
	}

	var req quicEnrollFactorRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.FactorName == "" || req.Subject == "" {
		return nil, &UnmarshalError{Err: errMissingField("factor_name and subject are required")}
	}

	params := authfactors.EnrollParams{
		UserID:       req.Subject,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	}
	if params.MethodParams == nil {
		params.MethodParams = make(map[string]any)
	}

	enrollment, err := s.authFactorSvc.EnrollFactor(ctx, req.FactorName, req.Subject, params)
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{
		"enrollment_id": enrollment.EnrollmentID,
		"factor_name":   enrollment.FactorName,
		"user_id":       enrollment.UserID,
		"tenant_id":     enrollment.TenantID,
		"metadata":      enrollment.Metadata,
		"enrolled_at":   enrollment.EnrolledAt.Format(time.RFC3339),
	})
}

// handleAuthFactorChallenge handles authfactor.Challenge QUIC frames.
//
// TODO: apply permission.AuthFactorChallenge once Phase 2 ships.
func (s *Server) handleAuthFactorChallenge(ctx context.Context, payload []byte) ([]byte, error) {
	if s.authFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "authfactor"}
	}

	var req quicChallengeFactorRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.EnrollmentID == "" {
		return nil, &UnmarshalError{Err: errMissingField("enrollment_id is required")}
	}

	challenge, err := s.authFactorSvc.ChallengeFactor(ctx, req.EnrollmentID)
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{
		"challenge_id":  challenge.ChallengeID,
		"enrollment_id": challenge.EnrollmentID,
		"nonce":         challenge.Nonce,
		"expires_at":    challenge.ExpiresAt.Format(time.RFC3339),
		"method_data":   challenge.MethodData,
	})
}

// handleAuthFactorVerify handles authfactor.Verify QUIC frames.
//
// TODO: apply permission.AuthFactorVerify once Phase 2 ships.
func (s *Server) handleAuthFactorVerify(ctx context.Context, payload []byte) ([]byte, error) {
	if s.authFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "authfactor"}
	}

	var req quicVerifyFactorRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.ChallengeID == "" {
		return nil, &UnmarshalError{Err: errMissingField("challenge_id is required")}
	}

	response := &authfactors.Response{
		Value:    req.Value,
		Metadata: req.Metadata,
	}
	if response.Metadata == nil {
		response.Metadata = make(map[string]string)
	}

	if err := s.authFactorSvc.VerifyFactor(ctx, req.FactorName, req.ChallengeID, response); err != nil {
		return nil, err
	}

	return json.Marshal(map[string]string{"status": "verified"})
}

// handleAuthFactorList handles authfactor.List QUIC frames.
//
// TODO: apply permission.AuthFactorList once Phase 2 ships.
func (s *Server) handleAuthFactorList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.authFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "authfactor"}
	}

	var req quicListFactorsRequest
	_ = json.Unmarshal(payload, &req) // optional; ignore error

	enrollments, err := s.authFactorSvc.ListFactors(ctx, req.Subject)
	if err != nil {
		return nil, err
	}

	entries := make([]map[string]any, len(enrollments))
	for i, e := range enrollments {
		entry := map[string]any{
			"enrollment_id": e.EnrollmentID,
			"factor_name":   e.FactorName,
			"user_id":       e.UserID,
			"tenant_id":     e.TenantID,
			"metadata":      e.Metadata,
			"enrolled_at":   e.EnrolledAt.Format(time.RFC3339),
		}
		if !e.RevokedAt.IsZero() {
			entry["revoked_at"] = e.RevokedAt.Format(time.RFC3339)
		}
		entries[i] = entry
	}

	return json.Marshal(map[string]any{
		"enrollments": entries,
		"total":       len(entries),
	})
}

// handleAuthFactorRevoke handles authfactor.Revoke QUIC frames.
//
// TODO: apply permission.AuthFactorRevoke once Phase 2 ships.
func (s *Server) handleAuthFactorRevoke(ctx context.Context, payload []byte) ([]byte, error) {
	if s.authFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "authfactor"}
	}

	var req quicRevokeFactorRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.EnrollmentID == "" {
		return nil, &UnmarshalError{Err: errMissingField("enrollment_id is required")}
	}

	if err := s.authFactorSvc.RevokeFactor(ctx, req.EnrollmentID); err != nil {
		return nil, err
	}

	return json.Marshal(map[string]string{"status": "revoked"})
}

// errMissingField wraps a missing-field message as an error type compatible with UnmarshalError.
type errMissingFieldType struct{ msg string }

func (e *errMissingFieldType) Error() string { return e.msg }

func errMissingField(msg string) error { return &errMissingFieldType{msg: msg} }
