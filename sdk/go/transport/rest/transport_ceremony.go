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
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonyInitialize", req.SOPin)
	if err != nil {
		return nil, err
	}
	body := *req
	body.SOPin = soPIN

	var resp transport.CeremonyInitializeResponse
	if err := t.postJSONDecode(ctx, "CeremonyInitialize", "/v1/ceremony/initialize", &body, &resp); err != nil {
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

// CeremonySignCSR submits a CSR for signing during or after the ceremony.
// Maps to POST /v1/ceremony/sign-csr.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonySignCSR", req.SOPin)
	if err != nil {
		return nil, err
	}
	body := *req
	body.SOPin = soPIN

	var resp transport.SignCSRInitResponse
	if err := t.postJSONDecode(ctx, "CeremonySignCSR", "/v1/ceremony/sign-csr", &body, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
