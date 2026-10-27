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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
// (seal, officer management); InitBegin and Init are the trust-on-first-init
// gate with its proof of possession, and Sealed the sealed-window gate — all
// route through the shared cross-transport rules in pkg/auth/middleware
// (BeginTenantBarrierInit / AuthorizeTenantBarrierInitProof /
// AuthorizeTenantBarrierSealedOp) so the fallback semantics cannot drift
// from REST, gRPC, and QUIC.
type tenantBarrierAuthorizers struct {
	Op        tenantScopeAuthorizer
	InitBegin func(ctx context.Context, tenantID string) (*initproof.Challenge, error)
	Init      func(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error
	// SignedInit authorizes either half of a two-phase signed init: as Init,
	// without the connection-bound proof the signed statement replaces.
	SignedInit func(ctx context.Context, tenantID string) error
	Sealed     tenantScopeAuthorizer
}

// tenantBarrierTools returns the MCP tool registrations for per-tenant
// barrier lifecycle: init, unseal, seal, status, and the share-holder tools.
// Every handler authorizes against the target tenant parsed from the tool
// arguments before delegating to the tenant service. Init/Unseal/Status
// register as IdentityGated at the tool layer because
// their RBAC check runs inside the handlers with the bootstrap-window
// fallbacks (see tenantBarrierAuthorizers).
func tenantBarrierTools(svc tenant.TenantServicer, authz tenantBarrierAuthorizers) []serverTool {
	authorize := authz.Op
	tools := []serverTool{
		{Tool: toolTenantBarrierInitBegin(), Perm: permission.IdentityGated, Handler: handleTenantBarrierInitBegin(authz.InitBegin)},
		{Tool: toolTenantBarrierInit(), Perm: permission.IdentityGated, Handler: handleTenantBarrierInit(svc, authz.Init)},
		{Tool: toolTenantBarrierSignedInitBegin(), Perm: permission.IdentityGated, Handler: handleTenantBarrierSignedInitBegin(svc, authz.SignedInit)},
		{Tool: toolTenantBarrierSignedInitComplete(), Perm: permission.IdentityGated, Handler: handleTenantBarrierSignedInitComplete(svc, authz.SignedInit)},
		{Tool: toolTenantBarrierUnseal(), Perm: permission.IdentityGated, Handler: handleTenantBarrierUnseal(svc, authz.Sealed)},
		{Tool: toolTenantBarrierSeal(), Perm: permission.TenantBarrierSeal, Handler: handleTenantBarrierSeal(svc, authorize)},
		{Tool: toolTenantBarrierStatus(), Perm: permission.IdentityGated, Handler: handleTenantBarrierStatus(svc, authz.Sealed)},
	}
	return append(tools, tenantHolderTools(svc, authorize)...)
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_init_begin
// ---------------------------------------------------------------------------

// toolTenantBarrierInitBegin defines the qrdb_tenant_barrier_init_begin tool
// schema.
func toolTenantBarrierInitBegin() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_init_begin",
		mcp.WithDescription("Begin initializing a per-tenant barrier: returns a base64 nonce, valid only on this connection, that qrdb_tenant_barrier_init_complete must carry signed by the first admin's key together with this connection's TLS exporter. Both fields are empty on an unauthenticated deployment, which asks no proof."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier is to be initialized."),
		),
	)
}

// handleTenantBarrierInitBegin returns a tool handler that issues the init
// nonce through begin.
func handleTenantBarrierInitBegin(begin func(ctx context.Context, tenantID string) (*initproof.Challenge, error)) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		challenge, err := begin(ctx, tenantID)
		if err != nil {
			return authzToolError(err), nil
		}
		resp := map[string]any{"nonce": base64.StdEncoding.EncodeToString(challenge.Nonce)}
		if !challenge.ExpiresAt.IsZero() {
			resp["expires"] = challenge.ExpiresAt.Unix()
		}
		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_init_begin", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// qrdb_tenant_barrier_init_complete
// ---------------------------------------------------------------------------

// toolTenantBarrierInit defines the qrdb_tenant_barrier_init_complete tool
// schema.
func toolTenantBarrierInit() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_barrier_init_complete",
		mcp.WithDescription("Initialize a per-tenant barrier, given proof of possession of the first admin's key over this connection (see qrdb_tenant_barrier_init_begin). Tenant must already exist."),
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
		mcp.WithString("officers",
			mcp.Description("Inline Shamir share-holder roster, JSON-encoded: an array of {\"username\":..., \"role\":\"so\"|\"custodian\", \"csr_pem\":..., \"encryption_kem\":..., \"encryption_key\":..., \"factors\":[...], \"designated\":...} objects, e.g. '[{\"username\":\"alice\",\"role\":\"so\",\"csr_pem\":\"...\"}]'. Validated and registered atomically with initialization; refused on any strategy but shamir. Its length must equal shares."),
		),
		mcp.WithString("policy",
			mcp.Description("Factor policy the officers roster enrols under: designated (default) or any-enrolled. Ignored when officers is empty."),
		),
		mcp.WithString("nonce",
			mcp.Description("Base64 nonce from qrdb_tenant_barrier_init_begin on this connection."),
		),
		mcp.WithString("public_key",
			mcp.Description("Base64 DER SubjectPublicKeyInfo of the first admin's key."),
		),
		mcp.WithString("signature",
			mcp.Description("Base64 signature by that key over the tenant id, the nonce and this connection's TLS exporter."),
		),
		mcp.WithString("init_statement",
			mcp.Description("Base64 epoch-1 barrier init statement signed by the same key. Required on a clustered node once per-node-barrier-keys is active; every replica initializes its own barrier key from it."),
		),
		mcp.WithString("roster",
			mcp.Description("Base64 encoded holder roster the first request of a Shamir init returned. With unseal_key_check it marks the second request, which completes that prepared split."),
		),
		mcp.WithString("unseal_key_check",
			mcp.Description("Base64 unseal key check value the first request of a Shamir init returned, echoed with roster by the second request."),
		),
	)
}

