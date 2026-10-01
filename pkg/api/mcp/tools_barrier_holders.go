// Copyright (c) 2026 Jeremy Hahn
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
	"encoding/hex"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mcpCertThumbprint returns the verified TLS client certificate's
// thumbprint; the share claim requires one.
func mcpCertThumbprint(ctx context.Context, toolName string) (string, error) {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return "", &ToolError{Tool: toolName, Err: &service.ClientCertRequiredError{}}
	}
	return mtls.Thumbprint(tlsState.PeerCertificates[0].Raw), nil
}

// mcpOptionalThumbprint returns the verified TLS client certificate's
// thumbprint, or "" when none was presented. A quorum submission does not
// require mTLS — the factor proof authenticates the holder — but when a
// certificate is present the mtls factor binds to it.
func mcpOptionalThumbprint(ctx context.Context) string {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return ""
	}
	return mtls.Thumbprint(tlsState.PeerCertificates[0].Raw)
}

// decodeHolderArgs maps a share-holder tool's arguments onto its request.
func decodeHolderArgs(tool string, req mcp.CallToolRequest, v any) error {
	if err := wire.DecodeArgs(req.GetArguments(), v, wire.HolderJSONFields...); err != nil {
		return &ToolError{Tool: tool, Err: err}
	}
	return nil
}

// holderResult renders a share-holder tool's result as JSON text.
func holderResult(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}

// Shared argument declarations of the holder tool schemas.
var (
	argUsername = mcp.WithString("username", mcp.Required(), mcp.Description("The share holder's username."))
	argDomain   = mcp.WithString("domain", mcp.Required(), mcp.Description("The key the round is over: unseal or recovery."))
	argPolicy   = mcp.WithString("policy", mcp.Description("Factor policy of the new share set: designated (default) or any-enrolled."))
	argSOPin    = mcp.WithString("so_pin", mcp.Description("SO PIN verified against the node's PIN backend."))
	argTenantID = mcp.WithString("tenant_id", mcp.Required(), mcp.Description("The tenant whose barrier the call is for."))
)

// holderRegisterArgs are the enrolment arguments shared by the system and
// tenant registration tools.
func holderRegisterArgs() []mcp.ToolOption {
	return []mcp.ToolOption{
		argUsername,
		mcp.WithString("role", mcp.Description("Holder role: so (default) or custodian.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("PEM certificate signing request. Only its public key is used: the node's CA assigns the certificate identity.")),
		mcp.WithString("encryption_kem", mcp.Description("HPKE suite of a separate share-envelope key: dhkem-p256, dhkem-p384, dhkem-x25519 or mlkem768-x25519.")),
		mcp.WithString("encryption_key", mcp.Description("Base64 of the separate share-envelope public key in that suite's encoding.")),
		mcp.WithString("factors", mcp.Description("JSON array of additional factor enrolments: [{\"name\":...,\"params\":{...}}].")),
		mcp.WithString("designated", mcp.Description("The factor fixed at enrolment under the designated policy; mtls when empty.")),
	}
}

// holderClaimCompleteArgs are the claim-complete arguments.
func holderClaimCompleteArgs() []mcp.ToolOption {
	return []mcp.ToolOption{
		argUsername,
		mcp.WithString("nonce_hex", mcp.Required(), mcp.Description("Nonce hex from the claim-begin response.")),
		mcp.WithString("signature", mcp.Required(), mcp.Description("Base64 signature over the nonce by the holder's certificate key.")),
	}
}

// quorumSubmitArgs are the quorum submission arguments.
func quorumSubmitArgs() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("op", mcp.Required(), mcp.Description("The operation: unseal, seal, root-token, rekey or recover.")),
		argUsername,
		mcp.WithString("challenge_id", mcp.Required(), mcp.Description("The challenge id from quorum-begin.")),
		mcp.WithString("factor", mcp.Required(), mcp.Description("The factor that answered the challenge.")),
		mcp.WithString("response", mcp.Required(), mcp.Description("Base64 factor response to the challenge.")),
		mcp.WithString("response_metadata", mcp.Description("JSON object of factor response metadata.")),
		mcp.WithString("share", mcp.Required(), mcp.Description("Base64 share recovered from the holder's envelope.")),
	}
}

// withArgs builds a tool from a name, description and argument groups.
func withArgs(name, description string, groups ...[]mcp.ToolOption) mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(description)}
	for _, g := range groups {
		opts = append(opts, g...)
	}
	return mcp.NewTool(name, opts...)
}

