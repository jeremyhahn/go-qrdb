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

// ---------------------------------------------------------------------------
// BarrierClient implementation
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the barrier with a single master key by
// calling the qrdb_barrier_initialize MCP tool.
func (t *Transport) BarrierInitialize(ctx context.Context, req *transport.BarrierInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_initialize", map[string]any{
		"secret": req.Secret,
	}); err != nil {
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
		"secret": req.Secret,
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

	text, err := t.callTool(ctx, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":       req.Secret,
		"threshold":    req.Threshold,
		"total_shares": req.TotalShares,
	})
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

// BarrierShamirListShares returns metadata about stored Shamir shares by
// calling the qrdb_barrier_shamir_list_shares MCP tool.
func (t *Transport) BarrierShamirListShares(ctx context.Context) (*transport.BarrierShamirSharesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_shamir_list_shares", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "BarrierShamirListShares", Err: err}
	}

	var resp transport.BarrierShamirSharesResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BarrierShamirListShares", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// BarrierShamirDeleteShare deletes a single Shamir share by index. Calls the
// qrdb_barrier_shamir_delete_share MCP tool.
func (t *Transport) BarrierShamirDeleteShare(ctx context.Context, req *transport.BarrierShamirDeleteShareRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_shamir_delete_share", map[string]any{
		"index": req.Index,
	}); err != nil {
		return &transport.RequestError{Method: "BarrierShamirDeleteShare", Err: err}
	}

	return nil
}

// BarrierShamirDeleteAllShares deletes all stored Shamir shares by calling
// the qrdb_barrier_shamir_delete_all_shares MCP tool.
func (t *Transport) BarrierShamirDeleteAllShares(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_shamir_delete_all_shares", nil); err != nil {
		return &transport.RequestError{Method: "BarrierShamirDeleteAllShares", Err: err}
	}

	return nil
}

// BarrierShamirVerify verifies the integrity of stored Shamir shares by
// calling the qrdb_barrier_shamir_verify MCP tool.
func (t *Transport) BarrierShamirVerify(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_barrier_shamir_verify", nil); err != nil {
		return &transport.RequestError{Method: "BarrierShamirVerify", Err: err}
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

	text, err := t.callTool(ctx, "qrdb_barrier_rekey", map[string]any{
		"threshold": req.Threshold,
		"total":     req.Total,
	})
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
func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_barrier_recovery_generate", map[string]any{
		"threshold": req.Threshold,
		"total":     req.Total,
	})
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