// initStatementArg decodes the base64 init_statement argument of
// qrdb_tenant_barrier_init_complete; an absent argument decodes to nil.
func initStatementArg(req mcp.CallToolRequest) ([]byte, error) {
	return initBytesArg(req, "init_statement")
}

// initBytesArg decodes the base64 argument name of
// qrdb_tenant_barrier_init_complete; an absent argument decodes to nil.
func initBytesArg(req mcp.CallToolRequest, name string) ([]byte, error) {
	encoded := mcp.ParseString(req, name, "")
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, &ToolArgumentError{Argument: name, Reason: "must be valid base64: " + err.Error()}
	}
	return decoded, nil
}

// initProofArgs decodes the base64 proof-of-possession arguments of
// qrdb_tenant_barrier_init_complete; an absent argument decodes to nil.
func initProofArgs(req mcp.CallToolRequest) (*authmw.TenantBarrierInitProof, error) {
	proof := &authmw.TenantBarrierInitProof{}
	fields := map[string]*[]byte{
		"nonce":      &proof.Nonce,
		"public_key": &proof.PublicKey,
		"signature":  &proof.Signature,
	}
	for name, dst := range fields {
		encoded := mcp.ParseString(req, name, "")
		if encoded == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, &ToolArgumentError{Argument: name, Reason: "must be valid base64: " + err.Error()}
		}
		*dst = decoded
	}
	return proof, nil
}

// handleTenantBarrierInit returns a tool handler that initializes the
// per-tenant barrier for the given tenant, given proof of possession of the
// first admin's key. When the strategy field is empty or omitted the
// passphrase (software) strategy is selected.
func handleTenantBarrierInit(svc tenant.TenantServicer, authorize func(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		proof, err := initProofArgs(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		statement, err := initStatementArg(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		splitRoster, err := initBytesArg(req, "roster")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		splitCheck, err := initBytesArg(req, "unseal_key_check")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := authorize(ctx, tenantID, proof); err != nil {
			return authzToolError(err), nil
		}

		passphrase := secretArg(req, "passphrase")
		defer secure.Wipe(passphrase)
		threshold := mcp.ParseInt(req, "threshold", 0)
		shares := mcp.ParseInt(req, "shares", 0)
		strategy := mcp.ParseString(req, "strategy", "")

		var roster *tenant.TenantBarrierShamirRoster
		if officersRaw := mcp.ParseString(req, "officers", ""); officersRaw != "" {
			var officers []transport.OfficerRegisterRequest
			if err := json.Unmarshal([]byte(officersRaw), &officers); err != nil {
				return nil, &ToolError{Tool: "qrdb_tenant_barrier_init_complete", Err: err}
			}
			roster = &tenant.TenantBarrierShamirRoster{
				Officers: holderwire.TenantBarrierRosterFrom(officers),
				Policy:   seal.FactorPolicy(mcp.ParseString(req, "policy", "")),
			}
		}

		result, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, passphrase, threshold, shares, seal.StrategyID(strategy), roster,
			tenant.WithInitStatement(statement),
			tenant.WithPreparedShamir(splitRoster, splitCheck))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_barrier_init_complete", Err: err}
		}

		// INV-1/INV-11: shares are NEVER returned in init responses.
		// Security Officers claim their individual share via the SO claim flow.
		// A Shamir split's roster and check value are returned, base64, for
		// the owner to sign between the two requests of a Shamir init.
		return tenantBarrierInitToolResult("qrdb_tenant_barrier_init_complete", result)
	}
}

// tenantBarrierInitToolResult renders an initialized barrier as tool's text
// result. INV-1/INV-11: it carries no share bytes.
func tenantBarrierInitToolResult(tool string, result *tenant.TenantBarrierInitResult) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(map[string]any{
		"tenant_id":        result.TenantID,
		"strategy":         result.Strategy,
		"threshold":        result.Threshold,
		"total_shares":     result.TotalShares,
		"share_count":      result.ShareCount,
		"holders":          holderwire.TenantBarrierHoldersTo(result.Holders),
		"roster":           result.Roster,
		"unseal_key_check": result.UnsealKeyCheck,
		"admin_pending":    result.AdminPending,
	})
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
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
