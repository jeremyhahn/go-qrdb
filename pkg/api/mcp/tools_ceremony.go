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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ceremonyTools returns the MCP tool registrations for initialization
// ceremony operations including initialization, status, certificate
// claiming, share distribution, and CSR signing.
func ceremonyTools(svc service.CeremonyServicer, ca ceremony.CeremonyCA) []serverTool {
	return []serverTool{
		{Tool: toolCeremonyInitialize(), Perm: permission.CeremonyInitialize, Handler: handleCeremonyInitialize(svc, ca)},
		{Tool: toolCeremonyStatus(), Perm: permission.CeremonyStatus, Handler: handleCeremonyStatus(svc)},
		{Tool: toolCeremonySignCSR(), Perm: permission.CeremonySignCSR, Handler: handleCeremonySignCSR(svc)},
	}
}

// toolCeremonyInitialize defines the qrdb_ceremony_initialize tool schema.
func toolCeremonyInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_initialize",
		mcp.WithDescription("Initialize the system ceremony, issuing TLS and SO certificates and optionally starting M-of-N enrollment."),
		withSecretArg("so_pin", "The Security Officer PIN to seal at initialization. Takes precedence over server configuration when provided."),
		withSecretArg("user_pin", "The user-level PIN to seal at initialization. Takes precedence over server configuration when provided."),
		mcp.WithString("credential_seal_strategy",
			mcp.Description("How credentials are sealed at rest. Takes precedence over server configuration when provided."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("The M-of-N Shamir threshold. Requires officers to be non-empty; takes precedence over server configuration when non-zero."),
		),
		mcp.WithString("policy",
			mcp.Description("Factor policy of the initial share set: designated (default) or any-enrolled."),
		),
		mcp.WithString("officers",
			mcp.Description("Initial share holders, JSON-encoded: an array of {\"username\":..., \"role\":\"so\"|\"custodian\", \"csr_pem\":..., \"encryption_kem\":..., \"encryption_key\":..., \"factors\":[...], \"designated\":...} objects, e.g. '[{\"username\":\"alice\",\"role\":\"so\",\"csr_pem\":\"...\"}]'. encryption_kem/encryption_key name an optional separate share-envelope key (HPKE suite, base64 public key) -- without one the certificate key receives the envelope. factors is a JSON array of additional factor enrolments: [{\"name\":...,\"params\":{...}}]. designated names the factor fixed at enrolment under the designated policy; mtls when empty. Takes precedence over server configuration when non-empty."),
		),
		mcp.WithString("hostname",
			mcp.Description("The primary hostname for TLS certificate generation. Takes precedence over server configuration when provided."),
		),
		mcp.WithString("sans",
			mcp.Description("Additional Subject Alternative Names for TLS certificates, JSON-encoded as a string array, e.g. '[\"host2\",\"1.2.3.4\"]'. Takes precedence over server configuration when non-empty."),
		),
		mcp.WithString("admin_csr_pem",
			mcp.Description("PEM-encoded CSR for the bootstrap admin's certificate (CSR-sign path). Mutually exclusive with admin_cert_pem and admin_generate; requires a configured CeremonyCA."),
		),
		mcp.WithString("admin_cert_pem",
			mcp.Description("PEM-encoded, caller-supplied bootstrap admin certificate (bring-your-own-certificate path). Mutually exclusive with admin_csr_pem and admin_generate; needs no CeremonyCA."),
		),
		mcp.WithString("admin_cert_pool_pem",
			mcp.Description("PEM-encoded CA bundle admin_cert_pem must chain to when it is not self-signed. Ignored unless admin_cert_pem is set."),
		),
		mcp.WithBoolean("admin_generate",
			mcp.Description("Selects the generate path explicitly: the server mints the bootstrap admin's key pair and certificate. Mutually exclusive with admin_csr_pem and admin_cert_pem; requires a configured CeremonyCA. The generated private key is returned exactly once in the response and never retained."),
		),
		withSecretArg("secret", "The passphrase the system barrier is initialized with (single-admin path only; ignored for Threshold >= 2, where the barrier unseals later from share holders' Shamir reconstruction)."),
	)
}

