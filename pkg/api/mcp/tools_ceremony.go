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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ceremonyTools returns the MCP tool registrations for initialization
// ceremony operations including initialization, status, certificate
// claiming, share distribution, and CSR signing.
func ceremonyTools(svc service.CeremonyServicer, ca ceremony.CeremonyCA) []serverTool {
	return []serverTool{
		{Tool: toolCeremonyInitialize(), Perm: permission.CeremonyInitialize, Handler: handleCeremonyInitialize(svc, ca)},
		{Tool: toolCeremonyStatus(), Perm: permission.CeremonyStatus, Handler: handleCeremonyStatus(svc)},
		{Tool: toolCeremonyClaimCertBegin(), Perm: permission.CeremonyClaimCert, Handler: handleCeremonyClaimCertBegin(svc)},
		{Tool: toolCeremonyClaimCertComplete(), Perm: permission.CeremonyClaimCert, Handler: handleCeremonyClaimCertComplete(svc)},
		{Tool: toolCeremonyBeginClaimShare(), Perm: permission.IdentityGated, Handler: handleCeremonyBeginClaimShare(svc)},
		{Tool: toolCeremonyCompleteClaimShare(), Perm: permission.IdentityGated, Handler: handleCeremonyCompleteClaimShare(svc)},
		{Tool: toolCeremonySignCSR(), Perm: permission.CeremonySignCSR, Handler: handleCeremonySignCSR(svc)},
		{Tool: toolCeremonyClaimShareBegin(), Perm: permission.IdentityGated, Handler: handleCeremonyClaimShareBegin(svc)},
		{Tool: toolCeremonyClaimShareComplete(), Perm: permission.IdentityGated, Handler: handleCeremonyClaimShareComplete(svc)},
		{Tool: toolCeremonySubmitUnsealShare(), Perm: permission.BarrierUnsealShare, Handler: handleCeremonySubmitUnsealShare(svc)},
	}
}

// toolCeremonyInitialize defines the qrdb_ceremony_initialize tool schema.
func toolCeremonyInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_initialize",
		mcp.WithDescription("Initialize the system ceremony, issuing TLS and SO certificates and optionally starting M-of-N enrollment."),
		mcp.WithString("so_pin",
			mcp.Description("The Security Officer PIN to seal at initialization. Takes precedence over server configuration when provided."),
		),
		mcp.WithString("user_pin",
			mcp.Description("The user-level PIN to seal at initialization. Takes precedence over server configuration when provided."),
		),
		mcp.WithString("credential_seal_strategy",
			mcp.Description("How credentials are sealed at rest. Takes precedence over server configuration when provided."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("The M-of-N Shamir threshold. Requires officers to be non-empty; takes precedence over server configuration when non-zero."),
		),
		mcp.WithString("officers",
			mcp.Description("Officer configurations for M-of-N enrollment, JSON-encoded: an array of {\"username\":..., \"csr_pem\":...} objects, e.g. '[{\"username\":\"alice\",\"csr_pem\":\"...\"}]'. Takes precedence over server configuration when non-empty."),
		),
		mcp.WithString("hostname",
			mcp.Description("The primary hostname for TLS certificate generation. Takes precedence over server configuration when provided."),
		),
		mcp.WithString("sans",
			mcp.Description("Additional Subject Alternative Names for TLS certificates, JSON-encoded as a string array, e.g. '[\"host2\",\"1.2.3.4\"]'. Takes precedence over server configuration when non-empty."),
		),
	)
}

