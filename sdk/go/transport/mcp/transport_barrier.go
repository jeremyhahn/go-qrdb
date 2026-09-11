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

package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BarrierClient = (*Transport)(nil)

// officerRegisterWireResponse mirrors the JSON shape
// pkg/api/mcp/tools_barrier.go's handleBarrierOfficerRegister /
// handleBarrierRecoveryOfficerRegister actually emit: cert_pem travels as
// the plain PEM string (string(result.CertPEM) in a map), matching the REST
// transport's identical convention (sdk/go/transport/rest/transport_barrier.go's
// officerRegisterWireResponse: "cert_pem travels as a plain PEM string for
// the other transports (byte marshaling would base64-encode it)"). Decoding
// straight into transport.OfficerRegisterResponse (whose CertPEM field is
// []byte, and therefore expects standard json base64 encoding per Go's
// encoding/json convention for byte slices) fails with "illegal base64
// data" on the very first officer registration, since the server never
// base64-encodes the PEM text. This wire-shape struct plus the manual
// []byte(...) conversion below restores parity with REST's handling.
type officerRegisterWireResponse struct {
	CertPEM    string `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

// ---------------------------------------------------------------------------
// BarrierClient implementation
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the barrier with a single master key by
// calling the qrdb_barrier_initialize MCP tool.
func (t *Transport) BarrierInitialize(ctx context.Context, req *transport.BarrierInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"secret": secretArg(req.Secret),
	}
	if req.SOPin != "" {
		args["so_pin"] = req.SOPin
	}
	if req.UserPin != "" {
		args["user_pin"] = req.UserPin
	}
	if _, err := t.callTool(ctx, "qrdb_barrier_initialize", args); err != nil {
		return &transport.RequestError{Method: "BarrierInitialize", Err: err}
	}

	return nil
}

// BarrierUnseal unseals the barrier using the master key by calling the
// qrdb_barrier_unseal MCP tool.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_unseal", map[string]any{
		"secret": secretArg(req.Secret),
	}); err != nil {
		return &transport.RequestError{Method: "BarrierUnseal", Err: err}
	}

	return nil
}

// BarrierSeal seals the barrier, preventing further data access until it is
// unsealed again. Calls the qrdb_barrier_seal MCP tool.
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_seal", nil); err != nil {
		return &transport.RequestError{Method: "BarrierSeal", Err: err}
	}

	return nil
}

// BarrierStatus returns the current state of the cryptographic barrier by
// calling the qrdb_barrier_status MCP tool.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierStatus", Err: err}
	}

	var resp transport.BarrierStatusResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret sharing
// and returns the generated key shares. Calls the
// qrdb_barrier_shamir_initialize MCP tool.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"secret":       secretArg(req.Secret),
		"threshold":    req.Threshold,
		"total_shares": req.TotalShares,
	}
	if req.SOPin != "" {
		args["so_pin"] = req.SOPin
	}
	if req.UserPin != "" {
		args["user_pin"] = req.UserPin
	}
	text, err := t.callTool(ctx, "qrdb_barrier_shamir_initialize", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierInitializeShamir", Err: err}
	}

	var resp transport.BarrierInitializeShamirResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierInitializeShamir", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierUnsealWithShare submits a single Shamir share toward the unseal
// threshold. Calls the qrdb_barrier_shamir_unseal_share MCP tool.
func (t *Transport) BarrierUnsealWithShare(ctx context.Context, req *transport.BarrierUnsealShareRequest) (*transport.BarrierUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_shamir_unseal_share", map[string]any{
		"share": req.Share,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierUnsealWithShare", Err: err}
	}

	var resp transport.BarrierUnsealShareResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierUnsealWithShare", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierUnsealWithShares submits multiple Shamir shares to unseal the
// barrier in a single request. Calls the qrdb_barrier_shamir_unseal_shares
// MCP tool with a comma-separated share list.
func (t *Transport) BarrierUnsealWithShares(ctx context.Context, req *transport.BarrierUnsealSharesRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_shamir_unseal_shares", map[string]any{
		"shares": strings.Join(req.Shares, ","),
	}); err != nil {
		return &transport.RequestError{Method: "BarrierUnsealWithShares", Err: err}
	}

	return nil
}

// BarrierRekey generates new Shamir shares with the specified threshold and
// total count, replacing the existing shares. Calls the qrdb_barrier_rekey
// MCP tool.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"threshold": req.Threshold,
		"total":     req.Total,
	}
	if req.SOPin != "" {
		args["so_pin"] = req.SOPin
	}
	text, err := t.callTool(ctx, "qrdb_barrier_rekey", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierRekey", Err: err}
	}

	var resp transport.BarrierRekeyResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierRekey", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios by calling the qrdb_barrier_recovery_generate MCP tool.
// BarrierEnableEncryption is not supported on the mcp transport; enable
// encryption via the REST or gRPC (unix) transports.
func (t *Transport) BarrierEnableEncryption(_ context.Context, _ *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	return nil, &transport.NotImplementedError{Method: "BarrierEnableEncryption"}
}

func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"threshold": req.Threshold,
		"total":     req.Total,
	}
	if req.SOPin != "" {
		args["so_pin"] = req.SOPin
	}
	text, err := t.callTool(ctx, "qrdb_barrier_recovery_generate", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierGenerateRecoveryKeys", Err: err}
	}

	var resp transport.BarrierRecoveryKeysResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierGenerateRecoveryKeys", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierRecoverWithKeys unseals the barrier using recovery keys. Calls the
// qrdb_barrier_recovery_recover MCP tool with a comma-separated key list.
func (t *Transport) BarrierRecoverWithKeys(ctx context.Context, req *transport.BarrierRecoverWithKeysRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_recovery_recover", map[string]any{
		"keys": strings.Join(req.Keys, ","),
	}); err != nil {
		return &transport.RequestError{Method: "BarrierRecoverWithKeys", Err: err}
	}

	return nil
}

// BarrierDeleteRecoveryKeys deletes all stored recovery keys by calling the
// qrdb_barrier_recovery_delete MCP tool.
func (t *Transport) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_recovery_delete", nil); err != nil {
		return &transport.RequestError{Method: "BarrierDeleteRecoveryKeys", Err: err}
	}

	return nil
}

// BarrierHasRecoveryKeys checks whether recovery keys have been generated by
// calling the qrdb_barrier_recovery_status MCP tool. The server returns a
// has_recovery_keys field which is mapped to the transport response type.
func (t *Transport) BarrierHasRecoveryKeys(ctx context.Context) (*transport.BarrierHasRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_recovery_status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierHasRecoveryKeys", Err: err}
	}

	var wire struct {
		HasRecoveryKeys bool `json:"has_recovery_keys"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierHasRecoveryKeys", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.BarrierHasRecoveryKeysResponse{
		HasKeys: wire.HasRecoveryKeys,
	}, nil
}