// barrierHolderTools returns the system-barrier share-holder tools.
func barrierHolderTools(svc service.BarrierServicer) []serverTool {
	return []serverTool{
		{Tool: withArgs("qrdb_barrier_officer_register", "Enrol a share holder (Security Officer or key custodian) of the unseal key.", holderRegisterArgs()),
			Perm: permission.SealOfficerRegister, Handler: handleHolderRegister(svc, seal.DomainUnseal, "qrdb_barrier_officer_register")},
		{Tool: withArgs("qrdb_barrier_recovery_officer_register", "Enrol a holder of the recovery key.", holderRegisterArgs()),
			Perm: permission.SealOfficerRegister, Handler: handleHolderRegister(svc, seal.DomainRecovery, "qrdb_barrier_recovery_officer_register")},
		{Tool: mcp.NewTool("qrdb_barrier_officer_list", mcp.WithDescription("List the unseal key's share holders and their claim state.")),
			Perm: permission.SealOfficerList, Handler: handleHolderList(svc)},
		{Tool: withArgs("qrdb_barrier_share_claim_begin", "Issue a claim nonce to an unseal-key holder. Requires the holder's mTLS client certificate.", []mcp.ToolOption{argUsername}),
			Perm: permission.IdentityGated, Handler: handleHolderClaimBegin(svc, seal.DomainUnseal, "qrdb_barrier_share_claim_begin")},
		{Tool: withArgs("qrdb_barrier_share_claim_complete", "Verify the holder's nonce signature and deliver its sealed share envelope, once. Requires the holder's mTLS client certificate.", holderClaimCompleteArgs()),
			Perm: permission.IdentityGated, Handler: handleHolderClaimComplete(svc, seal.DomainUnseal, "qrdb_barrier_share_claim_complete")},
		{Tool: withArgs("qrdb_barrier_recovery_share_claim_begin", "Issue a claim nonce to a recovery-key holder. Requires the holder's mTLS client certificate.", []mcp.ToolOption{argUsername}),
			Perm: permission.IdentityGated, Handler: handleHolderClaimBegin(svc, seal.DomainRecovery, "qrdb_barrier_recovery_share_claim_begin")},
		{Tool: withArgs("qrdb_barrier_recovery_share_claim_complete", "Deliver a recovery-key holder's sealed share envelope, once. Requires the holder's mTLS client certificate.", holderClaimCompleteArgs()),
			Perm: permission.IdentityGated, Handler: handleHolderClaimComplete(svc, seal.DomainRecovery, "qrdb_barrier_recovery_share_claim_complete")},
		{Tool: withArgs("qrdb_barrier_quorum_begin", "Issue the factor challenge a holder answers to submit a share. Reachable while sealed.", []mcp.ToolOption{
			argDomain,
			mcp.WithString("op", mcp.Required(), mcp.Description("The operation: unseal, seal, root-token, rekey or recover.")),
			argUsername,
			mcp.WithString("factor", mcp.Description("The factor to answer with; the designated factor when empty.")),
		}), Perm: permission.IdentityGated, Handler: handleQuorumBegin(svc)},
		{Tool: withArgs("qrdb_barrier_quorum_submit", "Submit one authenticated holder's share toward the open round. Each holder counts once. Reachable while sealed.", []mcp.ToolOption{argDomain}, quorumSubmitArgs()),
			Perm: permission.IdentityGated, Handler: handleQuorumSubmit(svc)},
		{Tool: withArgs("qrdb_barrier_rekey_start", "Open a rekey round; it completes when a fresh M-of-N holder quorum submits to the rekey op.", []mcp.ToolOption{
			argDomain,
			mcp.WithNumber("threshold", mcp.Required(), mcp.Description("Threshold of the new share set.")),
			argPolicy, argSOPin,
		}), Perm: permission.SealRekey, Handler: handleRekeyStart(svc)},
		{Tool: withArgs("qrdb_barrier_officer_revoke", "Revoke a holder through a quorum-gated rekey that drops it atomically.", []mcp.ToolOption{
			argDomain, argUsername,
			mcp.WithNumber("threshold", mcp.Description("Threshold of the remaining share set; the current threshold when zero.")),
			argSOPin,
		}), Perm: permission.SealOfficerRevoke, Handler: handleOfficerRevoke(svc)},
	}
}

