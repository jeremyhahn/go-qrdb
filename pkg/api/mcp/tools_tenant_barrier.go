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
	"encoding/hex"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantScopeAuthorizer authorizes a per-tenant barrier tool invocation
// against the TRUE target tenant carried in the tool arguments (not the
// caller's ambient tenant). The coarse mcpToolPermissions mapping enforced by
// toolAuthzMiddleware only proves the caller holds the permission at all;
// this fine-grained gate delegates to the tenant-scoped authorizer, whose
// ScopeTenantOnly rule denies the system tenant and any cross-tenant access.
// It mirrors authorizeTenantBarrierOp on the QUIC, gRPC, and REST servers.
type tenantScopeAuthorizer func(ctx context.Context, tenantID string, perm permission.Permission) error

// tenantBarrierAuthorizers carries the three authorization gates the tenant
// barrier tools dispatch over. Op is the plain tenant-scoped RBAC check
// (seal, officer management); Init is the trust-on-first-init gate and
// Sealed the sealed-window gate — both route through the shared
// cross-transport rules in pkg/auth/middleware (AuthorizeTenantBarrierInit /
// AuthorizeTenantBarrierSealedOp) so the fallback semantics cannot drift
// from REST, gRPC, and QUIC.
type tenantBarrierAuthorizers struct {
	Op     tenantScopeAuthorizer
	Init   func(ctx context.Context, tenantID string) error
	Sealed tenantScopeAuthorizer
}

// tenantBarrierTools returns the MCP tool registrations for per-tenant
// barrier lifecycle: init, unseal, seal, status, and Security Officer flows.
// Every handler authorizes against the target tenant parsed from the tool
// arguments before delegating to the tenant service. Init/Unseal/
// UnsealShare/Status register as IdentityGated at the tool layer because
// their RBAC check runs inside the handlers with the bootstrap-window
// fallbacks (see tenantBarrierAuthorizers).
func tenantBarrierTools(svc tenant.TenantServicer, authz tenantBarrierAuthorizers) []serverTool {
	authorize := authz.Op
	return []serverTool{
		{Tool: toolTenantBarrierInit(), Perm: permission.IdentityGated, Handler: handleTenantBarrierInit(svc, authz.Init)},
		{Tool: toolTenantBarrierUnseal(), Perm: permission.IdentityGated, Handler: handleTenantBarrierUnseal(svc, authz.Sealed)},
		{Tool: toolTenantBarrierUnsealShare(), Perm: permission.IdentityGated, Handler: handleTenantBarrierUnsealShare(svc, authz.Sealed)},
		{Tool: toolTenantBarrierSeal(), Perm: permission.TenantBarrierSeal, Handler: handleTenantBarrierSeal(svc, authorize)},
		{Tool: toolTenantBarrierStatus(), Perm: permission.IdentityGated, Handler: handleTenantBarrierStatus(svc, authz.Sealed)},
		{Tool: toolTenantBarrierOfficerRegister(), Perm: permission.TenantBarrierOfficerRegister, Handler: handleTenantBarrierOfficerRegister(svc, authorize)},
		{Tool: toolTenantBarrierOfficerList(), Perm: permission.TenantBarrierOfficerList, Handler: handleTenantBarrierOfficerList(svc, authorize)},
		{Tool: toolTenantBarrierShareClaimBegin(), Perm: permission.IdentityGated, Handler: handleTenantBarrierShareClaimBegin(svc, authorize)},
		{Tool: toolTenantBarrierShareClaimComplete(), Perm: permission.IdentityGated, Handler: handleTenantBarrierShareClaimComplete(svc, authorize)},
		{Tool: toolTenantBarrierOfficerClaimStatus(), Perm: permission.TenantBarrierOfficerList, Handler: handleTenantBarrierOfficerClaimStatus(svc, authorize)},
		{Tool: toolTenantBarrierShamirSealVote(), Perm: permission.TenantBarrierSealVote, Handler: handleTenantBarrierShamirSealVote(svc, authorize)},
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_init
// ---------------------------------------------------------------------------

// toolTenantBarrierInit defines the qrdb_tenant_barrier_init tool schema.
func toolTenantBarrierInit() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_init",
		mcp.WithDescription("Initialize a per-tenant barrier. Tenant must already exist."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier should be initialized."),
		),
		mcp.WithString("passphrase",
			mcp.Description("The caller-supplied passphrase used to seal the tenant barrier. Required for the passphrase strategy; ignored for hardware/cloud strategies."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Shamir threshold (number of shares required to unseal). Optional; 0 selects defaults."),
		),
		mcp.WithNumber("shares",
			mcp.Description("Total number of Shamir shares to generate. Optional; 0 selects defaults."),
		),
		mcp.WithString("strategy",
			mcp.Description("Seal strategy for the per-tenant barrier. Defaults to 'passphrase' when empty. Valid values include 'passphrase', 'none', 'shamir', 'tpm2', 'pkcs11', 'aws-kms', 'gcp-kms', 'azure-kv', 'vault' (hardware/cloud strategies require go-xkms injection)."),
		),
	)
}

// handleTenantBarrierInit returns a tool handler that initializes the
// per-tenant barrier for the given tenant. When the strategy field is empty
// or omitted the passphrase (software) strategy is selected.
func handleTenantBarrierInit(svc tenant.TenantServicer, authorize func(ctx context.Context, tenantID string) error) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID); err != nil {
			return authzToolError(err), nil
		}

		passphrase := secretArg(req, "passphrase")
		defer secure.Wipe(passphrase)
		threshold := mcp.ParseInt(req, "threshold", 0)
		shares := mcp.ParseInt(req, "shares", 0)
		strategy := mcp.ParseString(req, "strategy", "")

		result, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, passphrase, threshold, shares, seal.StrategyID(strategy))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_init", Err: err}
		}

		// INV-1/INV-11: shares are NEVER returned in init responses.
		// Security Officers claim their individual share via the SO claim flow.
		resp := map[string]any{
			"tenant_id":    result.TenantID,
			"strategy":     result.Strategy,
			"threshold":    result.Threshold,
			"total_shares": result.TotalShares,
			"share_count":  result.ShareCount,
		}
		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_init", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_unseal
