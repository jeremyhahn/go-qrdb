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
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// barrierTools returns the MCP tool registrations for barrier lifecycle
// operations: initialization, seal/unseal, Shamir custody, recovery, and the
// share-holder enrolment, claim and quorum tools.
//
// pinBackend is the node's SO/User PIN store: initialization provisions the
// so_pin / user_pin tool arguments through pin.Provision, and the
// SO-PIN-gated operations (seal, recovery generation, rekey start, holder
// revocation) verify through pin.RequireSOPIN. When nil those operations fail
// closed with pin.ErrBackendNotConfigured.
//
// encMigrator is optional: qrdb_barrier_enable_encryption is registered
// unconditionally (its own availability does not depend on svc being
// non-nil -- see the caller in server.go, which also registers it when
// barrierSvc is nil but encMigrator is not); handleBarrierEnableEncryption
// itself refuses with a typed error when encMigrator is nil, mirroring the
// REST/gRPC/QUIC BarrierEnableEncryption handlers rather than omitting the
// tool the way this function omits the rest of the group when svc is nil
// (go-qrdb#558).
func barrierTools(svc service.BarrierServicer, pinBackend pin.PINBackend, encMigrator service.EncryptionMigrator, authorize tenantScopeAuthorizer) []serverTool {
	tools := []serverTool{
		{Tool: toolBarrierInitialize(), Perm: permission.SealBootstrap, Handler: handleBarrierInitialize(svc, pinBackend)},
		{Tool: toolBarrierUnseal(), Perm: permission.SealUnseal, Handler: handleBarrierUnseal(svc)},
		{Tool: toolBarrierSeal(), Perm: permission.SealSeal, Handler: handleBarrierSeal(svc, pinBackend)},
		{Tool: toolBarrierStatus(), Perm: permission.SealStatus, Handler: handleBarrierStatus(svc)},
		{Tool: toolBarrierClusterUnseal(), Perm: permission.SealUnseal, Handler: handleBarrierClusterUnseal(svc)},
		{Tool: toolBarrierShamirInitialize(), Perm: permission.SealInitializeShamir, Handler: handleBarrierShamirInitialize(svc, pinBackend)},
		{Tool: toolBarrierRecoveryGenerate(), Perm: permission.SealGenerateRecovery, Handler: handleBarrierRecoveryGenerate(svc, pinBackend)},
		{Tool: toolBarrierRecoveryDelete(), Perm: permission.SealGenerateRecovery, Handler: handleBarrierRecoveryDelete(svc)},
		{Tool: toolBarrierRecoveryStatus(), Perm: permission.SealStatus, Handler: handleBarrierRecoveryStatus(svc)},
		// Registered unconditionally -- go-qrdb#558. This tool's own
		// availability depends on encMigrator, not svc; gating it on
		// encMigrator != nil here would omit it entirely in standalone mode
		// (encMigrator nil, svc non-nil) instead of letting the handler
		// answer with the typed refusal every other transport gives for
		// that state. Identity-gated: the handler authorizes per scope
		// (go-qrdb#791).
		{Tool: toolBarrierEnableEncryption(), Perm: permission.IdentityGated, Handler: handleBarrierEnableEncryption(encMigrator, authorize)},
	}
	return append(tools, barrierHolderTools(svc)...)
}

// barrierEnableEncryptionMigrationAcceptWindow bounds how long the tool
// handler waits for EncryptionMigrator.EnableEncryption to return before
// giving up on delivering its outcome synchronously. Mirrors
// migrationAcceptWindow / grpcMigrationAcceptWindow / quicMigrationAcceptWindow
// on the REST/gRPC/QUIC transports — see the REST doc comment for the full
// rationale: every synchronously-knowable rejection returns well within this
// window, before the destructive Quiesce phase (which for a system-scope
// migration recycles the serving MCP server) ever runs.
const barrierEnableEncryptionMigrationAcceptWindow = 750 * time.Millisecond

