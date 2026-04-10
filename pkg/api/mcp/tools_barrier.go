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
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// barrierTools returns the MCP tool registrations for barrier lifecycle
// operations including initialization, seal/unseal, Shamir share management,
// rekey, recovery, and root token generation.
func barrierTools(svc service.BarrierServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolBarrierInitialize(), Handler: handleBarrierInitialize(svc)},
		{Tool: toolBarrierUnseal(), Handler: handleBarrierUnseal(svc)},
		{Tool: toolBarrierSeal(), Handler: handleBarrierSeal(svc)},
		{Tool: toolBarrierStatus(), Handler: handleBarrierStatus(svc)},
		{Tool: toolBarrierShamirInitialize(), Handler: handleBarrierShamirInitialize(svc)},
		{Tool: toolBarrierShamirUnsealShare(), Handler: handleBarrierShamirUnsealShare(svc)},
		{Tool: toolBarrierShamirUnsealShares(), Handler: handleBarrierShamirUnsealShares(svc)},
		{Tool: toolBarrierShamirListShares(), Handler: handleBarrierShamirListShares(svc)},
		{Tool: toolBarrierShamirDeleteShare(), Handler: handleBarrierShamirDeleteShare(svc)},
		{Tool: toolBarrierShamirDeleteAllShares(), Handler: handleBarrierShamirDeleteAllShares(svc)},
		{Tool: toolBarrierShamirVerify(), Handler: handleBarrierShamirVerify(svc)},
		{Tool: toolBarrierRekey(), Handler: handleBarrierRekey(svc)},
		{Tool: toolBarrierRecoveryGenerate(), Handler: handleBarrierRecoveryGenerate(svc)},
		{Tool: toolBarrierRecoveryRecover(), Handler: handleBarrierRecoveryRecover(svc)},
		{Tool: toolBarrierRecoveryDelete(), Handler: handleBarrierRecoveryDelete(svc)},
		{Tool: toolBarrierRecoveryStatus(), Handler: handleBarrierRecoveryStatus(svc)},
		{Tool: toolBarrierRootToken(), Handler: handleBarrierRootToken(svc)},
	}
}

// toolBarrierInitialize defines the qrdb_barrier_initialize tool schema.
func toolBarrierInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_initialize",
		mcp.WithDescription("Initialize the cryptographic barrier with a secret."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("Secret for barrier initialization."),
		),
	)
}

// handleBarrierInitialize returns a tool handler that delegates to
// svc.BarrierInitialize.
func handleBarrierInitialize(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret := mcp.ParseString(req, "secret", "")
		if secret == "" {
			return mcp.NewToolResultError("secret is required"), nil
		}

		if err := svc.BarrierInitialize(ctx, secret); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_initialize", Err: err}
		}

		return mcp.NewToolResultText("barrier initialized"), nil
	}
}

// toolBarrierUnseal defines the qrdb_barrier_unseal tool schema.
func toolBarrierUnseal() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_unseal",
		mcp.WithDescription("Unseal the cryptographic barrier with a secret."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("Secret for barrier unsealing."),
		),
	)
}

// handleBarrierUnseal returns a tool handler that delegates to
// svc.BarrierUnseal.
func handleBarrierUnseal(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret := mcp.ParseString(req, "secret", "")
		if secret == "" {
			return mcp.NewToolResultError("secret is required"), nil
		}

		if err := svc.BarrierUnseal(ctx, secret); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_unseal", Err: err}
		}

		return mcp.NewToolResultText("barrier unsealed"), nil
	}
}

// toolBarrierSeal defines the qrdb_barrier_seal tool schema.
func toolBarrierSeal() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_seal",
		mcp.WithDescription("Seal the cryptographic barrier, transitioning it to the sealed state."),
	)
}

// handleBarrierSeal returns a tool handler that delegates to
// svc.BarrierSeal.
func handleBarrierSeal(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := svc.BarrierSeal(ctx); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_seal", Err: err}
		}

		return mcp.NewToolResultText("barrier sealed"), nil
	}
}

// toolBarrierStatus defines the qrdb_barrier_status tool schema.
func toolBarrierStatus() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_status",
		mcp.WithDescription("Return the current barrier status including seal state and strategy."),
	)
}

// handleBarrierStatus returns a tool handler that delegates to
// svc.BarrierStatus.
func handleBarrierStatus(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status, err := svc.BarrierStatus(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_status", Err: err}
		}

		data, jsonErr := json.Marshal(status)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShamirInitialize defines the qrdb_barrier_shamir_initialize
// tool schema.
func toolBarrierShamirInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_initialize",
		mcp.WithDescription("Initialize the barrier with Shamir secret sharing, splitting the root key into shares."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("Secret for Shamir barrier initialization."),
		),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("Minimum number of shares required to reconstruct the root key."),
		),
		mcp.WithNumber("total_shares",
			mcp.Required(),
			mcp.Description("Total number of Shamir shares to generate."),
		),
	)
}