// ---------------------------------------------------------------------------

// toolTenantBarrierUnseal defines the qrdb_tenant_barrier_unseal tool schema.
func toolTenantBarrierUnseal() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_unseal",
		mcp.WithDescription("Unseal a per-tenant barrier using the supplied key material."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier should be unsealed."),
		),
		mcp.WithString("key",
			mcp.Description("Base64-encoded key material. Optional; when omitted the deterministic default credential is used."),
		),
	)
}

// handleTenantBarrierUnseal returns a tool handler that unseals the
// per-tenant barrier with the supplied key.
func handleTenantBarrierUnseal(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierUnseal); err != nil {
			return authzToolError(err), nil
		}

		var key []byte
		if encoded := mcp.ParseString(req, "key", ""); encoded != "" {
			decoded, decErr := base64.StdEncoding.DecodeString(encoded)
			if decErr != nil {
				return mcp.NewToolResultError("key must be valid base64: " + decErr.Error()), nil
			}
			key = decoded
		}

		if err := svc.TenantBarrierUnseal(ctx, tenantID, key); err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_unseal", Err: err}
		}

		return mcp.NewToolResultText(`{"unsealed":true}`), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_unseal_share
// ---------------------------------------------------------------------------

// toolTenantBarrierUnsealShare defines the qrdb_tenant_barrier_unseal_share
// tool schema.
func toolTenantBarrierUnsealShare() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_unseal_share",
		mcp.WithDescription("Submit a single Shamir share toward a per-tenant barrier's quorum threshold. The barrier unseals automatically once enough distinct shares are submitted."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose Shamir barrier should receive the share."),
		),
		mcp.WithString("share",
			mcp.Required(),
			mcp.Description("A single Shamir share value generated at initialization."),
		),
	)
}

// handleTenantBarrierUnsealShare returns a tool handler that submits a single
// Shamir share toward the per-tenant barrier's quorum and reports progress.
func handleTenantBarrierUnsealShare(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierUnseal); err != nil {
			return authzToolError(err), nil
		}
		share := mcp.ParseString(req, "share", "")
		if share == "" {
			return mcp.NewToolResultError("share is required"), nil
		}

		progress, err := svc.TenantBarrierUnsealShare(ctx, tenantID, share)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_unseal_share", Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_unseal_share", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_seal
// ---------------------------------------------------------------------------

// toolTenantBarrierSeal defines the qrdb_tenant_barrier_seal tool schema.
func toolTenantBarrierSeal() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_seal",
		mcp.WithDescription("Seal a per-tenant barrier, refusing further reads/writes until unsealed again."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier should be sealed."),
		),
	)
}

// handleTenantBarrierSeal returns a tool handler that seals the per-tenant
// barrier for the given tenant.
func handleTenantBarrierSeal(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierSeal); err != nil {
			return authzToolError(err), nil
		}

		if err := svc.TenantBarrierSeal(ctx, tenantID); err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_seal", Err: err}
		}

		return mcp.NewToolResultText(`{"sealed":true}`), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_status
