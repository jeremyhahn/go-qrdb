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
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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
		// Security Officer distribution tools.
		{Tool: toolBarrierOfficerRegister(), Handler: handleBarrierOfficerRegister(svc)},
		{Tool: toolBarrierOfficerList(), Handler: handleBarrierOfficerList(svc)},
		{Tool: toolBarrierShareClaimBegin(), Handler: handleBarrierShareClaimBegin(svc)},
		{Tool: toolBarrierShareClaimComplete(), Handler: handleBarrierShareClaimComplete(svc)},
		{Tool: toolBarrierOfficerClaimStatus(), Handler: handleBarrierOfficerClaimStatus(svc)},
		{Tool: toolBarrierShamirSealVote(), Handler: handleBarrierShamirSealVote(svc)},
		{Tool: toolBarrierRecoveryOfficerRegister(), Handler: handleBarrierRecoveryOfficerRegister(svc)},
		{Tool: toolBarrierRecoveryShareClaimBegin(), Handler: handleBarrierRecoveryShareClaimBegin(svc)},
		{Tool: toolBarrierRecoveryShareClaimComplete(), Handler: handleBarrierRecoveryShareClaimComplete(svc)},
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
// svc.BarrierStatus. The result is rendered with the canonical snake_case
// wire keys (sealed, strategy, hardware_backed, initialized_at,
// shamir_threshold, shamir_total) so SDK clients can decode it into
// transport.BarrierStatusResponse, matching the REST representation.
func handleBarrierStatus(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status, err := svc.BarrierStatus(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_status", Err: err}
		}

		resp := map[string]any{
			"sealed":          status.Sealed,
			"strategy":        status.Strategy,
			"hardware_backed": status.HardwareBacked,
		}
		if !status.InitializedAt.IsZero() {
			resp["initialized_at"] = status.InitializedAt.Format(time.RFC3339)
		}
		if status.ShamirThreshold > 0 {
			resp["shamir_threshold"] = status.ShamirThreshold
		}
		if status.ShamirTotal > 0 {
			resp["shamir_total"] = status.ShamirTotal
		}

		data, jsonErr := json.Marshal(resp)
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

		data, jsonErr := json.Marshal(shamirInitInfoToWire(info))
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

		data, jsonErr := json.Marshal(shamirInitInfoToWire(info))
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

		data, jsonErr := json.Marshal(map[string]any{
			"threshold":   info.Threshold,
			"total":       info.Total,
			"share_count": info.ShareCount,
		})
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

		data, jsonErr := json.Marshal(map[string]any{
			"token":      info.Token,
			"created_at": info.CreatedAt.Format(time.RFC3339),
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_root_token", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// shamirInitInfoToWire converts a ShamirInitInfo to the canonical snake_case
// wire keys (threshold, total_shares, share_count) shared with the REST
// representation so SDK clients can decode the result into
// transport.BarrierInitializeShamirResponse / BarrierRekeyResponse.
func shamirInitInfoToWire(info *service.ShamirInitInfo) map[string]any {
	return map[string]any{
		"threshold":    info.Threshold,
		"total_shares": info.TotalShares,
		"share_count":  info.ShareCount,
	}
}

// ---------------------------------------------------------------------------
// Barrier Security Officer MCP tools
// ---------------------------------------------------------------------------

// mcpCertThumbprint extracts the mTLS peer certificate thumbprint from the
// context. Returns a ToolError when no peer certificate is present (INV-5:
// no share bytes, nonces, or signatures in any log line).
func mcpCertThumbprint(ctx context.Context, toolName string) (string, error) {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return "", &ToolError{Tool: toolName, Err: &mcpClientCertRequiredError{}}
	}
	return ceremony.CertThumbprint(tlsState.PeerCertificates[0].Raw), nil
}

// mcpClientCertRequiredError is returned when a share-claim MCP tool requires
// a verified mTLS client certificate but the connection carries none.
type mcpClientCertRequiredError struct{}

// Error returns a human-readable description.
func (e *mcpClientCertRequiredError) Error() string {
	return "mcp: client certificate required for share claim"
}

// toolBarrierOfficerRegister defines the qrdb_barrier_officer_register tool.
func toolBarrierOfficerRegister() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_officer_register",
		mcp.WithDescription("Register a Security Officer for the system barrier."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Unique identifier for the officer.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("PEM-encoded certificate signing request.")),
		mcp.WithString("authenticator_type", mcp.Description("Authenticator type (software, piv, fido2).")),
		mcp.WithString("authenticator_ref", mcp.Description("Opaque reference within the authenticator.")),
	)
}