// handleBarrierShamirInitialize returns a tool handler that delegates to
// svc.BarrierInitializeShamir.
func handleBarrierShamirInitialize(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret := mcp.ParseString(req, "secret", "")
		if secret == "" {
			return mcp.NewToolResultError("secret is required"), nil
		}

		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		totalShares := mcp.ParseInt(req, "total_shares", 0)
		if totalShares <= 0 {
			return mcp.NewToolResultError("total_shares is required and must be positive"), nil
		}

		info, err := svc.BarrierInitializeShamir(ctx, secret, threshold, totalShares)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_initialize", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_initialize", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShamirUnsealShare defines the qrdb_barrier_shamir_unseal_share
// tool schema.
func toolBarrierShamirUnsealShare() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_unseal_share",
		mcp.WithDescription("Submit a single Shamir share toward the unseal quorum threshold."),
		mcp.WithString("share",
			mcp.Required(),
			mcp.Description("A single Shamir share value."),
		),
	)
}

// handleBarrierShamirUnsealShare returns a tool handler that delegates to
// svc.BarrierUnsealWithShare.
func handleBarrierShamirUnsealShare(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		share := mcp.ParseString(req, "share", "")
		if share == "" {
			return mcp.NewToolResultError("share is required"), nil
		}

		progress, err := svc.BarrierUnsealWithShare(ctx, share)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_unseal_share", Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_unseal_share", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShamirUnsealShares defines the qrdb_barrier_shamir_unseal_shares
// tool schema.
func toolBarrierShamirUnsealShares() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_unseal_shares",
		mcp.WithDescription("Unseal the barrier using a complete set of Shamir shares in a single call."),
		mcp.WithString("shares",
			mcp.Required(),
			mcp.Description("Comma-separated list of Shamir shares."),
		),
	)
}

// handleBarrierShamirUnsealShares returns a tool handler that delegates to
// svc.BarrierUnsealWithShares.
func handleBarrierShamirUnsealShares(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := mcp.ParseString(req, "shares", "")
		if raw == "" {
			return mcp.NewToolResultError("shares is required"), nil
		}

		shares := strings.Split(raw, ",")
		for i := range shares {
			shares[i] = strings.TrimSpace(shares[i])
		}

		if err := svc.BarrierUnsealWithShares(ctx, shares); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_unseal_shares", Err: err}
		}

		return mcp.NewToolResultText("barrier unsealed"), nil
	}
}

// toolBarrierShamirListShares defines the qrdb_barrier_shamir_list_shares
// tool schema.
func toolBarrierShamirListShares() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_list_shares",
		mcp.WithDescription("List information about persisted Shamir shares including count, threshold, and total."),
	)
}

// handleBarrierShamirListShares returns a tool handler that delegates to
// svc.BarrierShamirListShares.
func handleBarrierShamirListShares(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		info, err := svc.BarrierShamirListShares(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_list_shares", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_list_shares", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShamirDeleteShare defines the qrdb_barrier_shamir_delete_share
// tool schema.
func toolBarrierShamirDeleteShare() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_delete_share",
		mcp.WithDescription("Delete a single Shamir share by its 1-based index."),
		mcp.WithNumber("index",
			mcp.Required(),
			mcp.Description("The 1-based index of the share to delete."),
		),
	)
}

// handleBarrierShamirDeleteShare returns a tool handler that delegates to
// svc.BarrierShamirDeleteShare.
func handleBarrierShamirDeleteShare(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		index := mcp.ParseInt(req, "index", 0)
		if index <= 0 {
			return mcp.NewToolResultError("index is required and must be positive"), nil
		}

		if err := svc.BarrierShamirDeleteShare(ctx, index); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_delete_share", Err: err}
		}

		return mcp.NewToolResultText("share deleted"), nil
	}
}

// toolBarrierShamirDeleteAllShares defines the
// qrdb_barrier_shamir_delete_all_shares tool schema.
func toolBarrierShamirDeleteAllShares() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_delete_all_shares",
		mcp.WithDescription("Delete all persisted Shamir shares."),
	)
}

// handleBarrierShamirDeleteAllShares returns a tool handler that delegates to
// svc.BarrierShamirDeleteAllShares.
func handleBarrierShamirDeleteAllShares(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := svc.BarrierShamirDeleteAllShares(ctx); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_delete_all_shares", Err: err}
		}

		return mcp.NewToolResultText("all shares deleted"), nil
	}
}

// toolBarrierShamirVerify defines the qrdb_barrier_shamir_verify tool schema.
func toolBarrierShamirVerify() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_verify",
		mcp.WithDescription("Verify the consistency of all persisted Shamir shares."),
	)
}

