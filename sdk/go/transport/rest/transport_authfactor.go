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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.AuthFactorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// AuthFactorClient implementation
// ---------------------------------------------------------------------------

// restEnrollFactorRequest is the wire shape pkg/api/rest's
// handleAuthFactorEnroll actually decodes: `user_id`, not the
// transport.EnrollFactorRequest.Subject field's `subject` tag.
//
// It is a package-local struct, mirroring the shape
// sdk/go/transport/quic's own quicEnrollFactorRequest already uses for the
// identical reason: transport.EnrollFactorRequest is the PUBLIC, Go-level
// request every transport's client method takes, and its field names and
// JSON tags are chosen for that surface, not for any one wire format. gRPC
// encodes it as protobuf (unaffected by JSON tags), QUIC and MCP already
// build their own wire shapes from its fields, and only this REST client
// used to marshal it directly — which is what let the tag drift from the
// REST server's body contract unnoticed.
type restEnrollFactorRequest struct {
	UserID       string         `json:"user_id"`
	TenantID     string         `json:"tenant_id,omitempty"`
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// EnrollFactor enrolls a custodian in the given auth factor type via
// POST /v1/authfactors/{factor_name}/enroll, which responds 201 Created.
func (t *Transport) EnrollFactor(ctx context.Context, req *transport.EnrollFactorRequest) (*transport.EnrollFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	path := fmt.Sprintf("/v1/authfactors/%s/enroll", req.FactorName)

	data, err := json.Marshal(restEnrollFactorRequest{
		UserID:       req.Subject,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, t.decodeErrorResponse("EnrollFactor", resp)
	}

	var result transport.EnrollFactorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &transport.RequestError{Method: "EnrollFactor", Err: err}
	}
	return &result, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor via
// POST /v1/authfactors/{enrollment_id}/challenge.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	path := fmt.Sprintf("/v1/authfactors/%s/challenge", req.EnrollmentID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ChallengeFactor", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ChallengeFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ChallengeFactor", resp)
	}

	var result transport.ChallengeFactorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &transport.RequestError{Method: "ChallengeFactor", Err: err}
	}
	return &result, nil
}

// VerifyFactor verifies the custodian's challenge response via
// POST /v1/authfactors/{challenge_id}/verify.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	path := fmt.Sprintf("/v1/authfactors/%s/verify", req.ChallengeID)

	data, err := json.Marshal(req)
	if err != nil {
		return &transport.RequestError{Method: "VerifyFactor", Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: "VerifyFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "VerifyFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return t.decodeErrorResponse("VerifyFactor", resp)
	}

	return nil
}

// ListFactors lists enrolled auth factors via GET /v1/authfactors/list.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	rawURL := t.baseURL + "/v1/authfactors/list"
	if req != nil && req.Subject != "" {
		rawURL += "?subject=" + req.Subject
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListFactors", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListFactors", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &transport.RequestError{
			Method: "ListFactors",
			Err:    &transport.UnexpectedStatusError{StatusCode: resp.StatusCode, Body: string(body)},
		}
	}

	var result transport.ListFactorsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &transport.RequestError{Method: "ListFactors", Err: err}
	}
	return &result, nil
}

// RevokeFactor permanently revokes an enrollment via
// DELETE /v1/authfactors/{enrollment_id}, which responds 204 No Content.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	path := fmt.Sprintf("/v1/authfactors/%s", req.EnrollmentID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+path, nil)
	if err != nil {
		return &transport.RequestError{Method: "RevokeFactor", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "RevokeFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return t.decodeErrorResponse("RevokeFactor", resp)
	}

	return nil
}