// ---------------------------------------------------------------------------

// toolTenantBarrierStatus defines the qrdb_tenant_barrier_status tool schema.
func toolTenantBarrierStatus() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_status",
		mcp.WithDescription("Report the current sealed/initialized state and active strategy of a per-tenant barrier."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier status should be returned."),
		),
	)
}

// handleTenantBarrierStatus returns a tool handler that reports the current
// state of the per-tenant barrier as a JSON-encoded TenantBarrierStatus.
func handleTenantBarrierStatus(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierStatus); err != nil {
			return authzToolError(err), nil
		}

		status, err := svc.TenantBarrierStatus(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_status", Err: err}
		}

		data, jsonErr := json.Marshal(status)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_officer_register
// ---------------------------------------------------------------------------

// toolTenantBarrierOfficerRegister defines the qrdb_tenant_barrier_officer_register
// tool schema.
func toolTenantBarrierOfficerRegister() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_officer_register",
		mcp.WithDescription("Register a Security Officer for a per-tenant Shamir barrier. The officer's CSR is signed and their share is sealed into a ceremony enclave pending their claim."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier the officer is being registered for."),
		),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The unique username for the Security Officer."),
		),
		mcp.WithString("csr_pem",
			mcp.Required(),
			mcp.Description("PEM-encoded Certificate Signing Request for the officer's mTLS client certificate."),
		),
		mcp.WithString("authenticator_type",
			mcp.Description("Authenticator type for the officer (e.g. 'software', 'yubikey', 'tpm2'). Defaults to 'software'."),
		),
		mcp.WithString("authenticator_ref",
			mcp.Description("Opaque reference string identifying the officer's authenticator device. Optional."),
		),
	)
}

// handleTenantBarrierOfficerRegister returns a tool handler that registers a
// Security Officer for the per-tenant Shamir barrier.
func handleTenantBarrierOfficerRegister(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_officer_register"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierOfficerRegister); err != nil {
			return authzToolError(err), nil
		}

		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		csrPEM := mcp.ParseString(req, "csr_pem", "")
		if csrPEM == "" {
			return mcp.NewToolResultError("csr_pem is required"), nil
		}

		result, err := svc.TenantBarrierOfficerRegister(ctx, tenantID, &ceremony.RegisterOfficerRequest{
			Username:          username,
			CSRPEM:            []byte(csrPEM),
			AuthenticatorType: ceremony.AuthenticatorType(mcp.ParseString(req, "authenticator_type", "")),
			AuthenticatorRef:  mcp.ParseString(req, "authenticator_ref", ""),
		})
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		// INV-5: CertPEM is fine to return (public); share bytes are never in result.
		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_officer_list
// ---------------------------------------------------------------------------

// toolTenantBarrierOfficerList defines the qrdb_tenant_barrier_officer_list
// tool schema.
func toolTenantBarrierOfficerList() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_officer_list",
		mcp.WithDescription("List enrolled Security Officers for a per-tenant Shamir barrier, including their claim status."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier officers should be listed."),
		),
	)
}

// handleTenantBarrierOfficerList returns a tool handler that lists enrolled
// Security Officers for the per-tenant barrier.
func handleTenantBarrierOfficerList(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_officer_list"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierOfficerList); err != nil {
			return authzToolError(err), nil
		}

		officers, err := svc.TenantBarrierOfficerList(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		data, jsonErr := json.Marshal(officers)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_share_claim_begin
// ---------------------------------------------------------------------------

// toolTenantBarrierShareClaimBegin defines the qrdb_tenant_barrier_share_claim_begin
// tool schema.
func toolTenantBarrierShareClaimBegin() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_share_claim_begin",
		mcp.WithDescription("Begin a Security Officer share-claim for a per-tenant barrier. Issues a challenge nonce that the officer must sign with their registered key to complete the claim."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier share the officer is claiming."),
		),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The Security Officer's username."),
		),
	)
}