// handleCeremonyInitialize returns a tool handler that delegates to
// svc.CeremonyInitialize with the pre-configured ceremony CA.
//
// ca is nil whenever this node's TLS identity is BYO (no CA private key
// configured) -- that is no longer refused here.
// ceremony.CeremonyService.Initialize supports ca == nil directly (the
// operator's own leaf certificate is the server identity, and no CA is
// needed to pin it), refusing only the two admin-credential paths that
// actually need one to sign with (CSR-sign, generate) via the typed
// *ceremony.AdminCredentialRequiresCAError the service layer already
// returns (go-qrdb#560 review item 5).
// ceremonyInitializeSecretArgs is every argument handleCeremonyInitialize
// reads through secretArg, in both forms, and so keeps out of its struct
// decode.
var ceremonyInitializeSecretArgs = map[string]struct{}{
	"secret": {}, "secret" + secretB64Suffix: {},
	"so_pin": {}, "so_pin" + secretB64Suffix: {},
	"user_pin": {}, "user_pin" + secretB64Suffix: {},
}

func handleCeremonyInitialize(svc service.CeremonyServicer, ca ceremony.CeremonyCA) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// "secret", "so_pin" and "user_pin" are decoded separately, through
		// secretArg, in the text-or-_b64 forms every barrier and ceremony
		// secret takes -- never through the generic struct decode below,
		// which knows neither form's sibling. argsWithoutSecrets is a
		// shallow copy with both forms of each removed.
		args := req.GetArguments()
		argsWithoutSecrets := make(map[string]any, len(args))
		for k, v := range args {
			if _, isSecret := ceremonyInitializeSecretArgs[k]; isSecret {
				continue
			}
			argsWithoutSecrets[k] = v
		}

		var in transport.CeremonyInitializeRequest
		if err := wire.DecodeArgs(argsWithoutSecrets, &in, wire.CeremonyJSONFields...); err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}
		soPIN, userPIN, err := initializePINArgs(req)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}
		in.SOPin, in.UserPin = soPIN, userPIN
		if in.Secret, err = secretArg(req, "secret"); err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}

		// in.Secret is consumed into a *secure.Buffer by CeremonyInitFrom,
		// which this handler owns and destroys once the ceremony call below
		// has finished with it.
		opts, err := holderwire.CeremonyInitFrom(&in)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}
		defer opts.Secret.Destroy()

		result, err := svc.CeremonyInitialize(ctx, ca, opts)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_initialize", Err: err}
		}
		// out carries its own independent copy of result.AdminKeyPEM
		// (holderwire.CeremonyInitTo's bytes.Clone), and holderResult
		// fully serializes it to JSON text before returning, so wiping
		// result's copy here, now that out is built and about to be
		// returned, cannot affect the response already handed back
		// (go-qrdb#560 review item 7: every transport, not only REST).
		out := holderwire.CeremonyInitTo(result)
		result.WipeAdminKeyPEM()
		return holderResult("qrdb_ceremony_initialize", out)
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

// toolCeremonySignCSR defines the qrdb_ceremony_sign_csr tool schema.
func toolCeremonySignCSR() mcp.Tool {
	return mcp.NewTool("qrdb_ceremony_sign_csr",
		mcp.WithDescription("Sign a certificate signing request during the initialization ceremony."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The officer or service username requesting CSR signing."),
		),
		withSecretArg("so_pin", "The Security Officer PIN authorizing the signing operation. Required, in exactly one of its two forms."),
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

		soPin, err := pinArg(req, "so_pin")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_sign_csr", Err: err}
		}
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

		wire := &transport.SignCSRInitResponse{CertPEM: string(result.CertPEM)}
		data, jsonErr := json.Marshal(wire)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_ceremony_sign_csr", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