// handleHolderRegister enrols a holder of domain.
func handleHolderRegister(svc service.BarrierServicer, domain seal.QuorumDomain, tool string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in transport.OfficerRegisterRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		result, err := svc.BarrierOfficerRegister(ctx, domain, holderwire.RegisterFrom(&in))
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, holderwire.RegisterTo(result))
	}
}

// handleHolderList lists the unseal key's holders.
func handleHolderList(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_barrier_officer_list"
		officers, err := svc.BarrierOfficerList(ctx, seal.DomainUnseal)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, map[string]any{"officers": holderwire.StatusesTo(officers)})
	}
}

// handleHolderClaimBegin issues a claim nonce to a holder of domain.
func handleHolderClaimBegin(svc service.BarrierServicer, domain seal.QuorumDomain, tool string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in transport.ShareClaimBeginRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		thumbprint, err := mcpCertThumbprint(ctx, tool)
		if err != nil {
			return nil, err
		}
		challenge, err := svc.BarrierShareClaimBegin(ctx, domain, in.Username, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt})
	}
}

// handleHolderClaimComplete delivers a holder's sealed envelope, once.
func handleHolderClaimComplete(svc service.BarrierServicer, domain seal.QuorumDomain, tool string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in transport.ShareClaimCompleteRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		thumbprint, err := mcpCertThumbprint(ctx, tool)
		if err != nil {
			return nil, err
		}
		result, err := svc.BarrierShareClaimComplete(ctx, domain, in.Username, in.NonceHex, in.Signature, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope})
	}
}

// handleQuorumBegin issues a quorum challenge.
func handleQuorumBegin(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_barrier_quorum_begin"
		var in transport.QuorumBeginRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		challenge, err := svc.BarrierQuorumBegin(ctx, holderwire.BeginFrom(&in))
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, holderwire.ChallengeTo(challenge))
	}
}

// handleQuorumSubmit counts one authenticated holder's share. The TLS peer
// certificate, when present, is taken from the connection.
func handleQuorumSubmit(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_barrier_quorum_submit"
		var in transport.QuorumSubmitRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		defer secure.Wipe(in.Share)
		out, err := svc.BarrierQuorumSubmit(ctx, holderwire.SubmitFrom(&in, mcpOptionalThumbprint(ctx)))
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, holderwire.OutcomeTo(out))
	}
}

// handleRekeyStart opens a rekey round under SO PIN authorization, which the
// barrier service verifies.
func handleRekeyStart(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_barrier_rekey_start"
		var in transport.RekeyStartRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		round, err := svc.BarrierRekeyStart(ctx, seal.QuorumDomain(in.Domain), in.Threshold, seal.FactorPolicy(in.Policy), in.SOPin)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, holderwire.RoundTo(round))
	}
}

// handleOfficerRevoke revokes a holder under SO PIN authorization, which the
// barrier service verifies.
func handleOfficerRevoke(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_barrier_officer_revoke"
		var in transport.OfficerRevokeRequest
		if err := decodeHolderArgs(tool, req, &in); err != nil {
			return nil, err
		}
		round, err := svc.BarrierOfficerRevoke(ctx, seal.QuorumDomain(in.Domain), in.Username, in.Threshold, in.SOPin)
		if err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return holderResult(tool, holderwire.RoundTo(round))
	}
}

// ---------------------------------------------------------------------------
// Tenant barrier share holders
// ---------------------------------------------------------------------------

