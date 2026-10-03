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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Shamir custody of a database barrier (docs/databases/api.md). Each tool
// names the database; the database service confines the call to a database
// in the caller's tenant, as it does for qrdb_database_seal.

// argDatabase names the database whose barrier a holder tool is for.
var argDatabase = mcp.WithString("database", mcp.Required(), mcp.Description("The database whose barrier the call is for."))

// databaseHolderTools returns the database-barrier share-holder tools.
func databaseHolderTools(dbSvc service.DatabaseServicer) []serverTool {
	dbArgs := []mcp.ToolOption{argDatabase}
	return []serverTool{
		{Tool: withArgs("qrdb_database_barrier_officer_register", "Enrol a share holder of a database's Shamir barrier.", dbArgs, holderRegisterArgs()),
			Perm: permission.DatabasesOfficerRegister, Handler: handleDatabaseHolderRegister(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_officer_list", "List a database barrier's share holders and their claim state.", dbArgs),
			Perm: permission.DatabasesOfficerList, Handler: handleDatabaseHolderList(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_shamir_initialize", "Split a database's root key to its enrolled holders at the M-of-N it was created with.", dbArgs, []mcp.ToolOption{argPolicy}),
			Perm: permission.DatabasesInitializeShamir, Handler: handleDatabaseShamirInitialize(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_share_claim_begin", "Issue a claim nonce to a database holder. Requires the holder's mTLS client certificate.", dbArgs, []mcp.ToolOption{argUsername}),
			Perm: permission.IdentityGated, Handler: handleDatabaseHolderClaimBegin(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_share_claim_complete", "Deliver a database holder's sealed share envelope, once. Requires the holder's mTLS client certificate.", dbArgs, holderClaimCompleteArgs()),
			Perm: permission.IdentityGated, Handler: handleDatabaseHolderClaimComplete(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_quorum_begin", "Issue a database holder's quorum challenge.", dbArgs, []mcp.ToolOption{
			mcp.WithString("op", mcp.Required(), mcp.Description("The operation: unseal, seal or rekey.")),
			argUsername,
			mcp.WithString("factor", mcp.Description("The factor to answer with; the designated factor when empty.")),
		}), Perm: permission.IdentityGated, Handler: handleDatabaseQuorumBegin(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_quorum_submit", "Submit one authenticated database holder's share toward the open round.", dbArgs, quorumSubmitArgs()),
			Perm: permission.IdentityGated, Handler: handleDatabaseQuorumSubmit(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_rekey_start", "Open a rekey round on a database barrier.", dbArgs, []mcp.ToolOption{
			mcp.WithNumber("threshold", mcp.Required(), mcp.Description("Threshold of the new share set.")),
			argPolicy,
		}), Perm: permission.DatabasesRekey, Handler: handleDatabaseRekeyStart(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_officer_revoke", "Revoke a database holder through a quorum-gated rekey.", dbArgs, []mcp.ToolOption{
			argUsername,
			mcp.WithNumber("threshold", mcp.Description("Threshold of the remaining share set; the current threshold when zero.")),
		}), Perm: permission.DatabasesOfficerRevoke, Handler: handleDatabaseOfficerRevoke(dbSvc)},
		{Tool: withArgs("qrdb_database_barrier_officer_unenrol", "Remove a database holder before the database's custody is initialized, when it holds no share.", dbArgs, []mcp.ToolOption{argUsername}),
			Perm: permission.DatabasesOfficerUnenrol, Handler: handleDatabaseOfficerUnenrol(dbSvc)},
	}
}

// databaseHolderCall decodes a database holder tool's arguments into in,
// resolves the database service's share-holder custody, and renders run's
// result.
func databaseHolderCall(req mcp.CallToolRequest, tool string, dbSvc service.DatabaseServicer, in interface{ database() string }, run func(hs service.DatabaseHolderServicer) (any, error)) (*mcp.CallToolResult, error) {
	if err := decodeHolderArgs(tool, req, in); err != nil {
		return nil, err
	}
	if in.database() == "" {
		return mcp.NewToolResultError("database is required"), nil
	}
	hs, ok := dbSvc.(service.DatabaseHolderServicer)
	if !ok {
		return nil, &ToolError{Tool: tool, Err: &ServiceNotConfiguredError{Service: "database share holders"}}
	}
	out, err := run(hs)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return holderResult(tool, out)
}

// Database tool arguments name their database through one accessor so
// databaseHolderCall can refuse a call that names none.
type (
	databaseRegisterArgs struct {
		transport.DatabaseOfficerRegisterRequest
	}
	databaseListArgs struct {
		Database string `json:"database"`
	}
	databaseInitializeArgs struct {
		transport.DatabaseShamirInitializeRequest
	}
	databaseClaimBeginArgs struct {
		transport.DatabaseShareClaimBeginRequest
	}
	databaseClaimCompleteArgs struct {
		transport.DatabaseShareClaimCompleteRequest
	}
	databaseQuorumBeginArgs struct {
		transport.DatabaseQuorumBeginRequest
	}
	databaseQuorumSubmitArgs struct {
		transport.DatabaseQuorumSubmitRequest
	}
	databaseRekeyStartArgs struct {
		transport.DatabaseRekeyStartRequest
	}
	databaseRevokeArgs struct {
		transport.DatabaseOfficerRevokeRequest
	}
	databaseUnenrolArgs struct {
		transport.DatabaseOfficerUnenrolRequest
	}
)

func (a *databaseRegisterArgs) database() string      { return a.Database }
func (a *databaseListArgs) database() string          { return a.Database }
func (a *databaseInitializeArgs) database() string    { return a.Database }
func (a *databaseClaimBeginArgs) database() string    { return a.Database }
func (a *databaseClaimCompleteArgs) database() string { return a.Database }
func (a *databaseQuorumBeginArgs) database() string   { return a.Database }
func (a *databaseQuorumSubmitArgs) database() string  { return a.Database }
func (a *databaseRekeyStartArgs) database() string    { return a.Database }
func (a *databaseUnenrolArgs) database() string       { return a.Database }
func (a *databaseRevokeArgs) database() string        { return a.Database }

// handleDatabaseHolderRegister enrols a database holder.
func handleDatabaseHolderRegister(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseRegisterArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_officer_register", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			result, err := hs.DatabaseBarrierOfficerRegister(ctx, in.Database, holderwire.RegisterFrom(&in.OfficerRegisterRequest))
			if err != nil {
				return nil, err
			}
			return holderwire.RegisterTo(result), nil
		})
	}
}

