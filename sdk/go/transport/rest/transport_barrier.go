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
	"net/url"

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

	// The barrier SO PIN, when one is configured, comes from the PIN bound by
	// transport.WithSOPIN: BarrierSeal takes no request of its own.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierSeal", "")
	if err != nil {
		return err
	}
	var body any
	if soPIN != "" {
		body = map[string]string{"so_pin": soPIN}
	}
	return t.postJSONDecode(ctx, "BarrierSeal", "/v1/barrier/seal", body, nil)
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

// BarrierInitializeShamir initializes the barrier under Shamir custody and
// distributes one sealed envelope to each enrolled unseal-key holder.
// Maps to POST /v1/barrier/shamir/initialize.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierInitializeShamir", req.SOPin)
	if err != nil {
		return nil, err
	}
	body := *req
	body.SOPin = soPIN

	var shamirResp transport.BarrierInitializeShamirResponse
	if err := t.postJSONDecode(ctx, "BarrierInitializeShamir", "/v1/barrier/shamir/initialize", &body, &shamirResp); err != nil {
		return nil, err
	}

	return &shamirResp, nil
}

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy. Maps to POST /v1/barrier/enable-encryption.
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.BarrierEnableEncryptionResponse
	if err := t.postJSONDecode(ctx, "BarrierEnableEncryption", "/v1/barrier/enable-encryption", req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios. Maps to POST /v1/barrier/recovery/generate.
func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierGenerateRecoveryKeys", req.SOPin)
	if err != nil {
		return nil, err
	}
	body := *req
	body.SOPin = soPIN

	var keysResp transport.BarrierRecoveryKeysResponse
	if err := t.postJSONDecode(ctx, "BarrierGenerateRecoveryKeys", "/v1/barrier/recovery/generate", &body, &keysResp); err != nil {
		return nil, err
	}

	return &keysResp, nil
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

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// holderPost posts payload to path and decodes the reply into dest.
func (t *Transport) holderPost(ctx context.Context, method, path string, payload, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.postJSONDecode(ctx, method, path, payload, dest)
}

// OfficerRegister enrols a share holder of the unseal key.
// Maps to POST /v1/barrier/officers.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	var resp transport.OfficerRegisterResponse
	if err := t.holderPost(ctx, "OfficerRegister", "/v1/barrier/officers", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryOfficerRegister enrols a holder of the recovery key.
// Maps to POST /v1/barrier/recovery/officers.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	var resp transport.OfficerRegisterResponse
	if err := t.holderPost(ctx, "RecoveryOfficerRegister", "/v1/barrier/recovery/officers", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// OfficerList lists the unseal key's holders. Maps to GET /v1/barrier/officers.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := t.getJSONDecode(ctx, "OfficerList", "/v1/barrier/officers", &wire); err != nil {
		return nil, err
	}
	return wire.Officers, nil
}

// ShareClaimBegin issues a claim nonce to an unseal-key holder.
// Maps to POST /v1/barrier/officers/{username}/claim/begin.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp transport.ShareClaimBeginResponse
	if err := t.holderPost(ctx, "ShareClaimBegin", "/v1/barrier/officers/"+url.PathEscape(req.Username)+"/claim/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ShareClaimComplete delivers an unseal-key holder's sealed envelope once.
// Maps to POST /v1/barrier/officers/{username}/claim/complete.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp transport.ShareClaimCompleteResponse
	if err := t.holderPost(ctx, "ShareClaimComplete", "/v1/barrier/officers/"+url.PathEscape(req.Username)+"/claim/complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
// Maps to POST /v1/barrier/recovery/officers/{username}/claim/begin.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp transport.ShareClaimBeginResponse
	if err := t.holderPost(ctx, "RecoveryShareClaimBegin", "/v1/barrier/recovery/officers/"+url.PathEscape(req.Username)+"/claim/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
// Maps to POST /v1/barrier/recovery/officers/{username}/claim/complete.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp transport.ShareClaimCompleteResponse
	if err := t.holderPost(ctx, "RecoveryShareClaimComplete", "/v1/barrier/recovery/officers/"+url.PathEscape(req.Username)+"/claim/complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierQuorumBegin issues the factor challenge a holder answers.
// Maps to POST /v1/barrier/quorum/begin.
func (t *Transport) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	var resp transport.QuorumChallenge
	if err := t.holderPost(ctx, "BarrierQuorumBegin", "/v1/barrier/quorum/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierQuorumSubmit submits one authenticated holder's share.
// Maps to POST /v1/barrier/quorum/submit.
func (t *Transport) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	var resp transport.QuorumOutcome
	if err := t.holderPost(ctx, "BarrierQuorumSubmit", "/v1/barrier/quorum/submit", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierRekeyStart opens a rekey round. Maps to POST /v1/barrier/rekey/start.
func (t *Transport) BarrierRekeyStart(ctx context.Context, req *transport.RekeyStartRequest) (*transport.QuorumRound, error) {
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierRekeyStart", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	var resp transport.QuorumRound
	if err := t.holderPost(ctx, "BarrierRekeyStart", "/v1/barrier/rekey/start", &resolved, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey.
// Maps to POST /v1/barrier/officers/revoke.
func (t *Transport) BarrierOfficerRevoke(ctx context.Context, req *transport.OfficerRevokeRequest) (*transport.QuorumRound, error) {
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierOfficerRevoke", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	var resp transport.QuorumRound
	if err := t.holderPost(ctx, "BarrierOfficerRevoke", "/v1/barrier/officers/revoke", &resolved, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
