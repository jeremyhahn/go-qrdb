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

package mcp

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
// ---------------------------------------------------------------------------

// EnrollFactor enrolls a custodian in the given auth factor type by calling
// the qrdb_authfactor_enroll MCP tool.
func (t *Transport) EnrollFactor(ctx context.Context, req *transport.EnrollFactorRequest) (*transport.EnrollFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	methodParamsJSON, _ := json.Marshal(req.MethodParams)
	text, err := t.callTool(ctx, "qrdb_authfactor_enroll", map[string]any{
		"factor_name":   req.FactorName,
		"subject":       req.Subject,
		"tenant_id":     req.TenantID,
		"method_params": string(methodParamsJSON),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: err}
	}

	var resp transport.EnrollFactorResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor by
// calling the qrdb_authfactor_challenge MCP tool.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_authfactor_challenge", map[string]any{
		"enrollment_id": req.EnrollmentID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ChallengeFactor", Err: err}
	}

	// The MCP tool returns JSON with string times; parse manually.
	var raw struct {
		ChallengeID  string         `json:"challenge_id"`
		EnrollmentID string         `json:"enrollment_id"`
		Nonce        []byte         `json:"nonce"`
		ExpiresAt    string         `json:"expires_at"`
		MethodData   map[string]any `json:"method_data"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ChallengeFactor", Err: &jsonDecodeError{Err: jsonErr}}
	}

	expiresAt, _ := time.Parse(time.RFC3339, raw.ExpiresAt)
	return &transport.ChallengeFactorResponse{
		ChallengeID:  raw.ChallengeID,
		EnrollmentID: raw.EnrollmentID,
		Nonce:        raw.Nonce,
		ExpiresAt:    expiresAt,
		MethodData:   raw.MethodData,
	}, nil
}

// VerifyFactor verifies the custodian's challenge response by calling the
// qrdb_authfactor_verify MCP tool.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	responseJSON, _ := json.Marshal(map[string]any{
		"value":    req.Value,
		"metadata": req.Metadata,
	})

	if _, err := t.callTool(ctx, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  req.ChallengeID,
		"factor_name":   req.FactorName,
		"response_json": string(responseJSON),
	}); err != nil {
		return &transport.RequestError{Method: "VerifyFactor", Err: err}
	}

	return nil
}

// ListFactors lists enrolled auth factors by calling the
// qrdb_authfactor_list MCP tool.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if req != nil && req.Subject != "" {
		args["subject"] = req.Subject
	}

	text, err := t.callTool(ctx, "qrdb_authfactor_list", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListFactors", Err: err}
	}

	// Parse the raw response which uses string times.
	var raw struct {
		Enrollments []struct {
			EnrollmentID string         `json:"enrollment_id"`
			FactorName   string         `json:"factor_name"`
			UserID       string         `json:"user_id"`
			TenantID     string         `json:"tenant_id"`
			Metadata     map[string]any `json:"metadata"`
			EnrolledAt   string         `json:"enrolled_at"`
			RevokedAt    string         `json:"revoked_at,omitempty"`
		} `json:"enrollments"`
		Total int `json:"total"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListFactors", Err: &jsonDecodeError{Err: jsonErr}}
	}

	entries := make([]transport.EnrollmentEntry, len(raw.Enrollments))
	for i, e := range raw.Enrollments {
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
		Total:       raw.Total,
	}, nil
}

// RevokeFactor permanently revokes an enrollment by calling the
// qrdb_authfactor_revoke MCP tool.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_authfactor_revoke", map[string]any{
		"enrollment_id": req.EnrollmentID,
	}); err != nil {
		return &transport.RequestError{Method: "RevokeFactor", Err: err}
	}

	return nil
}