// handleTenantBarrierShareClaimBegin returns a tool handler that begins the
// per-tenant barrier share claim flow for the named Security Officer.
// The caller must present a valid mTLS client certificate; its thumbprint is
// bound to the challenge and verified during CompleteClaimShare.
func handleTenantBarrierShareClaimBegin(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_share_claim_begin"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		// Identity-gated (permission.IdentityGated): the registered officer's
		// mTLS certificate is the credential. The service layer enforces the
		// binding authorization (registry membership + thumbprint match +
		// nonce-signature verification); officers are not RBAC users.
		if err := authorize(ctx, tenantID, permission.IdentityGated); err != nil {
			return authzToolError(err), nil
		}

		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, tool)
		if err != nil {
			return nil, err
		}

		challenge, err := svc.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		// INV-5: return only the nonce hex and expiry; never share bytes.
		resp := map[string]any{
			"nonce_hex":  hex.EncodeToString(challenge.Nonce),
			"expires_at": challenge.ExpiresAt.Unix(),
		}
		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_share_claim_complete
// ---------------------------------------------------------------------------

// toolTenantBarrierShareClaimComplete defines the qrdb_tenant_barrier_share_claim_complete
// tool schema.
func toolTenantBarrierShareClaimComplete() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_share_claim_complete",
		mcp.WithDescription("Complete a Security Officer share-claim for a per-tenant barrier by submitting the signed challenge nonce. Returns the officer's encrypted share bytes on success."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier share the officer is claiming."),
		),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The Security Officer's username."),
		),
		mcp.WithString("nonce_hex",
			mcp.Required(),
			mcp.Description("The hex-encoded challenge nonce returned by qrdb_tenant_barrier_share_claim_begin."),
		),
		mcp.WithString("signature",
			mcp.Required(),
			mcp.Description("Base64-encoded signature of the nonce produced with the officer's registered private key."),
		),
	)
}

// handleTenantBarrierShareClaimComplete returns a tool handler that completes
// the per-tenant barrier share claim flow. The caller must present the same
// mTLS client certificate used in Begin; on success the officer's share bytes
// are returned (INV-1/INV-11: only here, only once).
func handleTenantBarrierShareClaimComplete(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_share_claim_complete"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		// Identity-gated (permission.IdentityGated): the registered officer's
		// mTLS certificate is the credential. The service layer enforces the
		// binding authorization (registry membership + thumbprint match +
		// nonce-signature verification); officers are not RBAC users.
		if err := authorize(ctx, tenantID, permission.IdentityGated); err != nil {
			return authzToolError(err), nil
		}

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
			return mcp.NewToolResultError("signature must be valid base64: " + decErr.Error()), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, tool)
		if err != nil {
			return nil, err
		}

		// INV-1/INV-11: share bytes are delivered only in this response,
		// only to the claiming officer's own authenticated session.
		result, err := svc.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_officer_claim_status
// ---------------------------------------------------------------------------

// toolTenantBarrierOfficerClaimStatus defines the qrdb_tenant_barrier_officer_claim_status
// tool schema.
func toolTenantBarrierOfficerClaimStatus() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_officer_claim_status",
		mcp.WithDescription("Return per-officer share-claim status for all enrolled Security Officers on a per-tenant barrier."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier officer claim statuses should be returned."),
		),
	)
}

// handleTenantBarrierOfficerClaimStatus returns a tool handler that reports
// the share-claim status for all enrolled Security Officers.
func handleTenantBarrierOfficerClaimStatus(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_officer_claim_status"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierOfficerList); err != nil {
			return authzToolError(err), nil
		}

		statuses, err := svc.TenantBarrierOfficerClaimStatus(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		data, jsonErr := json.Marshal(statuses)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_shamir_seal_vote
// ---------------------------------------------------------------------------

// toolTenantBarrierShamirSealVote defines the qrdb_tenant_barrier_shamir_seal_vote
// tool schema.
func toolTenantBarrierShamirSealVote() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_shamir_seal_vote",
		mcp.WithDescription("Submit a Shamir seal vote (share) toward the quorum required to seal a per-tenant Shamir barrier. The barrier seals once enough distinct shares are submitted."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose Shamir barrier should receive the seal vote."),
		),
		mcp.WithString("share",
			mcp.Required(),
			mcp.Description("A single Shamir share value contributed toward the seal-vote quorum."),
		),
	)
}

// handleTenantBarrierShamirSealVote returns a tool handler that submits a
// Shamir seal-vote share for the per-tenant barrier.
func handleTenantBarrierShamirSealVote(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_shamir_seal_vote"

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		if err := authorize(ctx, tenantID, permission.TenantBarrierSealVote); err != nil {
			return authzToolError(err), nil
		}

		share := mcp.ParseString(req, "share", "")
		if share == "" {
			return mcp.NewToolResultError("share is required"), nil
		}

		progress, err := svc.TenantBarrierShamirSealVote(ctx, tenantID, share)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: tool, Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}