// tenantHolderTools returns the tenant-barrier share-holder tools. Every
// handler authorizes against the tenant named in its arguments.
func tenantHolderTools(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) []serverTool {
	tenantArgs := []mcp.ToolOption{argTenantID}
	return []serverTool{
		{Tool: withArgs("qrdb_tenant_barrier_officer_register", "Enrol a share holder of a tenant barrier.", tenantArgs, holderRegisterArgs()),
			Perm: permission.TenantBarrierOfficerRegister, Handler: handleTenantHolderRegister(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_officer_list", "List a tenant barrier's share holders and their claim state.", tenantArgs),
			Perm: permission.TenantBarrierOfficerList, Handler: handleTenantHolderList(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_share_claim_begin", "Issue a claim nonce to a tenant holder. Requires the holder's mTLS client certificate.", tenantArgs, []mcp.ToolOption{argUsername}),
			Perm: permission.IdentityGated, Handler: handleTenantHolderClaimBegin(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_share_claim_complete", "Deliver a tenant holder's sealed share envelope, once. Requires the holder's mTLS client certificate.", tenantArgs, holderClaimCompleteArgs()),
			Perm: permission.IdentityGated, Handler: handleTenantHolderClaimComplete(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_quorum_begin", "Issue a tenant holder's quorum challenge.", tenantArgs, []mcp.ToolOption{
			mcp.WithString("op", mcp.Required(), mcp.Description("The operation: unseal, seal or rekey.")),
			argUsername,
			mcp.WithString("factor", mcp.Description("The factor to answer with; the designated factor when empty.")),
		}), Perm: permission.IdentityGated, Handler: handleTenantQuorumBegin(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_quorum_submit", "Submit one authenticated tenant holder's share toward the open round.", tenantArgs, quorumSubmitArgs()),
			Perm: permission.IdentityGated, Handler: handleTenantQuorumSubmit(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_rekey_start", "Open a rekey round on a tenant barrier.", tenantArgs, []mcp.ToolOption{
			mcp.WithNumber("threshold", mcp.Required(), mcp.Description("Threshold of the new share set.")),
			argPolicy,
		}), Perm: permission.TenantBarrierRekey, Handler: handleTenantRekeyStart(svc, authorize)},
		{Tool: withArgs("qrdb_tenant_barrier_officer_revoke", "Revoke a tenant holder through a quorum-gated rekey.", tenantArgs, []mcp.ToolOption{
			argUsername,
			mcp.WithNumber("threshold", mcp.Description("Threshold of the remaining share set; the current threshold when zero.")),
		}), Perm: permission.TenantBarrierOfficerRevoke, Handler: handleTenantOfficerRevoke(svc, authorize)},
	}
}

// tenantHolderCall decodes a tenant holder tool's arguments into in,
// authorizes the call against in's tenant, and renders run's result.
func tenantHolderCall(ctx context.Context, req mcp.CallToolRequest, tool string, perm permission.Permission, authorize tenantScopeAuthorizer, in interface{ tenant() string }, run func() (any, error)) (*mcp.CallToolResult, error) {
	if err := decodeHolderArgs(tool, req, in); err != nil {
		return nil, err
	}
	if in.tenant() == "" {
		return mcp.NewToolResultError("tenant_id is required"), nil
	}
	if err := authorize(ctx, in.tenant(), perm); err != nil {
		return authzToolError(err), nil
	}
	out, err := run()
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return holderResult(tool, out)
}

// Tenant tool arguments name their tenant through one accessor so
// tenantHolderCall can authorize any of them.
type (
	tenantRegisterArgs struct {
		transport.TenantOfficerRegisterRequest
	}
	tenantListArgs struct {
		TenantID string `json:"tenant_id"`
	}
	tenantClaimBeginArgs struct {
		transport.TenantShareClaimBeginRequest
	}
	tenantClaimCompleteArgs struct {
		transport.TenantShareClaimCompleteRequest
	}
	tenantQuorumBeginArgs struct {
		transport.TenantQuorumBeginRequest
	}
	tenantQuorumSubmitArgs struct {
		transport.TenantQuorumSubmitRequest
	}
	tenantRekeyStartArgs struct {
		transport.TenantRekeyStartRequest
	}
	tenantRevokeArgs struct {
		transport.TenantOfficerRevokeRequest
	}
)

func (a *tenantRegisterArgs) tenant() string      { return a.TenantID }
func (a *tenantListArgs) tenant() string          { return a.TenantID }
func (a *tenantClaimBeginArgs) tenant() string    { return a.TenantID }
func (a *tenantClaimCompleteArgs) tenant() string { return a.TenantID }
func (a *tenantQuorumBeginArgs) tenant() string   { return a.TenantID }
func (a *tenantQuorumSubmitArgs) tenant() string  { return a.TenantID }
func (a *tenantRekeyStartArgs) tenant() string    { return a.TenantID }
func (a *tenantRevokeArgs) tenant() string        { return a.TenantID }

// handleTenantHolderRegister enrols a tenant holder.
func handleTenantHolderRegister(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantRegisterArgs{}
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_officer_register", permission.TenantBarrierOfficerRegister, authorize, in, func() (any, error) {
			reg := service.HolderRegistration(ctx, holderwire.TenantRegisterFrom(&in.TenantOfficerRegisterRequest))
			result, err := svc.TenantBarrierOfficerRegister(ctx, in.TenantID, &reg)
			if err != nil {
				return nil, err
			}
			return holderwire.TenantRegisterTo(result), nil
		})
	}
}