// handleDatabaseHolderList lists a database barrier's holders.
func handleDatabaseHolderList(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseListArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_officer_list", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			officers, err := hs.DatabaseBarrierOfficerList(ctx, in.Database)
			if err != nil {
				return nil, err
			}
			return map[string]any{"officers": holderwire.StatusesTo(officers)}, nil
		})
	}
}

// handleDatabaseShamirInitialize splits a database's root key to its holders.
func handleDatabaseShamirInitialize(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseInitializeArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_shamir_initialize", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			info, err := hs.DatabaseBarrierShamirInitialize(ctx, in.Database, seal.FactorPolicy(in.Policy))
			if err != nil {
				return nil, err
			}
			return transport.BarrierInitializeShamirResponse{Threshold: info.Threshold, TotalShares: info.TotalShares, ShareCount: info.ShareCount}, nil
		})
	}
}

// handleDatabaseHolderClaimBegin issues a claim nonce to a database holder.
func handleDatabaseHolderClaimBegin(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_database_barrier_share_claim_begin"
		in := &databaseClaimBeginArgs{}
		return databaseHolderCall(req, tool, dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			thumbprint, err := mcpCertThumbprint(ctx, tool)
			if err != nil {
				return nil, err
			}
			challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, in.Database, in.Username, thumbprint)
			if err != nil {
				return nil, err
			}
			return transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt}, nil
		})
	}
}

// handleDatabaseHolderClaimComplete delivers a database holder's envelope
// once.
func handleDatabaseHolderClaimComplete(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_database_barrier_share_claim_complete"
		in := &databaseClaimCompleteArgs{}
		return databaseHolderCall(req, tool, dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			thumbprint, err := mcpCertThumbprint(ctx, tool)
			if err != nil {
				return nil, err
			}
			result, err := hs.DatabaseBarrierShareClaimComplete(ctx, in.Database, in.Username, in.NonceHex, in.Signature, thumbprint)
			if err != nil {
				return nil, err
			}
			return transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
		})
	}
}

// handleDatabaseQuorumBegin issues a database holder's quorum challenge.
func handleDatabaseQuorumBegin(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseQuorumBeginArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_quorum_begin", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			challenge, err := hs.DatabaseBarrierQuorumBegin(ctx, in.Database, holderwire.BeginFrom(&in.QuorumBeginRequest))
			if err != nil {
				return nil, err
			}
			return holderwire.ChallengeTo(challenge), nil
		})
	}
}

// handleDatabaseQuorumSubmit counts one authenticated database holder's
// share. The decoded share is destroyed on every path.
func handleDatabaseQuorumSubmit(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseQuorumSubmitArgs{}
		defer func() { secure.Wipe(in.Share) }()
		return databaseHolderCall(req, "qrdb_database_barrier_quorum_submit", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			out, err := hs.DatabaseBarrierQuorumSubmit(ctx, in.Database, holderwire.SubmitFrom(&in.QuorumSubmitRequest, mcpOptionalThumbprint(ctx)))
			if err != nil {
				return nil, err
			}
			return holderwire.OutcomeTo(out), nil
		})
	}
}

// handleDatabaseRekeyStart opens a rekey round on a database barrier.
func handleDatabaseRekeyStart(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseRekeyStartArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_rekey_start", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			round, err := hs.DatabaseBarrierRekeyStart(ctx, in.Database, in.Threshold, seal.FactorPolicy(in.Policy))
			if err != nil {
				return nil, err
			}
			return holderwire.RoundTo(round), nil
		})
	}
}

// handleDatabaseOfficerRevoke revokes a database holder.
func handleDatabaseOfficerRevoke(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseRevokeArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_officer_revoke", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			round, err := hs.DatabaseBarrierOfficerRevoke(ctx, in.Database, in.Username, in.Threshold)
			if err != nil {
				return nil, err
			}
			return holderwire.RoundTo(round), nil
		})
	}
}

// databaseUnenrolResult reports the holder an unenrolment removed.
type databaseUnenrolResult struct {
	Unenrolled string `json:"unenrolled"`
}

// handleDatabaseOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func handleDatabaseOfficerUnenrol(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := &databaseUnenrolArgs{}
		return databaseHolderCall(req, "qrdb_database_barrier_officer_unenrol", dbSvc, in, func(hs service.DatabaseHolderServicer) (any, error) {
			if err := hs.DatabaseBarrierOfficerUnenrol(ctx, in.Database, in.Username); err != nil {
				return nil, err
			}
			return databaseUnenrolResult{Unenrolled: in.Username}, nil
		})
	}
}