// toolBarrierEnableEncryption defines the qrdb_barrier_enable_encryption
// tool schema.
func toolBarrierEnableEncryption() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_enable_encryption",
		mcp.WithDescription("Migrate a previously-unencrypted barrier to a keyed strategy (passphrase or shamir). Runs asynchronously; poll qrdb_barrier_status for completion."),
		mcp.WithString("tenant_id",
			mcp.Description("When set, scopes the migration to that tenant's dedicated barrier; empty means the system barrier (admin scope)."),
		),
		mcp.WithString("strategy",
			mcp.Required(),
			mcp.Description("Target keyed strategy: \"passphrase\" or \"shamir\"."),
		),
		mcp.WithString("mechanism",
			mcp.Description("Migration mechanism: \"swap\" (default) or \"in-place\"."),
		),
		mcp.WithString("availability",
			mcp.Description("Availability mode during migration: \"quiesced\" (default) or \"online\"."),
		),
		withSecretArg("passphrase", "Unseal credential for a passphrase target."),
		mcp.WithNumber("shamir_threshold",
			mcp.Description("Shamir threshold (M) when strategy is \"shamir\"."),
		),
		mcp.WithNumber("shamir_total",
			mcp.Description("Shamir total share count (N) when strategy is \"shamir\"."),
		),
	)
}

// handleBarrierEnableEncryption returns a tool handler that delegates to
// migrator.EnableEncryption. Like the REST/gRPC/QUIC handlers it validates
// synchronously, then invokes the migrator and races its result against
// barrierEnableEncryptionMigrationAcceptWindow: a synchronous rejection (bad
// target, shared-barrier-immutable, insufficient officers, entitlement
// denied, provider unavailable) is returned to the caller as a real tool
// error, while a migration still running once the window elapses is
// acknowledged with "migration_started"; clients poll qrdb_barrier_status.
func handleBarrierEnableEncryption(migrator service.EncryptionMigrator, authorize tenantScopeAuthorizer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The tool is identity-gated: which permission authorizes the call
		// depends on the barrier the request names, so it is decided here,
		// before anything else. No tenant named is the node's own barrier, a
		// platform-operator act under seal:enable_encryption; a named tenant is
		// that tenant's barrier, authorized under tenant_barrier:enable_encryption
		// for THAT tenant, exactly like every other tenant-barrier operation.
		scope := service.BarrierScope{Kind: service.ScopeSystem}
		target, perm := tenantid.System, permission.SealEnableEncryption
		if tenantID := mcp.ParseString(req, "tenant_id", ""); tenantID != "" {
			scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: tenantID}
			target, perm = tenantID, permission.TenantBarrierEnableEncryption
		}
		if err := authorize(ctx, target, perm); err != nil {
			return authzToolError(err), nil
		}
		if migrator == nil {
			// Tool is registered unconditionally (go-qrdb#558); this is the
			// same typed refusal REST/gRPC/Unix/QUIC give when no
			// EncryptionMigrator is configured (standalone mode, or a
			// composer that never wired one).
			return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: service.ErrMigrationProviderUnavailable}
		}
		strategy := mcp.ParseString(req, "strategy", "")
		if strategy == "" {
			return mcp.NewToolResultError("strategy is required"), nil
		}

		mechanism := seal.MechanismSwap
		if seal.MigrationMechanism(mcp.ParseString(req, "mechanism", "")) == seal.MechanismInPlace {
			mechanism = seal.MechanismInPlace
		}
		availability := seal.AvailabilityQuiesced
		if seal.MigrationAvailability(mcp.ParseString(req, "availability", "")) == seal.AvailabilityOnline {
			availability = seal.AvailabilityOnline
		}

		// The passphrase argument is copied into protected memory and the
		// decoded copy wiped at once — the credential is what the migration
		// reads from here on.
		passphrase, err := secretArg(req, "passphrase")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: err}
		}
		creds, err := seal.CredentialsFor(ctx, passphrase)
		secure.Wipe(passphrase)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: err}
		}

		opts := seal.MigrationOptions{
			TargetStrategy:  seal.StrategyID(strategy),
			Mechanism:       mechanism,
			Availability:    availability,
			Credentials:     creds,
			ShamirThreshold: mcp.ParseInt(req, "shamir_threshold", 0),
			ShamirTotal:     mcp.ParseInt(req, "shamir_total", 0),
		}
		if err := opts.Validate(); err != nil {
			seal.DestroyCredentials(creds)
			return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: err}
		}

		// The migration runs on the request's context without its cancellation:
		// the request context is cancelled once this call returns, and the
		// migration must continue independent of its lifetime, while still
		// carrying the request's values (the caller's identity and tenant), as
		// on every transport.
		migrationCtx := context.WithoutCancel(ctx)
		resultCh := make(chan error, 1)
		go func() {
			// The migration OWNS the credential from here: it outlives this
			// tool call, so destroying it in the handler would pull the
			// passphrase out from under a running migration. The destroy
			// belongs to whoever the last reader is, and that is this
			// goroutine.
			defer seal.DestroyCredentials(creds)
			resultCh <- migrator.EnableEncryption(migrationCtx, scope, opts)
		}()

		select {
		case err := <-resultCh:
			if err != nil {
				return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: err}
			}
			// Completed within the window with no error: an unusually fast
			// migration (e.g. an empty barrier) already finished. Fall
			// through and report "migration_started" — qrdb_barrier_status
			// already reflects the committed strategy, so the wording is
			// accurate either way.
		case <-time.After(barrierEnableEncryptionMigrationAcceptWindow):
			// Still running past acceptance: no caller is left to deliver
			// the eventual outcome to, so only log it.
			go func() {
				if err := <-resultCh; err != nil {
					slog.Error("enable-encryption migration failed",
						slog.String("scope", string(scope.Kind)),
						slog.String("tenant", scope.TenantID),
						slog.String("strategy", string(opts.TargetStrategy)),
						slog.Any("error", err))
				}
			}()
		}

		data, jsonErr := json.Marshal(map[string]string{
			"status": "migration_started",
			"scope":  string(scope.Kind),
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_enable_encryption", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBarrierInitialize defines the qrdb_barrier_initialize tool schema.
// so_pin and user_pin are optional and feed the configured pin.PINBackend
// when supplied. secret is deliberately NOT mcp.Required(): the "none"
// sealing strategy (identity passthrough, no secret material) is selected
// entirely by the daemon's own boot-time seal config -- service.BarrierServicer.
// BarrierInitialize takes only a secret string with no strategy parameter,
// and every other transport (REST's handleBarrierInitialize, gRPC's
// qrdbServiceServer.BarrierInitialize) passes an empty secret straight
// through with no validation. Requiring a non-empty secret here would make
// `barrier init --barrier-strategy none` (and the root `qrdb init`
// single-admin path, which also initializes with a possibly-empty secret)
// unreachable over MCP alone.
func toolBarrierInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_initialize",
		mcp.WithDescription("Initialize the cryptographic barrier with a secret. The secret is empty for the \"none\" strategy. Optionally supply so_pin and user_pin to provision the PINBackend in the same call."),
		withSecretArg("secret", "Secret for barrier initialization (strategy wrapping passphrase). Empty for the \"none\" strategy."),
		withSecretArg("so_pin", "Optional Security Officer PIN routed to the configured pin.PINBackend."),
		withSecretArg("user_pin", "Optional User PIN routed to the configured pin.PINBackend."),
	)
}