// handleTenantHolderList lists a tenant barrier's holders.
func handleTenantHolderList(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantListArgs{}
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_officer_list", permission.TenantBarrierOfficerList, authorize, in, func() (any, error) {
			officers, err := svc.TenantBarrierOfficerList(ctx, in.TenantID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"officers": holderwire.StatusesTo(officers)}, nil
		})
	}
}

// handleTenantHolderClaimBegin issues a claim nonce to a tenant holder.
func handleTenantHolderClaimBegin(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_share_claim_begin"
		in := &tenantClaimBeginArgs{}
		return tenantHolderCall(ctx, req, tool, permission.IdentityGated, authorize, in, func() (any, error) {
			thumbprint, err := mcpCertThumbprint(ctx, tool)
			if err != nil {
				return nil, err
			}
			challenge, err := svc.TenantBarrierShareClaimBegin(ctx, in.TenantID, in.Username, thumbprint)
			if err != nil {
				return nil, err
			}
			return transport.ShareClaimBeginResponse{NonceHex: hex.EncodeToString(challenge.Nonce), ExpiresAt: challenge.ExpiresAt}, nil
		})
	}
}

// handleTenantHolderClaimComplete delivers a tenant holder's envelope once.
func handleTenantHolderClaimComplete(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_tenant_barrier_share_claim_complete"
		in := &tenantClaimCompleteArgs{}
		return tenantHolderCall(ctx, req, tool, permission.IdentityGated, authorize, in, func() (any, error) {
			thumbprint, err := mcpCertThumbprint(ctx, tool)
			if err != nil {
				return nil, err
			}
			result, err := svc.TenantBarrierShareClaimComplete(ctx, in.TenantID, in.Username, in.NonceHex, in.Signature, thumbprint)
			if err != nil {
				return nil, err
			}
			return transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
		})
	}
}

// handleTenantQuorumBegin issues a tenant holder's quorum challenge.
func handleTenantQuorumBegin(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantQuorumBeginArgs{}
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_quorum_begin", permission.IdentityGated, authorize, in, func() (any, error) {
			challenge, err := svc.TenantBarrierQuorumBegin(ctx, in.TenantID, seal.QuorumOp(in.Op), in.Username, in.Factor)
			if err != nil {
				return nil, err
			}
			return holderwire.ChallengeTo(challenge), nil
		})
	}
}

// handleTenantQuorumSubmit counts one authenticated tenant holder's share.
func handleTenantQuorumSubmit(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantQuorumSubmitArgs{}
		defer func() { secure.Wipe(in.Share) }()
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_quorum_submit", permission.IdentityGated, authorize, in, func() (any, error) {
			out, err := svc.TenantBarrierQuorumSubmit(ctx, in.TenantID, holderwire.TenantSubmitFrom(&in.TenantQuorumSubmitRequest, mcpOptionalThumbprint(ctx)))
			if err != nil {
				return nil, err
			}
			return holderwire.OutcomeTo(service.QuorumOutcomeFrom(out)), nil
		})
	}
}

// handleTenantRekeyStart opens a rekey round on a tenant barrier.
func handleTenantRekeyStart(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantRekeyStartArgs{}
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_rekey_start", permission.TenantBarrierRekey, authorize, in, func() (any, error) {
			round, err := svc.TenantBarrierRekeyStart(ctx, in.TenantID, in.Threshold, seal.FactorPolicy(in.Policy))
			if err != nil {
				return nil, err
			}
			return holderwire.RoundTo(round), nil
		})
	}
}

// handleTenantOfficerRevoke revokes a tenant holder.
func handleTenantOfficerRevoke(svc tenant.TenantServicer, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &tenantRevokeArgs{}
		return tenantHolderCall(ctx, req, "qrdb_tenant_barrier_officer_revoke", permission.TenantBarrierOfficerRevoke, authorize, in, func() (any, error) {
			round, err := svc.TenantBarrierOfficerRevoke(ctx, in.TenantID, in.Username, in.Threshold)
			if err != nil {
				return nil, err
			}
			return holderwire.RoundTo(round), nil
		})
	}
}