// BarrierGenerateRootToken generates a new root token using the provided
// Shamir shares by calling the qrdb_barrier_root_token MCP tool with a
// comma-separated share list.
func (t *Transport) BarrierGenerateRootToken(ctx context.Context, req *transport.BarrierGenerateRootTokenRequest) (*transport.BarrierRootTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_root_token", map[string]any{
		"shares": strings.Join(req.Shares, ","),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierGenerateRootToken", Err: err}
	}

	var resp transport.BarrierRootTokenResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierGenerateRootToken", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// OfficerRegister registers a Security Officer for the system barrier by
// calling the qrdb_barrier_officer_register MCP tool.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"username": req.Username,
		"csr_pem":  string(req.CSRPEM),
	}
	if req.AuthenticatorType != "" {
		args["authenticator_type"] = req.AuthenticatorType
	}
	if req.AuthenticatorRef != "" {
		args["authenticator_ref"] = req.AuthenticatorRef
	}

	text, err := t.callTool(ctx, "qrdb_barrier_officer_register", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "OfficerRegister", Err: err}
	}

	var wireResp officerRegisterWireResponse
	if jsonErr := json.Unmarshal([]byte(text), &wireResp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "OfficerRegister", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.OfficerRegisterResponse{
		CertPEM:    []byte(wireResp.CertPEM),
		ShareIndex: wireResp.ShareIndex,
	}, nil
}