// handleBarrierInitialize returns a tool handler that delegates to
// svc.BarrierInitialize. When pinBackend is non-nil and the request
// supplies so_pin / user_pin, the PINs are persisted on the backend
// before the barrier is initialized. secret is passed through unchanged
// (including empty) -- see toolBarrierInitialize's doc comment for why an
// empty-secret guard here would break parity with every other transport.
func handleBarrierInitialize(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret, err := secretArg(req, "secret")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_initialize", Err: err}
		}
		defer secure.Wipe(secret)

		soPIN, userPIN, err := initializePINArgs(req)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_initialize", Err: err}
		}
		if err := service.ProvisionInitializePINs(ctx, svc, pinBackend, soPIN, userPIN); err != nil {
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
		withSecretArg("secret", "Secret for barrier unsealing. Required, in exactly one of its two forms."),
	)
}

// handleBarrierUnseal returns a tool handler that delegates to
// svc.BarrierUnseal.
func handleBarrierUnseal(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		secret, err := secretArg(req, "secret")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_unseal", Err: err}
		}
		if len(secret) == 0 {
			return mcp.NewToolResultError("secret is required"), nil
		}
		defer secure.Wipe(secret)

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
		withSecretArg("so_pin", "Optional SO PIN verified against the configured pin.PINBackend."),
	)
}

