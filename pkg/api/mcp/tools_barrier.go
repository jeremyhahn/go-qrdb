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

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// barrierTools returns the MCP tool registrations for barrier lifecycle
// operations including initialization, seal/unseal, Shamir share management,
// rekey, recovery, and root token generation.
//
// pinBackend is optional: when supplied, handleBarrierInitialize forwards
// the so_pin / user_pin tool arguments to the backend before invoking the
// barrier service, and handleBarrierRekey / handleBarrierRecoveryGenerate
// verify the SO PIN. When nil the PIN-related arguments are ignored,
// preserving dev-mode and unconfigured-deployment flows.
func barrierTools(svc service.BarrierServicer, pinBackend pin.PINBackend) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolBarrierInitialize(), Handler: handleBarrierInitialize(svc, pinBackend)},
		{Tool: toolBarrierUnseal(), Handler: handleBarrierUnseal(svc)},
		{Tool: toolBarrierSeal(), Handler: handleBarrierSeal(svc, pinBackend)},
		{Tool: toolBarrierStatus(), Handler: handleBarrierStatus(svc)},
		{Tool: toolBarrierShamirInitialize(), Handler: handleBarrierShamirInitialize(svc, pinBackend)},
		{Tool: toolBarrierShamirUnsealShare(), Handler: handleBarrierShamirUnsealShare(svc)},
		{Tool: toolBarrierShamirUnsealShares(), Handler: handleBarrierShamirUnsealShares(svc)},
		{Tool: toolBarrierRekey(), Handler: handleBarrierRekey(svc, pinBackend)},
		{Tool: toolBarrierRecoveryGenerate(), Handler: handleBarrierRecoveryGenerate(svc, pinBackend)},
		{Tool: toolBarrierRecoveryRecover(), Handler: handleBarrierRecoveryRecover(svc)},
		{Tool: toolBarrierRecoveryDelete(), Handler: handleBarrierRecoveryDelete(svc)},
		{Tool: toolBarrierRecoveryStatus(), Handler: handleBarrierRecoveryStatus(svc)},
		{Tool: toolBarrierRootToken(), Handler: handleBarrierRootToken(svc)},
	}
}

// toolBarrierInitialize defines the qrdb_barrier_initialize tool schema.
// so_pin and user_pin are optional and feed the configured pin.PINBackend
// when supplied.
func toolBarrierInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_initialize",
		mcp.WithDescription("Initialize the cryptographic barrier with a secret. Optionally supply so_pin and user_pin to provision the PINBackend in the same call."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("Secret for barrier initialization (strategy wrapping passphrase)."),
		),
		mcp.WithString("so_pin",
			mcp.Description("Optional Security Officer PIN routed to the configured pin.PINBackend."),
		),
		mcp.WithString("user_pin",
			mcp.Description("Optional User PIN routed to the configured pin.PINBackend."),
		),
	)
}

// handleBarrierInitialize returns a tool handler that delegates to
// svc.BarrierInitialize. When pinBackend is non-nil and the request
// supplies so_pin / user_pin, the PINs are persisted on the backend
// before the barrier is initialized.
func handleBarrierInitialize(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret := mcp.ParseString(req, "secret", "")
		if secret == "" {
			return mcp.NewToolResultError("secret is required"), nil
		}

		soPIN := mcp.ParseString(req, "so_pin", "")
		userPIN := mcp.ParseString(req, "user_pin", "")
		if err := applyBarrierInitializePINs(pinBackend, soPIN, userPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_initialize", Err: err}
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
// so_pin is optional and verified against the configured pin.PINBackend
// when one is set; when no backend is configured the argument is ignored.
func toolBarrierSeal() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_seal",
		mcp.WithDescription("Seal the cryptographic barrier, transitioning it to the sealed state."),
		mcp.WithString("so_pin",
			mcp.Description("Optional SO PIN verified against the configured pin.PINBackend."),
		),
	)
}

// handleBarrierSeal returns a tool handler that delegates to
// svc.BarrierSeal. SO PIN verification is enforced when pinBackend is
// non-nil and has a SO PIN set.
func handleBarrierSeal(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		soPIN := mcp.ParseString(req, "so_pin", "")
		if err := verifyBarrierSOPIN(pinBackend, soPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_seal", Err: err}
		}

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
// tool schema. so_pin and user_pin are optional and feed the configured
// pin.PINBackend when supplied.
func toolBarrierShamirInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_initialize",
		mcp.WithDescription("Initialize the barrier with Shamir secret sharing, splitting the root key into shares. Optionally supply so_pin and user_pin to provision the PINBackend in the same call."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("Secret for Shamir barrier initialization (strategy wrapping passphrase)."),
		),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("Minimum number of shares required to reconstruct the root key."),
		),
		mcp.WithNumber("total_shares",
			mcp.Required(),
			mcp.Description("Total number of Shamir shares to generate."),
		),
		mcp.WithString("so_pin",
			mcp.Description("Optional Security Officer PIN routed to the configured pin.PINBackend."),
		),
		mcp.WithString("user_pin",
			mcp.Description("Optional User PIN routed to the configured pin.PINBackend."),
		),
	)
}

// handleBarrierShamirInitialize returns a tool handler that delegates to
// svc.BarrierInitializeShamir. When pinBackend is non-nil and the request
// supplies so_pin / user_pin, the PINs are persisted on the backend before
// initialization.
func handleBarrierShamirInitialize(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
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

		soPIN := mcp.ParseString(req, "so_pin", "")
		userPIN := mcp.ParseString(req, "user_pin", "")
		if err := applyBarrierInitializePINs(pinBackend, soPIN, userPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_initialize", Err: err}
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

// toolBarrierRekey defines the qrdb_barrier_rekey tool schema.
// so_pin is optional and verified against the configured pin.PINBackend.
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
		mcp.WithString("so_pin",
			mcp.Description("Optional SO PIN verified against the configured pin.PINBackend."),
		),
	)
}

// handleBarrierRekey returns a tool handler that delegates to
// svc.BarrierRekey. SO PIN verification is enforced when pinBackend is
// non-nil and has a SO PIN set.
func handleBarrierRekey(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		total := mcp.ParseInt(req, "total", 0)
		if total <= 0 {
			return mcp.NewToolResultError("total is required and must be positive"), nil
		}

		soPIN := mcp.ParseString(req, "so_pin", "")
		if err := verifyBarrierSOPIN(pinBackend, soPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_rekey", Err: err}
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
// tool schema. so_pin is optional and verified against the configured
// pin.PINBackend.
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
		mcp.WithString("so_pin",
			mcp.Description("Optional SO PIN verified against the configured pin.PINBackend."),
		),
	)
}

// handleBarrierRecoveryGenerate returns a tool handler that delegates to
// svc.BarrierGenerateRecoveryKeys. SO PIN verification is enforced when
// pinBackend is non-nil and has a SO PIN set.
func handleBarrierRecoveryGenerate(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		total := mcp.ParseInt(req, "total", 0)
		if total <= 0 {
			return mcp.NewToolResultError("total is required and must be positive"), nil
		}

		soPIN := mcp.ParseString(req, "so_pin", "")
		if err := verifyBarrierSOPIN(pinBackend, soPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_generate", Err: err}
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
