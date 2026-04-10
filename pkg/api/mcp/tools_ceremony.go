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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ceremonyTools returns the MCP tool registrations for initialization
// ceremony operations including initialization, status, certificate
// claiming, share distribution, and CSR signing.
func ceremonyTools(svc service.CeremonyServicer, ca ceremony.CeremonyCA) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolCeremonyInitialize(), Handler: handleCeremonyInitialize(svc, ca)},
		{Tool: toolCeremonyStatus(), Handler: handleCeremonyStatus(svc)},
		{Tool: toolCeremonyClaimCertBegin(), Handler: handleCeremonyClaimCertBegin(svc)},
		{Tool: toolCeremonyClaimCertComplete(), Handler: handleCeremonyClaimCertComplete(svc)},
		{Tool: toolCeremonyClaimShare(), Handler: handleCeremonyClaimShare(svc)},
		{Tool: toolCeremonySignCSR(), Handler: handleCeremonySignCSR(svc)},
	}
}

// toolCeremonyInitialize defines the qrdb_ceremony_initialize tool schema.
func toolCeremonyInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_initialize",
		mcp.WithDescription("Initialize the system ceremony, issuing TLS and SO certificates and optionally starting M-of-N enrollment."),
	)
}

// handleCeremonyInitialize returns a tool handler that delegates to
// svc.CeremonyInitialize with the pre-configured ceremony CA.
func handleCeremonyInitialize(svc service.CeremonyServicer, ca ceremony.CeremonyCA) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ca == nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: ErrCeremonyCANotConfigured}
		}
		result, err := svc.CeremonyInitialize(ctx, ca)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonyStatus defines the qrdb_ceremony_status tool schema.
func toolCeremonyStatus() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_status",
		mcp.WithDescription("Return the current ceremony state and SPKI pin."),
	)
}

// handleCeremonyStatus returns a tool handler that delegates to
// svc.CeremonyStatus.
func handleCeremonyStatus(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status, err := svc.CeremonyStatus(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_status", Err: err}
		}

		data, jsonErr := json.Marshal(status)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonyClaimCertBegin defines the qrdb_ceremony_claim_cert_begin
// tool schema.
func toolCeremonyClaimCertBegin() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_claim_cert_begin",
		mcp.WithDescription("Start the certificate claim handshake for an officer, returning a challenge nonce."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer username initiating the certificate claim."),
		),
	)
}

// handleCeremonyClaimCertBegin returns a tool handler that delegates to
// svc.CeremonyBeginClaimCert.
func handleCeremonyClaimCertBegin(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		challenge, err := svc.CeremonyBeginClaimCert(ctx, username)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_cert_begin", Err: err}
		}

		data, jsonErr := json.Marshal(challenge)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_cert_begin", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonyClaimCertComplete defines the qrdb_ceremony_claim_cert_complete
// tool schema.
func toolCeremonyClaimCertComplete() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_claim_cert_complete",
		mcp.WithDescription("Complete the certificate claim handshake by verifying the officer's nonce signature."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer username completing the certificate claim."),
		),
		mcp.WithString("nonce_hex",
			mcp.Required(),
			mcp.Description("The hex-encoded challenge nonce from the begin step."),
		),
		mcp.WithString("signature",
			mcp.Required(),
			mcp.Description("The base64-encoded signature over the nonce."),
		),
	)
}

// handleCeremonyClaimCertComplete returns a tool handler that delegates to
// svc.CeremonyCompleteClaimCert.
func handleCeremonyClaimCertComplete(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		nonceHex := mcp.ParseString(req, "nonce_hex", "")
		if nonceHex == "" {
			return mcp.NewToolResultError("nonce_hex is required"), nil
		}

		sigB64 := mcp.ParseString(req, "signature", "")
		if sigB64 == "" {
			return mcp.NewToolResultError("signature is required"), nil
		}

		sigBytes, decErr := base64.StdEncoding.DecodeString(sigB64)
		if decErr != nil {
			return mcp.NewToolResultError("signature must be valid base64"), nil
		}

		result, err := svc.CeremonyCompleteClaimCert(ctx, username, nonceHex, sigBytes)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_cert_complete", Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_cert_complete", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonyClaimShare defines the qrdb_ceremony_claim_share tool schema.
func toolCeremonyClaimShare() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_claim_share",
		mcp.WithDescription("Retrieve the Shamir share assigned to the given officer username."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer username claiming their Shamir share."),
		),
	)
}

// handleCeremonyClaimShare returns a tool handler that delegates to
// svc.CeremonyClaimShare.
func handleCeremonyClaimShare(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		share, err := svc.CeremonyClaimShare(ctx, username)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_share", Err: err}
		}

		result := struct {
			Share string `json:"share"`
		}{
			Share: base64.StdEncoding.EncodeToString(share),
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_claim_share", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonySignCSR defines the qrdb_ceremony_sign_csr tool schema.
func toolCeremonySignCSR() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_sign_csr",
		mcp.WithDescription("Sign a certificate signing request during the initialization ceremony."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer or service username requesting CSR signing."),
		),
		mcp.WithString("so_pin",
			mcp.Required(),
			mcp.Description("The Security Officer PIN authorizing the signing operation."),
		),
		mcp.WithString("csr_pem",
			mcp.Required(),
			mcp.Description("The PEM-encoded certificate signing request."),
		),
		mcp.WithString("role",
			mcp.Required(),
			mcp.Description("The certificate role profile to apply (e.g., security-officer, node)."),
		),
	)
}

// handleCeremonySignCSR returns a tool handler that delegates to
// svc.CeremonySignCSR.
func handleCeremonySignCSR(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		soPin := mcp.ParseString(req, "so_pin", "")
		if soPin == "" {
			return mcp.NewToolResultError("so_pin is required"), nil
		}

		csrPEM := mcp.ParseString(req, "csr_pem", "")
		if csrPEM == "" {
			return mcp.NewToolResultError("csr_pem is required"), nil
		}

		role := mcp.ParseString(req, "role", "")
		if role == "" {
			return mcp.NewToolResultError("role is required"), nil
		}

		result, err := svc.CeremonySignCSR(ctx, username, soPin, csrPEM, role)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_sign_csr", Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_sign_csr", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