// handleBarrierSeal returns a tool handler that delegates to
// svc.BarrierSeal. SO PIN verification is enforced when pinBackend is
// non-nil and has a SO PIN set.
func handleBarrierSeal(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		soPIN, err := pinArg(req, "so_pin")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_seal", Err: err}
		}
		if err := pin.RequireSOPIN(ctx, pinBackend, soPIN); err != nil {
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
			"initialized":     status.Initialized,
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
		resp["cluster_seal"] = clusterSealOrInactive(status.ClusterSeal)
		if status.ClusterMigration != nil {
			resp["cluster_migration"] = status.ClusterMigration
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// clusterSealOrInactive returns r, or the inactive report for a service that
// supplied none.
func clusterSealOrInactive(r *clusterseal.Report) *clusterseal.Report {
	if r == nil {
		return &clusterseal.Report{}
	}
	return r
}

// toolBarrierClusterUnseal defines the qrdb_barrier_cluster_unseal tool
// schema. It takes no arguments.
func toolBarrierClusterUnseal() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_cluster_unseal",
		mcp.WithDescription("Clear a cluster-wide seal of the node barrier. Unseals nothing: this node must already have unsealed itself."),
	)
}

// handleBarrierClusterUnseal returns a tool handler that delegates to
// svc.BarrierClusterUnseal and renders the node barrier's cluster_seal object.
func handleBarrierClusterUnseal(svc service.BarrierServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := svc.BarrierClusterUnseal(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_cluster_unseal", Err: err}
		}
		return marshalToolResult("qrdb_barrier_cluster_unseal", clusterSealOrInactive(report))
	}
}

// toolBarrierShamirInitialize defines the qrdb_barrier_shamir_initialize
// tool schema. so_pin and user_pin are optional and feed the configured
// pin.PINBackend when supplied.
func toolBarrierShamirInitialize() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_shamir_initialize",
		mcp.WithDescription("Initialize the barrier under Shamir custody: split the unseal key threshold-of-total_shares to the enrolled unseal-key holders and seal one envelope to each. Optionally supply so_pin and user_pin to provision the PIN backend in the same call."),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("Minimum number of holders required to reconstruct the unseal key."),
		),
		mcp.WithNumber("total_shares",
			mcp.Required(),
			mcp.Description("Number of shares; must equal the number of enrolled unseal-key holders."),
		),
		mcp.WithString("policy",
			mcp.Description("Factor policy: designated (default) or any-enrolled."),
		),
		withSecretArg("so_pin", "Optional Security Officer PIN provisioned into the PIN backend."),
		withSecretArg("user_pin", "Optional User PIN provisioned into the PIN backend."),
	)
}

// handleBarrierShamirInitialize returns a tool handler that delegates to
// svc.BarrierInitializeShamir. When pinBackend is non-nil and the request
// supplies so_pin / user_pin, the PINs are persisted on the backend before
// initialization.
func handleBarrierShamirInitialize(svc service.BarrierServicer, pinBackend pin.PINBackend) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := mcp.ParseInt(req, "threshold", 0)
		if threshold <= 0 {
			return mcp.NewToolResultError("threshold is required and must be positive"), nil
		}

		totalShares := mcp.ParseInt(req, "total_shares", 0)
		if totalShares <= 0 {
			return mcp.NewToolResultError("total_shares is required and must be positive"), nil
		}

		soPIN, userPIN, err := initializePINArgs(req)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_initialize", Err: err}
		}
		if err := service.ProvisionInitializePINs(ctx, svc, pinBackend, soPIN, userPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_shamir_initialize", Err: err}
		}

		info, err := svc.BarrierInitializeShamir(ctx, threshold, totalShares, seal.FactorPolicy(mcp.ParseString(req, "policy", "")))
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

// toolBarrierRecoveryGenerate defines the qrdb_barrier_recovery_generate
// tool schema. so_pin is optional and verified against the configured
// pin.PINBackend.
func toolBarrierRecoveryGenerate() mcp.Tool {
	return mcp.NewTool("qrdb_barrier_recovery_generate",
		mcp.WithDescription("Generate the independent recovery key and distribute it to the enrolled recovery-key holders."),
		mcp.WithNumber("threshold",
			mcp.Required(),
			mcp.Description("Minimum number of recovery holders required for recovery."),
		),
		mcp.WithString("policy",
			mcp.Description("Factor policy: designated (default) or any-enrolled."),
		),
		withSecretArg("so_pin", "SO PIN verified against the node's PIN backend."),
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

		soPIN, err := pinArg(req, "so_pin")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_generate", Err: err}
		}
		if err := pin.RequireSOPIN(ctx, pinBackend, soPIN); err != nil {
			return nil, &ToolError{Tool: "qrdb_barrier_recovery_generate", Err: err}
		}

		info, err := svc.BarrierGenerateRecoveryKeys(ctx, threshold, seal.FactorPolicy(mcp.ParseString(req, "policy", "")))
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
