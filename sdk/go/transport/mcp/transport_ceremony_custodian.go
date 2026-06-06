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
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyClaimShareBegin issues a factor challenge via the
// qrdb_ceremony_claim_share_begin tool.
func (t *Transport) CeremonyClaimShareBegin(ctx context.Context, req *transport.CeremonyClaimShareBeginRequest) (*transport.CeremonyClaimShareBeginResponse, error) {
	text, err := t.callTool(ctx, "qrdb_ceremony_claim_share_begin", map[string]any{
		"custodian_id": req.CustodianID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareBegin", Err: err}
	}
	var wire struct {
		CustodianID string `json:"custodian_id"`
		Nonce       string `json:"nonce"`
		ExpiresAt   string `json:"expires_at"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareBegin", Err: &jsonDecodeError{Err: jsonErr}}
	}
	nonce, _ := base64.StdEncoding.DecodeString(wire.Nonce)
	resp := &transport.CeremonyClaimShareBeginResponse{CustodianID: wire.CustodianID, Nonce: nonce}
	if wire.ExpiresAt != "" {
		if ts, perr := time.Parse(time.RFC3339, wire.ExpiresAt); perr == nil {
			resp.ExpiresAt = ts
		}
	}
	return resp, nil
}

// CeremonyClaimShareComplete verifies the factor response via the
// qrdb_ceremony_claim_share_complete tool.
func (t *Transport) CeremonyClaimShareComplete(ctx context.Context, req *transport.CeremonyClaimShareCompleteRequest) (*transport.CeremonyCustodianInfo, error) {
	args := map[string]any{
		"custodian_id": req.CustodianID,
		"challenge_id": req.ChallengeID,
	}
	if len(req.FactorResponse) > 0 {
		args["factor_response"] = base64.StdEncoding.EncodeToString(req.FactorResponse)
	}
	text, err := t.callTool(ctx, "qrdb_ceremony_claim_share_complete", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareComplete", Err: err}
	}
	// The tool marshals the custodian record with its own (camelCase) json
	// tags, which match CeremonyCustodianInfo.
	var info transport.CeremonyCustodianInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareComplete", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &info, nil
}

// CeremonySubmitUnsealShare submits a share via the
// qrdb_ceremony_submit_unseal_share tool.
func (t *Transport) CeremonySubmitUnsealShare(ctx context.Context, req *transport.CeremonySubmitUnsealShareRequest) (*transport.CeremonySubmitUnsealShareResponse, error) {
	args := map[string]any{
		"custodian_id": req.CustodianID,
		"challenge_id": req.ChallengeID,
		"share":        base64.StdEncoding.EncodeToString(req.ShareBytes),
	}
	if len(req.FactorResponse) > 0 {
		args["factor_response"] = base64.StdEncoding.EncodeToString(req.FactorResponse)
	}
	text, err := t.callTool(ctx, "qrdb_ceremony_submit_unseal_share", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonySubmitUnsealShare", Err: err}
	}
	var resp transport.CeremonySubmitUnsealShareResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CeremonySubmitUnsealShare", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}
