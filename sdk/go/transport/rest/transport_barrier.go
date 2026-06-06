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
	"io"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BarrierClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BarrierClient implementation
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the barrier with a single master key.
// Maps to POST /v1/barrier/initialize (204 No Content).
func (t *Transport) BarrierInitialize(ctx context.Context, req *transport.BarrierInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "BarrierInitialize", "/v1/barrier/initialize", req, nil)
}

// BarrierUnseal unseals the barrier using the master key.
// Maps to POST /v1/barrier/unseal (204 No Content).
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "BarrierUnseal", "/v1/barrier/unseal", req, nil)
}

// BarrierSeal seals the barrier, preventing further data access until
// it is unsealed again. Maps to POST /v1/barrier/seal (204 No Content).
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "BarrierSeal", "/v1/barrier/seal", nil, nil)
}

// BarrierStatus returns the current state of the cryptographic barrier.
// Maps to GET /v1/barrier/status.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/barrier/status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierStatus", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("BarrierStatus", resp)
	}

	var statusResp transport.BarrierStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, &transport.RequestError{Method: "BarrierStatus", Err: &jsonDecodeError{Err: err}}
	}

	return &statusResp, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret sharing
// and returns the generated key shares. Maps to POST /v1/barrier/shamir/initialize.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var shamirResp transport.BarrierInitializeShamirResponse
	if err := t.postJSONDecode(ctx, "BarrierInitializeShamir", "/v1/barrier/shamir/initialize", req, &shamirResp); err != nil {
		return nil, err
	}

	return &shamirResp, nil
}

// BarrierUnsealWithShare submits a single Shamir share toward the unseal
// threshold. Maps to POST /v1/barrier/shamir/unseal-share.
func (t *Transport) BarrierUnsealWithShare(ctx context.Context, req *transport.BarrierUnsealShareRequest) (*transport.BarrierUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var shareResp transport.BarrierUnsealShareResponse
	if err := t.postJSONDecode(ctx, "BarrierUnsealWithShare", "/v1/barrier/shamir/unseal-share", req, &shareResp); err != nil {
		return nil, err
	}

	return &shareResp, nil
}

// BarrierUnsealWithShares submits multiple Shamir shares to unseal the barrier
// in a single request. Maps to POST /v1/barrier/shamir/unseal-shares (204 No Content).
func (t *Transport) BarrierUnsealWithShares(ctx context.Context, req *transport.BarrierUnsealSharesRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "BarrierUnsealWithShares", "/v1/barrier/shamir/unseal-shares", req, nil)
}

// BarrierRekey generates new Shamir shares with the specified threshold and
// total count, replacing the existing shares. Maps to POST /v1/barrier/rekey.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var rekeyResp transport.BarrierRekeyResponse
	if err := t.postJSONDecode(ctx, "BarrierRekey", "/v1/barrier/rekey", req, &rekeyResp); err != nil {
		return nil, err
	}

	return &rekeyResp, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios. Maps to POST /v1/barrier/recovery/generate.
func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var keysResp transport.BarrierRecoveryKeysResponse
	if err := t.postJSONDecode(ctx, "BarrierGenerateRecoveryKeys", "/v1/barrier/recovery/generate", req, &keysResp); err != nil {
		return nil, err
	}

	return &keysResp, nil
}

// BarrierRecoverWithKeys unseals the barrier using recovery keys during
// disaster recovery. Maps to POST /v1/barrier/recovery/recover (204 No Content).
func (t *Transport) BarrierRecoverWithKeys(ctx context.Context, req *transport.BarrierRecoverWithKeysRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "BarrierRecoverWithKeys", "/v1/barrier/recovery/recover", req, nil)
}

// BarrierDeleteRecoveryKeys deletes all stored recovery keys.
// Maps to DELETE /v1/barrier/recovery.
func (t *Transport) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+"/v1/barrier/recovery", nil)
	if err != nil {
		return &transport.RequestError{Method: "BarrierDeleteRecoveryKeys", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "BarrierDeleteRecoveryKeys", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("BarrierDeleteRecoveryKeys", resp)
}

// BarrierHasRecoveryKeys checks whether recovery keys have been generated.
// Maps to GET /v1/barrier/recovery/status.
func (t *Transport) BarrierHasRecoveryKeys(ctx context.Context) (*transport.BarrierHasRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/barrier/recovery/status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierHasRecoveryKeys", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierHasRecoveryKeys", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("BarrierHasRecoveryKeys", resp)
	}

	var hasKeysResp transport.BarrierHasRecoveryKeysResponse
	if err := json.NewDecoder(resp.Body).Decode(&hasKeysResp); err != nil {
		return nil, &transport.RequestError{Method: "BarrierHasRecoveryKeys", Err: &jsonDecodeError{Err: err}}
	}

	return &hasKeysResp, nil
}

// BarrierGenerateRootToken generates a new root token using the provided
// Shamir shares. Maps to POST /v1/barrier/root-token.
func (t *Transport) BarrierGenerateRootToken(ctx context.Context, req *transport.BarrierGenerateRootTokenRequest) (*transport.BarrierRootTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var tokenResp transport.BarrierRootTokenResponse
	if err := t.postJSONDecode(ctx, "BarrierGenerateRootToken", "/v1/barrier/root-token", req, &tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}