// handleBarrierOfficerRegister returns a tool handler that registers a Security
// Officer for the system barrier.
func handleBarrierOfficerRegister(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		csrPEM := mcp.ParseString(req, "csr_pem", "")
		if csrPEM == "" {
			return mcp.NewToolResultError("csr_pem is required"), nil
		}

		result, err := svc.BarrierOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
			Username:          username,
			CSRPEM:            []byte(csrPEM),
			AuthenticatorType: mcp.ParseString(req, "authenticator_type", ""),
			AuthenticatorRef:  mcp.ParseString(req, "authenticator_ref", ""),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_register", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{
			"cert_pem":    string(result.CertPEM),
			"share_index": result.ShareIndex,
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_register", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierOfficerList defines the qrdb_barrier_officer_list tool.
func toolBarrierOfficerList() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_officer_list",
		mcp.WithDescription("List all enrolled Security Officers for the system barrier."),
	)
}

// handleBarrierOfficerList returns a tool handler that lists system barrier officers.
func handleBarrierOfficerList(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		officers, err := svc.BarrierOfficerList(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_list", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{"officers": officers})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_list", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShareClaimBegin defines the qrdb_barrier_share_claim_begin tool.
func toolBarrierShareClaimBegin() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_share_claim_begin",
		mcp.WithDescription("Issue a challenge nonce to a Security Officer for the system barrier share claim handshake. Requires mTLS client certificate."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Officer username.")),
	)
}

// handleBarrierShareClaimBegin returns a tool handler that issues a challenge nonce.
func handleBarrierShareClaimBegin(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_barrier_share_claim_begin")
		if err != nil {
			return nil, err
		}

		challenge, err := svc.BarrierShareClaimBegin(ctx, username, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_share_claim_begin", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{
			"nonce_hex":  challenge.NonceHex,
			"expires_at": challenge.ExpiresAt,
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_share_claim_begin", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShareClaimComplete defines the qrdb_barrier_share_claim_complete tool.
func toolBarrierShareClaimComplete() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_share_claim_complete",
		mcp.WithDescription("Verify the officer's nonce signature and retrieve their system barrier share. Requires mTLS client certificate. INV-11: share bytes returned only once."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Officer username.")),
		mcp.WithString("nonce_hex", mcp.Required(), mcp.Description("Nonce hex from claim-begin response.")),
		mcp.WithString("signature", mcp.Required(), mcp.Description("Base64-encoded signature over the nonce.")),
	)
}

// handleBarrierShareClaimComplete returns a tool handler that completes the share
// claim handshake (INV-11: share bytes returned exactly once).
func handleBarrierShareClaimComplete(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		nonceHex := mcp.ParseString(req, "nonce_hex", "")
		if nonceHex == "" {
			return mcp.NewToolResultError("nonce_hex is required"), nil
		}
		sigStr := mcp.ParseString(req, "signature", "")
		if sigStr == "" {
			return mcp.NewToolResultError("signature is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_barrier_share_claim_complete")
		if err != nil {
			return nil, err
		}

		sigBytes, decErr := base64.StdEncoding.DecodeString(sigStr)
		if decErr != nil {
			return mcp.NewToolResultError("signature must be valid base64"), nil
		}

		result, err := svc.BarrierShareClaimComplete(ctx, username, nonceHex, sigBytes, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_share_claim_complete", Err: err}
		}

		// INV-11: share_bytes appears only here.
		data, jsonErr := json.Marshal(map[string]interface{}{
			"share_index": result.ShareIndex,
			"share_bytes": base64.StdEncoding.EncodeToString(result.ShareBytes),
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_share_claim_complete", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierOfficerClaimStatus defines the qrdb_barrier_officer_claim_status tool.
func toolBarrierOfficerClaimStatus() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_officer_claim_status",
		mcp.WithDescription("Return per-officer claim status for the system barrier (no share bytes)."),
	)
}

// handleBarrierOfficerClaimStatus returns a tool handler for officer claim status.
func handleBarrierOfficerClaimStatus(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		officers, err := svc.BarrierOfficerClaimStatus(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_claim_status", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{"officers": officers})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_officer_claim_status", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierShamirSealVote defines the qrdb_barrier_shamir_seal_vote tool.
func toolBarrierShamirSealVote() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_seal_vote",
		mcp.WithDescription("Submit a Shamir share as a seal vote. When the quorum threshold is met the system barrier is sealed."),
		mcp.WithString("share", mcp.Required(), mcp.Description("A Shamir share value.")),
	)
}

// handleBarrierShamirSealVote returns a tool handler that submits a seal vote.
func handleBarrierShamirSealVote(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		share := mcp.ParseString(req, "share", "")
		if share == "" {
			return mcp.NewToolResultError("share is required"), nil
		}

		progress, err := svc.BarrierShamirSealVote(ctx, share)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_seal_vote", Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_seal_vote", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRecoveryOfficerRegister defines the
// qrdb_barrier_recovery_officer_register tool.
func toolBarrierRecoveryOfficerRegister() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_officer_register",
		mcp.WithDescription("Register a Security Officer for recovery key distribution."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Unique identifier for the recovery officer.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("PEM-encoded certificate signing request.")),
		mcp.WithString("authenticator_type", mcp.Description("Authenticator type (software, piv, fido2).")),
		mcp.WithString("authenticator_ref", mcp.Description("Opaque reference within the authenticator.")),
	)
}

// handleBarrierRecoveryOfficerRegister returns a tool handler that registers a
// recovery officer.
func handleBarrierRecoveryOfficerRegister(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		csrPEM := mcp.ParseString(req, "csr_pem", "")
		if csrPEM == "" {
			return mcp.NewToolResultError("csr_pem is required"), nil
		}

		result, err := svc.BarrierRecoveryOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
			Username:          username,
			CSRPEM:            []byte(csrPEM),
			AuthenticatorType: mcp.ParseString(req, "authenticator_type", ""),
			AuthenticatorRef:  mcp.ParseString(req, "authenticator_ref", ""),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_officer_register", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{
			"cert_pem":    string(result.CertPEM),
			"share_index": result.ShareIndex,
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_officer_register", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRecoveryShareClaimBegin defines the
// qrdb_barrier_recovery_share_claim_begin tool.
func toolBarrierRecoveryShareClaimBegin() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_share_claim_begin",
		mcp.WithDescription("Issue a challenge nonce to a recovery officer for the recovery share claim handshake. Requires mTLS client certificate."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Recovery officer username.")),
	)
}

// handleBarrierRecoveryShareClaimBegin returns a tool handler that issues a
// challenge nonce to a recovery officer.
func handleBarrierRecoveryShareClaimBegin(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_barrier_recovery_share_claim_begin")
		if err != nil {
			return nil, err
		}

		challenge, err := svc.BarrierRecoveryShareClaimBegin(ctx, username, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_share_claim_begin", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]interface{}{
			"nonce_hex":  challenge.NonceHex,
			"expires_at": challenge.ExpiresAt,
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_share_claim_begin", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierRecoveryShareClaimComplete defines the
// qrdb_barrier_recovery_share_claim_complete tool.
func toolBarrierRecoveryShareClaimComplete() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_share_claim_complete",
		mcp.WithDescription("Verify a recovery officer's nonce signature and retrieve their recovery share. Requires mTLS client certificate. INV-11: share bytes returned only once."),
		mcp.WithString("username", mcp.Required(), mcp.Description("Recovery officer username.")),
		mcp.WithString("nonce_hex", mcp.Required(), mcp.Description("Nonce hex from claim-begin response.")),
		mcp.WithString("signature", mcp.Required(), mcp.Description("Base64-encoded signature over the nonce.")),
	)
}

// handleBarrierRecoveryShareClaimComplete returns a tool handler that completes
// the recovery share claim handshake (INV-11: share bytes returned exactly once).
func handleBarrierRecoveryShareClaimComplete(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		nonceHex := mcp.ParseString(req, "nonce_hex", "")
		if nonceHex == "" {
			return mcp.NewToolResultError("nonce_hex is required"), nil
		}
		sigStr := mcp.ParseString(req, "signature", "")
		if sigStr == "" {
			return mcp.NewToolResultError("signature is required"), nil
		}

		thumbprint, err := mcpCertThumbprint(ctx, "qrdb_barrier_recovery_share_claim_complete")
		if err != nil {
			return nil, err
		}

		sigBytes, decErr := base64.StdEncoding.DecodeString(sigStr)
		if decErr != nil {
			return mcp.NewToolResultError("signature must be valid base64"), nil
		}

		result, err := svc.BarrierRecoveryShareClaimComplete(ctx, username, nonceHex, sigBytes, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_share_claim_complete", Err: err}
		}

		// INV-11: share_bytes appears only here.
		data, jsonErr := json.Marshal(map[string]interface{}{
			"share_index": result.ShareIndex,
			"share_bytes": base64.StdEncoding.EncodeToString(result.ShareBytes),
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_share_claim_complete", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}
