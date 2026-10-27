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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	toolNameTenantBarrierSignedInitBegin    = "qrdb_tenant_barrier_signed_init_begin"
	toolNameTenantBarrierSignedInitComplete = "qrdb_tenant_barrier_signed_init_complete"
)

// toolTenantBarrierSignedInitBegin defines the
// qrdb_tenant_barrier_signed_init_begin tool.
func toolTenantBarrierSignedInitBegin() mcp.Tool {
	return mcp.NewTool(toolNameTenantBarrierSignedInitBegin,
		mcp.WithDescription("Begin a two-phase signed per-tenant barrier init: returns the base64 epoch-1 init statement for the first admin to sign away from the node, and a base64 single-use reference, valid for five minutes, that qrdb_tenant_barrier_signed_init_complete redeems with the signature. Tenant must already exist."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier should be initialized."),
		),
		mcp.WithString("public_key",
			mcp.Required(),
			mcp.Description("Base64 DER SubjectPublicKeyInfo of the first admin's key: ECDSA, RSA or Ed25519."),
		),
		mcp.WithString("strategy",
			mcp.Description("Seal strategy the statement names. Defaults to 'passphrase' when empty."),
		),
		mcp.WithString("key_ref",
			mcp.Description("Key reference a hardware or KMS strategy seals under. Optional."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Shamir threshold. Optional; 0 selects defaults."),
		),
		mcp.WithNumber("shares",
			mcp.Description("Total number of Shamir shares. Optional; 0 selects defaults."),
		),
		mcp.WithString("officers",
			mcp.Description("Inline Shamir share-holder roster, JSON-encoded as for qrdb_tenant_barrier_init_complete. Refused on any strategy but shamir."),
		),
		mcp.WithString("policy",
			mcp.Description("Factor policy the officers roster enrols under: designated (default) or any-enrolled."),
		),
	)
}

// handleTenantBarrierSignedInitBegin returns the statement to sign and the
// reference that redeems it.
func handleTenantBarrierSignedInitBegin(svc tenant.TenantServicer, authorize func(ctx context.Context, tenantID string) error) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		publicKey, err := initBytesArg(req, "public_key")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var roster *tenant.TenantBarrierShamirRoster
		if officersRaw := mcp.ParseString(req, "officers", ""); officersRaw != "" {
			var officers []transport.OfficerRegisterRequest
			if err := json.Unmarshal([]byte(officersRaw), &officers); err != nil {
				return mcp.NewToolResultError((&ToolArgumentError{Argument: "officers", Reason: "must be a JSON array: " + err.Error()}).Error()), nil
			}
			roster = &tenant.TenantBarrierShamirRoster{
				Officers: holderwire.TenantBarrierRosterFrom(officers),
				Policy:   seal.FactorPolicy(mcp.ParseString(req, "policy", "")),
			}
		}
		if err := authorize(ctx, tenantID); err != nil {
			return authzToolError(err), nil
		}
		begun, err := svc.TenantBarrierInitBegin(ctx, &tenant.TenantBarrierInitBeginRequest{
			TenantID:        tenantID,
			Strategy:        seal.StrategyID(mcp.ParseString(req, "strategy", "")),
			KeyRef:          mcp.ParseString(req, "key_ref", ""),
			Threshold:       mcp.ParseInt(req, "threshold", 0),
			Shares:          mcp.ParseInt(req, "shares", 0),
			Roster:          roster,
			SignerPublicKey: publicKey,
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameTenantBarrierSignedInitBegin, Err: err}
		}
		data, err := json.Marshal(transport.TenantBarrierSignedInitBeginResponse{
			TenantID:    begun.TenantID,
			Strategy:    begun.Strategy,
			Reference:   begun.Reference,
			Statement:   begun.Statement,
			ExpiresAt:   begun.ExpiresAt,
			Threshold:   begun.Threshold,
			TotalShares: begun.TotalShares,
			ShareCount:  begun.ShareCount,
			Holders:     holderwire.TenantBarrierHoldersTo(begun.Holders),
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameTenantBarrierSignedInitBegin, Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantBarrierSignedInitComplete defines the
// qrdb_tenant_barrier_signed_init_complete tool.
func toolTenantBarrierSignedInitComplete() mcp.Tool {
	return mcp.NewTool(toolNameTenantBarrierSignedInitComplete,
		mcp.WithDescription("Complete a two-phase signed per-tenant barrier init: redeems the reference from qrdb_tenant_barrier_signed_init_begin with the first admin's detached signature over its statement and initializes the barrier under that statement. A reference is redeemed at most once."),
		mcp.WithString("tenant_id",
			mcp.Required(),
			mcp.Description("The tenant whose barrier should be initialized."),
		),
		mcp.WithString("reference",
			mcp.Required(),
			mcp.Description("Base64 reference qrdb_tenant_barrier_signed_init_begin returned."),
		),
		mcp.WithString("signature",
			mcp.Required(),
			mcp.Description("Base64 detached signature over the statement: ECDSA (ASN.1) or RSA PKCS#1 v1.5 over its SHA-256, or pure Ed25519."),
		),
		mcp.WithString("passphrase",
			mcp.Description("The caller-supplied passphrase sealing the tenant barrier. Required for the passphrase strategy."),
		),
	)
}

// handleTenantBarrierSignedInitComplete redeems a begun signed init.
func handleTenantBarrierSignedInitComplete(svc tenant.TenantServicer, authorize func(ctx context.Context, tenantID string) error) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if tenantID == "" {
			return mcp.NewToolResultError("tenant_id is required"), nil
		}
		reference, err := initBytesArg(req, "reference")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		signature, err := initBytesArg(req, "signature")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := authorize(ctx, tenantID); err != nil {
			return authzToolError(err), nil
		}
		passphrase, err := tenant.NewSignedInitPassphrase(secretArg(req, "passphrase"))
		if err != nil {
			return nil, &ToolError{Tool: toolNameTenantBarrierSignedInitComplete, Err: err}
		}
		defer passphrase.Destroy()
		result, err := svc.TenantBarrierInitComplete(ctx, &tenant.TenantBarrierInitCompleteRequest{
			TenantID:   tenantID,
			Reference:  reference,
			Signature:  signature,
			Passphrase: passphrase,
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameTenantBarrierSignedInitComplete, Err: err}
		}
		return tenantBarrierInitToolResult(toolNameTenantBarrierSignedInitComplete, result)
	}
}
