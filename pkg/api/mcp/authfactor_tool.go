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

package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// authFactorTools returns the MCP tool registrations for custodian auth factor
// operations: enroll, challenge, verify, list, revoke.
func authFactorTools(svc service.AuthFactorServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolAuthFactorEnroll(), Handler: handleAuthFactorEnroll(svc)},
		{Tool: toolAuthFactorChallenge(), Handler: handleAuthFactorChallenge(svc)},
		{Tool: toolAuthFactorVerify(), Handler: handleAuthFactorVerify(svc)},
		{Tool: toolAuthFactorList(), Handler: handleAuthFactorList(svc)},
		{Tool: toolAuthFactorRevoke(), Handler: handleAuthFactorRevoke(svc)},
	}
}

// ---------------------------------------------------------------------------
// Tool schema definitions
// ---------------------------------------------------------------------------

// toolAuthFactorEnroll defines the qrdb_authfactor_enroll tool schema.
func toolAuthFactorEnroll() mcp.Tool {
	return mcp.NewTool("qrdb_authfactor_enroll",
		mcp.WithDescription("Enroll a user with an authentication factor (pkcs11, piv, or fido2)."),
		mcp.WithString("factor_name",
			mcp.Required(),
			mcp.Description("Factor type: pkcs11, piv, or fido2."),
		),
		mcp.WithString("subject",
			mcp.Required(),
			mcp.Description("User ID (custodian identifier) being enrolled."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("Optional tenant ID for multi-tenant deployments."),
		),
		mcp.WithString("method_params",
			mcp.Description("JSON-encoded method-specific parameters."),
		),
	)
}

// toolAuthFactorChallenge defines the qrdb_authfactor_challenge tool schema.
func toolAuthFactorChallenge() mcp.Tool {
	return mcp.NewTool("qrdb_authfactor_challenge",
		mcp.WithDescription("Issue a cryptographic challenge for a factor enrollment."),
		mcp.WithString("enrollment_id",
			mcp.Required(),
			mcp.Description("The enrollment ID to challenge."),
		),
	)
}

// toolAuthFactorVerify defines the qrdb_authfactor_verify tool schema.
func toolAuthFactorVerify() mcp.Tool {
	return mcp.NewTool("qrdb_authfactor_verify",
		mcp.WithDescription("Verify a challenge response to prove custodian possession."),
		mcp.WithString("challenge_id",
			mcp.Required(),
			mcp.Description("The challenge ID issued by qrdb_authfactor_challenge."),
		),
		mcp.WithString("factor_name",
			mcp.Description("Optional factor type override."),
		),
		mcp.WithString("response_json",
			mcp.Required(),
			mcp.Description("JSON-encoded Response object {value (base64), metadata}."),
		),
	)
}

// toolAuthFactorList defines the qrdb_authfactor_list tool schema.
func toolAuthFactorList() mcp.Tool {
	return mcp.NewTool("qrdb_authfactor_list",
		mcp.WithDescription("List auth factor enrollments, optionally filtered by subject."),
		mcp.WithString("subject",
			mcp.Description("Filter by user ID (empty returns all)."),
		),
	)
}

// toolAuthFactorRevoke defines the qrdb_authfactor_revoke tool schema.
func toolAuthFactorRevoke() mcp.Tool {
	return mcp.NewTool("qrdb_authfactor_revoke",
		mcp.WithDescription("Revoke an auth factor enrollment by ID."),
		mcp.WithString("enrollment_id",
			mcp.Required(),
			mcp.Description("The enrollment ID to revoke."),
		),
	)
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// handleAuthFactorEnroll returns a tool handler that delegates to svc.EnrollFactor.
//
// TODO: apply permission.AuthFactorEnroll once Phase 2 ships.
func handleAuthFactorEnroll(svc service.AuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factorName := mcp.ParseString(req, "factor_name", "")
		subject := mcp.ParseString(req, "subject", "")
		tenantID := mcp.ParseString(req, "tenant_id", "")
		methodParamsJSON := mcp.ParseString(req, "method_params", "{}")

		if factorName == "" {
			return mcp.NewToolResultError("factor_name is required"), nil
		}
		if subject == "" {
			return mcp.NewToolResultError("subject is required"), nil
		}

		var methodParams map[string]any
		if err := json.Unmarshal([]byte(methodParamsJSON), &methodParams); err != nil {
			return mcp.NewToolResultError("invalid method_params JSON: " + err.Error()), nil
		}

		params := authfactors.EnrollParams{
			UserID:       subject,
			TenantID:     tenantID,
			MethodParams: methodParams,
		}

		enrollment, err := svc.EnrollFactor(ctx, factorName, subject, params)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_authfactor_enroll", Err: err}
		}

		out, _ := json.Marshal(map[string]any{
			"enrollment_id": enrollment.EnrollmentID,
			"factor_name":   enrollment.FactorName,
			"user_id":       enrollment.UserID,
			"tenant_id":     enrollment.TenantID,
			"metadata":      enrollment.Metadata,
			"enrolled_at":   enrollment.EnrolledAt.Format(time.RFC3339),
		})
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleAuthFactorChallenge returns a tool handler that delegates to svc.ChallengeFactor.
//
// TODO: apply permission.AuthFactorChallenge once Phase 2 ships.
func handleAuthFactorChallenge(svc service.AuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		enrollmentID := mcp.ParseString(req, "enrollment_id", "")
		if enrollmentID == "" {
			return mcp.NewToolResultError("enrollment_id is required"), nil
		}

		challenge, err := svc.ChallengeFactor(ctx, enrollmentID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_authfactor_challenge", Err: err}
		}

		out, _ := json.Marshal(map[string]any{
			"challenge_id":  challenge.ChallengeID,
			"enrollment_id": challenge.EnrollmentID,
			"nonce":         challenge.Nonce,
			"expires_at":    challenge.ExpiresAt.Format(time.RFC3339),
			"method_data":   challenge.MethodData,
		})
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleAuthFactorVerify returns a tool handler that delegates to svc.VerifyFactor.
//
// TODO: apply permission.AuthFactorVerify once Phase 2 ships.
func handleAuthFactorVerify(svc service.AuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		challengeID := mcp.ParseString(req, "challenge_id", "")
		factorName := mcp.ParseString(req, "factor_name", "")
		responseJSON := mcp.ParseString(req, "response_json", "")

		if challengeID == "" {
			return mcp.NewToolResultError("challenge_id is required"), nil
		}
		if responseJSON == "" {
			return mcp.NewToolResultError("response_json is required"), nil
		}

		var response authfactors.Response
		if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
			return mcp.NewToolResultError("invalid response_json: " + err.Error()), nil
		}

		if err := svc.VerifyFactor(ctx, factorName, challengeID, &response); err != nil {
			return nil, &ToolError{Tool: "qrdb_authfactor_verify", Err: err}
		}

		return mcp.NewToolResultText(`{"status":"verified"}`), nil
	}
}

// handleAuthFactorList returns a tool handler that delegates to svc.ListFactors.
//
// TODO: apply permission.AuthFactorList once Phase 2 ships.
func handleAuthFactorList(svc service.AuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		subject := mcp.ParseString(req, "subject", "")

		enrollments, err := svc.ListFactors(ctx, subject)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_authfactor_list", Err: err}
		}

		entries := make([]map[string]any, len(enrollments))
		for i, e := range enrollments {
			entry := map[string]any{
				"enrollment_id": e.EnrollmentID,
				"factor_name":   e.FactorName,
				"user_id":       e.UserID,
				"tenant_id":     e.TenantID,
				"metadata":      e.Metadata,
				"enrolled_at":   e.EnrolledAt.Format(time.RFC3339),
			}
			if !e.RevokedAt.IsZero() {
				entry["revoked_at"] = e.RevokedAt.Format(time.RFC3339)
			}
			entries[i] = entry
		}

		out, _ := json.Marshal(map[string]any{
			"enrollments": entries,
			"total":       len(entries),
		})
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleAuthFactorRevoke returns a tool handler that delegates to svc.RevokeFactor.
//
// TODO: apply permission.AuthFactorRevoke once Phase 2 ships.
func handleAuthFactorRevoke(svc service.AuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		enrollmentID := mcp.ParseString(req, "enrollment_id", "")
		if enrollmentID == "" {
			return mcp.NewToolResultError("enrollment_id is required"), nil
		}

		if err := svc.RevokeFactor(ctx, enrollmentID); err != nil {
			return nil, &ToolError{Tool: "qrdb_authfactor_revoke", Err: err}
		}

		return mcp.NewToolResultText(`{"status":"revoked"}`), nil
	}
}