// handleCeremonyInitialize returns a tool handler that delegates to
// svc.CeremonyInitialize with the pre-configured ceremony CA.
func handleCeremonyInitialize(svc service.CeremonyServicer, ca ceremony.CeremonyCA) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ca == nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: ErrCeremonyCANotConfigured}
		}
		opts := &service.CeremonyInitializeOptions{
			SOPin:                  mcp.ParseString(req, "so_pin", ""),
			UserPin:                mcp.ParseString(req, "user_pin", ""),
			CredentialSealStrategy: mcp.ParseString(req, "credential_seal_strategy", ""),
			Threshold:              mcp.ParseInt(req, "threshold", 0),
			Officers:               parseCeremonyOfficers(req, "officers"),
			Hostname:               mcp.ParseString(req, "hostname", ""),
			SANs:                   parseStringArray(req, "sans"),
		}
		result, err := svc.CeremonyInitialize(ctx, ca, opts)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}

		data, jsonErr := json.Marshal(ceremonyInitToWireResponse(result))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ceremonyInitToWireResponse maps a service.CeremonyInitResult (PascalCase
// Go field names, no json tags) to the canonical snake_case wire shape every
// other transport produces -- REST's handlers_ceremony.go
// ceremonyInitToResponse and the SDK's transport.CeremonyInitializeResponse
// json tags. Marshaling *result directly (the prior behavior) emitted
// "SPKIPin"/"CACertPEM"/... keys that sdk/go/transport/mcp/transport_ceremony.go's
// CeremonyInitialize could never decode (json.Unmarshal's case-insensitive
// fallback only matches the STRUCT TAG case-insensitively, not an unrelated
// snake_case name), silently zeroing every field of the CLI's `qrdb init`
// response over MCP alone.
func ceremonyInitToWireResponse(r *service.CeremonyInitResult) map[string]interface{} {
	return map[string]interface{}{
		"spki_pin":     r.SPKIPin,
		"ca_cert_pem":  string(r.CACertPEM),
		"tls_cert_pem": string(r.TLSCertPEM),
		"state":        r.State,
		"share_count":  r.ShareCount,
	}
}

// parseStringArray reads an optional JSON-encoded string-array argument
// (e.g. '["a","b"]') into a []string. The wire shape matches every other
// array-valued MCP argument in this SDK (see sdk/go/transport/mcp's
// json.Marshal-then-string-arg convention, e.g. transport_ceremony.go's
// CeremonyInitialize encoding req.SANs into args["sans"] as a JSON string,
// and tools_vector.go's "ids" argument / handler pair for the server-side
// precedent): the tool schema declares the argument as a plain string
// (mcp.WithString), NOT mcp.WithArray, so it must be parsed with
// mcp.ParseString + json.Unmarshal rather than a type assertion to
// []interface{}, which a string argument value never satisfies.
func parseStringArray(req mcp.CallToolRequest, key string) []string {
	raw := mcp.ParseString(req, key, "")
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// parseCeremonyOfficers reads an optional JSON-encoded array-of-object
// argument (each element shaped {"username":..., "csr_pem":...}) into a
// []service.CeremonyOfficerConfig. See parseStringArray's doc comment for
// why this parses a JSON string (mcp.ParseString + json.Unmarshal) rather
// than type-asserting a native array value.
func parseCeremonyOfficers(req mcp.CallToolRequest, key string) []service.CeremonyOfficerConfig {
	raw := mcp.ParseString(req, key, "")
	if raw == "" {
		return nil
	}
	var wire []struct {
		Username string `json:"username"`
		CSRPEM   string `json:"csr_pem"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil || len(wire) == 0 {
		return nil
	}
	out := make([]service.CeremonyOfficerConfig, 0, len(wire))
	for _, o := range wire {
		out = append(out, service.CeremonyOfficerConfig{Username: o.Username, CSRPEM: o.CSRPEM})
	}
	return out
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

		data, jsonErr := json.Marshal(map[string]interface{}{
			"state":    status.State,
			"spki_pin": status.SPKIPin,
		})
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

// toolCeremonyBeginClaimShare defines the qrdb_ceremony_begin_claim_share
// tool schema.
func toolCeremonyBeginClaimShare() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_begin_claim_share",
		mcp.WithDescription("Start the Shamir share claim handshake for an officer. Requires an mTLS client certificate matching the officer's enrolment record."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer username claiming their Shamir share."),
		),
	)
}

// handleCeremonyBeginClaimShare returns a tool handler that delegates to
// svc.CeremonyBeginClaimShare. The caller certificate thumbprint is taken from
// the MCP session's mTLS peer certificate, never from the tool arguments.
func handleCeremonyBeginClaimShare(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_ceremony_begin_claim_share")
		if err != nil {
			return nil, err
		}

		challenge, err := svc.CeremonyBeginClaimShare(ctx, username, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_begin_claim_share", Err: err}
		}

		result := struct {
			NonceHex  string `json:"nonce_hex"`
			ExpiresAt string `json:"expires_at"`
		}{
			NonceHex:  challenge.NonceHex,
			ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_begin_claim_share", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCeremonyCompleteClaimShare defines the
// qrdb_ceremony_complete_claim_share tool schema.
func toolCeremonyCompleteClaimShare() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_complete_claim_share",
		mcp.WithDescription("Complete the Shamir share claim handshake by submitting the signed challenge nonce. The share is released exactly once."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer username claiming their Shamir share."),
		),
		mcp.WithString("nonce_hex",
			mcp.Required(),
			mcp.Description("The hex-encoded challenge nonce returned by begin."),
		),
		mcp.WithString("signature",
			mcp.Required(),
			mcp.Description("Base64-encoded signature over the raw nonce bytes."),
		),
	)
}

// handleCeremonyCompleteClaimShare returns a tool handler that delegates to
// svc.CeremonyCompleteClaimShare. The caller certificate thumbprint is taken
// from the MCP session's mTLS peer certificate, never from the tool arguments.
func handleCeremonyCompleteClaimShare(svc service.CeremonyServicer) mcpserver.ToolHandlerFunc {
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
		sig, decErr := base64.StdEncoding.DecodeString(sigB64)
		if decErr != nil {
			return mcp.NewToolResultError("signature must be base64-encoded"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_ceremony_complete_claim_share")
		if err != nil {
			return nil, err
		}

		claimed, err := svc.CeremonyCompleteClaimShare(ctx, username, nonceHex, sig, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_complete_claim_share", Err: err}
		}

		result := struct {
			ShareIndex int    `json:"share_index"`
			Share      string `json:"share"`
		}{
			ShareIndex: claimed.ShareIndex,
			Share:      base64.StdEncoding.EncodeToString(claimed.ShareBytes),
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_complete_claim_share", Err: jsonErr}
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