// OfficerList returns all enrolled Security Officers for the system barrier by
// calling the qrdb_barrier_officer_list MCP tool.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_officer_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "OfficerList", Err: err}
	}

	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "OfficerList", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return wire.Officers, nil
}

// OfficerClaimStatus returns a per-officer claim status snapshot for the
// system barrier by calling the qrdb_barrier_officer_claim_status MCP tool.
func (t *Transport) OfficerClaimStatus(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_officer_claim_status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "OfficerClaimStatus", Err: err}
	}

	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "OfficerClaimStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return wire.Officers, nil
}

// ShareClaimBegin issues a challenge nonce to the named Security Officer for
// the system barrier share claim handshake by calling the
// qrdb_barrier_share_claim_begin MCP tool.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_share_claim_begin", map[string]any{
		"username": req.Username,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ShareClaimBegin", Err: err}
	}

	var resp transport.ShareClaimBeginResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ShareClaimBegin", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// ShareClaimComplete verifies the Security Officer's nonce signature and
// returns the system barrier share bytes exactly once by calling the
// qrdb_barrier_share_claim_complete MCP tool.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  req.Username,
		"nonce_hex": req.NonceHex,
		"signature": req.Signature,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ShareClaimComplete", Err: err}
	}

	var resp transport.ShareClaimCompleteResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ShareClaimComplete", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// ShamirSealVote submits a Shamir share as a seal vote for the system barrier
// by calling the qrdb_barrier_shamir_seal_vote MCP tool.
func (t *Transport) ShamirSealVote(ctx context.Context, req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_shamir_seal_vote", map[string]any{
		"share": req.Share,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ShamirSealVote", Err: err}
	}

	var resp transport.ShamirSealVoteResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ShamirSealVote", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// RecoveryOfficerRegister registers a Security Officer for recovery key
// distribution by calling the qrdb_barrier_recovery_officer_register MCP tool.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"username": req.Username,
		"csr_pem":  string(req.CSRPEM),
	}
	if req.AuthenticatorType != "" {
		args["authenticator_type"] = req.AuthenticatorType
	}
	if req.AuthenticatorRef != "" {
		args["authenticator_ref"] = req.AuthenticatorRef
	}

	text, err := t.callTool(ctx, "qrdb_barrier_recovery_officer_register", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RecoveryOfficerRegister", Err: err}
	}

	var wireResp officerRegisterWireResponse
	if jsonErr := json.Unmarshal([]byte(text), &wireResp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RecoveryOfficerRegister", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.OfficerRegisterResponse{
		CertPEM:    []byte(wireResp.CertPEM),
		ShareIndex: wireResp.ShareIndex,
	}, nil
}

// RecoveryShareClaimBegin issues a challenge nonce to the named recovery
// officer for the recovery share claim handshake by calling the
// qrdb_barrier_recovery_share_claim_begin MCP tool.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_recovery_share_claim_begin", map[string]any{
		"username": req.Username,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RecoveryShareClaimBegin", Err: err}
	}

	var resp transport.ShareClaimBeginResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RecoveryShareClaimBegin", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// RecoveryShareClaimComplete verifies the recovery officer's nonce signature
// and returns the recovery share bytes exactly once by calling the
// qrdb_barrier_recovery_share_claim_complete MCP tool.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_recovery_share_claim_complete", map[string]any{
		"username":  req.Username,
		"nonce_hex": req.NonceHex,
		"signature": req.Signature,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RecoveryShareClaimComplete", Err: err}
	}

	var resp transport.ShareClaimCompleteResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RecoveryShareClaimComplete", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}
