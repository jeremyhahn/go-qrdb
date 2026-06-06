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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// toolCeremonyClaimShareBegin defines the qrdb_ceremony_claim_share_begin tool.
func toolCeremonyClaimShareBegin() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_claim_share_begin",
		mcp.WithDescription("Begin a custodian Shamir-share claim: returns a factor challenge to sign."),
		mcp.WithString("custodian_id", mcp.Required(), mcp.Description("The enrolled custodian ID.")),
	)
}

// handleCeremonyClaimShareBegin returns a handler that issues a claim challenge.
func handleCeremonyClaimShareBegin(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		custodianID := mcp.ParseString(req, "custodian_id", "")
		if custodianID == "" {
			return mcp.NewToolResultError("custodian_id is required"), nil
		}
		ch, err := svc.CeremonyClaimShareBegin(ctx, custodianID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_share_begin", Err: err}
		}
		return marshalToolResult("qrdb_ceremony_claim_share_begin", map[string]any{
			"custodian_id": ch.CustodianID,
			"nonce":        base64.StdEncoding.EncodeToString(ch.Nonce),
			"expires_at":   ch.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
}

// toolCeremonyClaimShareComplete defines the qrdb_ceremony_claim_share_complete tool.
func toolCeremonyClaimShareComplete() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_claim_share_complete",
		mcp.WithDescription("Complete a custodian Shamir-share claim by submitting the factor response."),
		mcp.WithString("custodian_id", mcp.Required(), mcp.Description("The enrolled custodian ID.")),
		mcp.WithString("challenge_id", mcp.Required(), mcp.Description("The challenge ID from claim-share-begin.")),
		mcp.WithString("factor_response", mcp.Description("Base64-encoded factor response (signed challenge).")),
	)
}

// handleCeremonyClaimShareComplete returns a handler that verifies the claim.
func handleCeremonyClaimShareComplete(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		custodianID := mcp.ParseString(req, "custodian_id", "")
		challengeID := mcp.ParseString(req, "challenge_id", "")
		if custodianID == "" || challengeID == "" {
			return mcp.NewToolResultError("custodian_id and challenge_id are required"), nil
		}
		factorResp, err := decodeOptionalBase64(req, "factor_response")
		if err != nil {
			return mcp.NewToolResultError("factor_response must be base64-encoded"), nil
		}
		c, err := svc.CeremonyClaimShareComplete(ctx, custodianID, challengeID, factorResp)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_share_complete", Err: err}
		}
		return marshalToolResult("qrdb_ceremony_claim_share_complete", c)
	}
}

// toolCeremonySubmitUnsealShare defines the qrdb_ceremony_submit_unseal_share tool.
func toolCeremonySubmitUnsealShare() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_submit_unseal_share",
		mcp.WithDescription("Submit a custodian's plaintext Shamir share (with factor proof) toward the unseal quorum."),
		mcp.WithString("custodian_id", mcp.Required(), mcp.Description("The enrolled custodian ID.")),
		mcp.WithString("challenge_id", mcp.Description("The challenge ID from claim-share-begin.")),
		mcp.WithString("factor_response", mcp.Description("Base64-encoded factor response.")),
		mcp.WithString("share", mcp.Required(), mcp.Description("Base64-encoded plaintext Shamir share.")),
	)
}

// handleCeremonySubmitUnsealShare returns a handler that submits an unseal share.
func handleCeremonySubmitUnsealShare(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		custodianID := mcp.ParseString(req, "custodian_id", "")
		if custodianID == "" {
			return mcp.NewToolResultError("custodian_id is required"), nil
		}
		shareBytes, err := decodeOptionalBase64(req, "share")
		if err != nil || len(shareBytes) == 0 {
			return mcp.NewToolResultError("share must be a non-empty base64-encoded value"), nil
		}
		factorResp, err := decodeOptionalBase64(req, "factor_response")
		if err != nil {
			return mcp.NewToolResultError("factor_response must be base64-encoded"), nil
		}
		result, err := svc.CeremonySubmitUnsealShare(ctx, custodianID,
			mcp.ParseString(req, "challenge_id", ""), factorResp, shareBytes)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_submit_unseal_share", Err: err}
		}
		return marshalToolResult("qrdb_ceremony_submit_unseal_share", result)
	}
}

// decodeOptionalBase64 decodes a base64 string argument, returning nil for an
// absent/empty argument.
func decodeOptionalBase64(req mcp.CallToolRequest, key string) ([]byte, error) {
	s := mcp.ParseString(req, key, "")
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