// handleBarrierShamirVerify returns a tool handler that delegates to
// svc.BarrierShamirVerify.
func handleBarrierShamirVerify(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := svc.BarrierShamirVerify(ctx); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_verify", Err: err}
		}

		return mcp.NewToolResultText("shares verified"), nil
	}
}

// toolBarrierRekey defines the qrdb_barrier_rekey tool schema.
func toolBarrierRekey() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_rekey",
		mcp.WithDescription("Generate new Shamir shares for the existing root key with new threshold and total parameters."),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("New minimum number of shares required for reconstruction."),
		),
		mcp.WithNumber("total",
			mcp.Required(),
			mcp.Description("New total number of shares to generate."),
		),
	)
}

// handleBarrierRekey returns a tool handler that delegates to
// svc.BarrierRekey.
func handleBarrierRekey(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		total := mcp.ParseInt(req, "total", 0)
		if total <= 0 {
			return mcp.NewToolResultError("total is required and must be positive"), nil
		}

		info, err := svc.BarrierRekey(ctx, threshold, total)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_rekey", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_rekey", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRecoveryGenerate defines the qrdb_barrier_recovery_generate
// tool schema.
func toolBarrierRecoveryGenerate() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_generate",
		mcp.WithDescription("Generate recovery keys from the root key for disaster recovery."),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("Minimum number of recovery keys required for recovery."),
		),
		mcp.WithNumber("total",
			mcp.Required(),
			mcp.Description("Total number of recovery keys to generate."),
		),
	)
}

// handleBarrierRecoveryGenerate returns a tool handler that delegates to
// svc.BarrierGenerateRecoveryKeys.
func handleBarrierRecoveryGenerate(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		total := mcp.ParseInt(req, "total", 0)
		if total <= 0 {
			return mcp.NewToolResultError("total is required and must be positive"), nil
		}

		info, err := svc.BarrierGenerateRecoveryKeys(ctx, threshold, total)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_generate", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_generate", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRecoveryRecover defines the qrdb_barrier_recovery_recover
// tool schema.
func toolBarrierRecoveryRecover() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_recover",
		mcp.WithDescription("Unseal the barrier using recovery keys."),
		mcp.WithString("keys",
			mcp.Required(),
			mcp.Description("Comma-separated list of recovery keys."),
		),
	)
}

// handleBarrierRecoveryRecover returns a tool handler that delegates to
// svc.BarrierRecoverWithKeys.
func handleBarrierRecoveryRecover(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := mcp.ParseString(req, "keys", "")
		if raw == "" {
			return mcp.NewToolResultError("keys is required"), nil
		}

		keys := strings.Split(raw, ",")
		for i := range keys {
			keys[i] = strings.TrimSpace(keys[i])
		}

		if err := svc.BarrierRecoverWithKeys(ctx, keys); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_recover", Err: err}
		}

		return mcp.NewToolResultText("barrier recovered"), nil
	}
}

// toolBarrierRecoveryDelete defines the qrdb_barrier_recovery_delete
// tool schema.
func toolBarrierRecoveryDelete() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_delete",
		mcp.WithDescription("Delete recovery key metadata from storage."),
	)
}

// handleBarrierRecoveryDelete returns a tool handler that delegates to
// svc.BarrierDeleteRecoveryKeys.
func handleBarrierRecoveryDelete(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := svc.BarrierDeleteRecoveryKeys(ctx); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_delete", Err: err}
		}

		return mcp.NewToolResultText("recovery keys deleted"), nil
	}
}

// toolBarrierRecoveryStatus defines the qrdb_barrier_recovery_status
// tool schema.
func toolBarrierRecoveryStatus() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_status",
		mcp.WithDescription("Check whether recovery key metadata exists in storage."),
	)
}

// handleBarrierRecoveryStatus returns a tool handler that delegates to
// svc.BarrierHasRecoveryKeys.
func handleBarrierRecoveryStatus(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hasKeys, err := svc.BarrierHasRecoveryKeys(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_status", Err: err}
		}

		result := struct {
			HasRecoveryKeys bool `json:"has_recovery_keys"`
		}{
			HasRecoveryKeys: hasKeys,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRootToken defines the qrdb_barrier_root_token tool schema.
func toolBarrierRootToken() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_root_token",
		mcp.WithDescription("Generate a new root token by proving knowledge of the master key through Shamir shares."),
		mcp.WithString("shares",
			mcp.Required(),
			mcp.Description("Comma-separated list of Shamir shares for root token generation."),
		),
	)
}

// handleBarrierRootToken returns a tool handler that delegates to
// svc.BarrierGenerateRootToken.
func handleBarrierRootToken(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := mcp.ParseString(req, "shares", "")
		if raw == "" {
			return mcp.NewToolResultError("shares is required"), nil
		}

		shares := strings.Split(raw, ",")
		for i := range shares {
			shares[i] = strings.TrimSpace(shares[i])
		}

		info, err := svc.BarrierGenerateRootToken(ctx, shares)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_root_token", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_root_token", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
