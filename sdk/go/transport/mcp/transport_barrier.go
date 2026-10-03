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

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BarrierClient = (*Transport)(nil)

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

	// The barrier SO PIN, when one is configured, comes from the PIN bound by
	// transport.WithSOPIN: BarrierSeal takes no request of its own.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierSeal", "")
	if err != nil {
		return &transport.RequestError{Method: "BarrierSeal", Err: err}
	}
	var args map[string]any
	if soPIN != "" {
		args = map[string]any{"so_pin": soPIN}
	}
	if _, err := t.callTool(ctx, "qrdb_barrier_seal", args); err != nil {
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

// BarrierClusterUnseal clears a cluster-wide seal of the node barrier and
// returns its cluster seal state. Calls the qrdb_barrier_cluster_unseal MCP
// tool. It unseals nothing.
func (t *Transport) BarrierClusterUnseal(ctx context.Context) (*transport.ClusterSeal, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_barrier_cluster_unseal", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierClusterUnseal", Err: err}
	}
	var out transport.ClusterSeal
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierClusterUnseal", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &out, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret sharing
// and returns the generated key shares. Calls the
// qrdb_barrier_shamir_initialize MCP tool.
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

	args := map[string]any{
		"threshold":    req.Threshold,
		"total_shares": req.TotalShares,
	}
	if req.Policy != "" {
		args["policy"] = req.Policy
	}
	if soPIN != "" {
		args["so_pin"] = soPIN
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

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a
// keyed strategy by calling the qrdb_barrier_enable_encryption MCP tool,
// which (pkg/api/mcp/tools_barrier.go) has always had a full server-side
// implementation mirroring REST/gRPC/QUIC; only this client stub forwarded
// nothing (go-qrdb#558).
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"strategy": req.Strategy,
	}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.Mechanism != "" {
		args["mechanism"] = req.Mechanism
	}
	if req.Availability != "" {
		args["availability"] = req.Availability
	}
	if len(req.Passphrase) > 0 {
		args["passphrase"] = secretArg(req.Passphrase)
	}
	if req.ShamirThreshold != 0 {
		args["shamir_threshold"] = req.ShamirThreshold
	}
	if req.ShamirTotal != 0 {
		args["shamir_total"] = req.ShamirTotal
	}

	text, err := t.callTool(ctx, "qrdb_barrier_enable_encryption", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierEnableEncryption", Err: err}
	}

	var resp transport.BarrierEnableEncryptionResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierEnableEncryption", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios by calling the qrdb_barrier_recovery_generate MCP tool.
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

	args := map[string]any{
		"threshold": req.Threshold,
	}
	if req.Policy != "" {
		args["policy"] = req.Policy
	}
	if soPIN != "" {
		args["so_pin"] = soPIN
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

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// callHolderTool maps req onto the tool's arguments through the shared
// wire.EncodeArgs, calls tool and decodes its JSON result into dest.
func (t *Transport) callHolderTool(ctx context.Context, method, tool string, req, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	var args map[string]any
	if req != nil {
		encoded, err := wire.EncodeArgs(req, wire.HolderJSONFields...)
		if err != nil {
			return &transport.RequestError{Method: method, Err: err}
		}
		args = encoded
	}
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	if err := json.Unmarshal([]byte(text), dest); err != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	return nil
}

// OfficerRegister enrols a share holder of the unseal key.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	var resp transport.OfficerRegisterResponse
	if err := t.callHolderTool(ctx, "OfficerRegister", "qrdb_barrier_officer_register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryOfficerRegister enrols a holder of the recovery key.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	var resp transport.OfficerRegisterResponse
	if err := t.callHolderTool(ctx, "RecoveryOfficerRegister", "qrdb_barrier_recovery_officer_register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// OfficerList lists the unseal key's holders and their claim state.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := t.callHolderTool(ctx, "OfficerList", "qrdb_barrier_officer_list", nil, &wire); err != nil {
		return nil, err
	}
	return wire.Officers, nil
}

// ShareClaimBegin issues a claim nonce to an unseal-key holder.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp transport.ShareClaimBeginResponse
	if err := t.callHolderTool(ctx, "ShareClaimBegin", "qrdb_barrier_share_claim_begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ShareClaimComplete delivers an unseal-key holder's sealed envelope once.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp transport.ShareClaimCompleteResponse
	if err := t.callHolderTool(ctx, "ShareClaimComplete", "qrdb_barrier_share_claim_complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp transport.ShareClaimBeginResponse
	if err := t.callHolderTool(ctx, "RecoveryShareClaimBegin", "qrdb_barrier_recovery_share_claim_begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp transport.ShareClaimCompleteResponse
	if err := t.callHolderTool(ctx, "RecoveryShareClaimComplete", "qrdb_barrier_recovery_share_claim_complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierQuorumBegin issues the factor challenge a holder answers.
func (t *Transport) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	var resp transport.QuorumChallenge
	if err := t.callHolderTool(ctx, "BarrierQuorumBegin", "qrdb_barrier_quorum_begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierQuorumSubmit submits one authenticated holder's share.
func (t *Transport) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	var resp transport.QuorumOutcome
	if err := t.callHolderTool(ctx, "BarrierQuorumSubmit", "qrdb_barrier_quorum_submit", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierRekeyStart opens a rekey round.
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
	if err := t.callHolderTool(ctx, "BarrierRekeyStart", "qrdb_barrier_rekey_start", &resolved, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey.
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
	if err := t.callHolderTool(ctx, "BarrierOfficerRevoke", "qrdb_barrier_officer_revoke", &resolved, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
