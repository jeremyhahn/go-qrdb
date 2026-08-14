// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CeremonyClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CeremonyClient implementation
// ---------------------------------------------------------------------------

// CeremonyInitialize starts the initialization ceremony with the provided
// configuration. Maps to POST /v1/ceremony/initialize.
func (t *Transport) CeremonyInitialize(ctx context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.CeremonyInitializeResponse
	if err := t.postJSONDecode(ctx, "CeremonyInitialize", "/v1/ceremony/initialize", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonyStatus returns the current ceremony state.
// Maps to GET /v1/ceremony/status.
func (t *Transport) CeremonyStatus(ctx context.Context) (*transport.CeremonyStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/ceremony/status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyStatus", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("CeremonyStatus", resp)
	}

	var statusResp transport.CeremonyStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, &transport.RequestError{Method: "CeremonyStatus", Err: &jsonDecodeError{Err: err}}
	}

	return &statusResp, nil
}

// CeremonyClaimCertBegin starts a certificate claim challenge for the named
// officer. Maps to POST /v1/ceremony/claim-cert/begin.
func (t *Transport) CeremonyClaimCertBegin(ctx context.Context, req *transport.ClaimCertBeginRequest) (*transport.ClaimCertBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.ClaimCertBeginResponse
	if err := t.postJSONDecode(ctx, "CeremonyClaimCertBegin", "/v1/ceremony/claim-cert/begin", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonyClaimCertComplete completes a certificate claim by submitting the
// signed nonce. Maps to POST /v1/ceremony/claim-cert/complete.
func (t *Transport) CeremonyClaimCertComplete(ctx context.Context, req *transport.ClaimCertCompleteRequest) (*transport.ClaimCertCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.ClaimCertCompleteResponse
	if err := t.postJSONDecode(ctx, "CeremonyClaimCertComplete", "/v1/ceremony/claim-cert/complete", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonyBeginClaimShare starts the Shamir share claim handshake for an
// officer who has already claimed their certificate. Maps to
// POST /v1/ceremony/officer-share/begin. The caller's certificate thumbprint
// is derived server-side from the mTLS peer chain.
func (t *Transport) CeremonyBeginClaimShare(ctx context.Context, req *transport.BeginClaimShareRequest) (*transport.BeginClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.BeginClaimShareResponse
	if err := t.postJSONDecode(ctx, "CeremonyBeginClaimShare", "/v1/ceremony/officer-share/begin", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonyCompleteClaimShare completes the Shamir share claim handshake by
// submitting the signed challenge nonce. Maps to
// POST /v1/ceremony/officer-share/complete.
func (t *Transport) CeremonyCompleteClaimShare(ctx context.Context, req *transport.CompleteClaimShareRequest) (*transport.CompleteClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wire := struct {
		Username  string `json:"username"`
		NonceHex  string `json:"nonce_hex"`
		Signature string `json:"signature"`
	}{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: base64.StdEncoding.EncodeToString(req.Signature),
	}

	var resp transport.CompleteClaimShareResponse
	if err := t.postJSONDecode(ctx, "CeremonyCompleteClaimShare", "/v1/ceremony/officer-share/complete", wire, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonySignCSR submits a CSR for signing during or after the ceremony.
// Maps to POST /v1/ceremony/sign-csr.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.SignCSRInitResponse
	if err := t.postJSONDecode(ctx, "CeremonySignCSR", "/v1/ceremony/sign-csr", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CeremonyEnrollExternalOfficer enrols an officer whose key material lives
// outside go-qrdb. Maps to POST /v1/init/enroll-external-officer.
func (t *Transport) CeremonyEnrollExternalOfficer(ctx context.Context, req *transport.EnrollExternalOfficerRequest) (*transport.EnrollExternalOfficerResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.EnrollExternalOfficerResponse
	if err := t.postJSONDecode(ctx, "CeremonyEnrollExternalOfficer", "/v1/init/enroll-external-officer", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
