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

package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Barrier request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// barrierInitializeRequest is the JSON payload for POST /v1/barrier/initialize.
//
// Secret is []byte, so it is base64 on the wire and a wipeable slice in the
// process. A Go string cannot be zeroized, which is the whole reason the
// barrier credential is not one anywhere in this stack.
type barrierInitializeRequest struct {
	Secret  []byte `json:"secret"`
	SOPin   string `json:"so_pin,omitempty"`
	UserPin string `json:"user_pin,omitempty"`
}

// barrierUnsealRequest is the JSON payload for POST /v1/barrier/unseal.
type barrierUnsealRequest struct {
	Secret []byte `json:"secret"`
}

// barrierSealRequest is the JSON payload for POST /v1/barrier/seal.
// When the request body is empty or absent the server treats SOPin as the
// empty string. pin.RequireSOPIN refuses the call when no pin.PINBackend is
// configured and verifies SOPin once an SO PIN has been provisioned.
type barrierSealRequest struct {
	SOPin string `json:"so_pin,omitempty"`
}

// ---------------------------------------------------------------------------
// Barrier handlers
// ---------------------------------------------------------------------------

// handleBarrierInitialize handles POST /v1/barrier/initialize -- initializes
// the barrier with a secret credential.
// Required permission: seal:bootstrap (SO role).
func (s *Server) handleBarrierInitialize(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierInitializeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	// The decoded credential is this handler's to own and to destroy. The
	// service layer and pkg/seal borrow it for the call and retain nothing,
	// so a deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.Secret)

	// Refuse an initialized barrier before any PIN is compared, then
	// provision the SO / User PINs. PINs supplied with no backend configured
	// are refused, never ignored.
	if err := service.ProvisionInitializePINs(r.Context(), s.barrierSvc, s.pinBackend, req.SOPin, req.UserPin); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	if err := s.barrierSvc.BarrierInitialize(r.Context(), req.Secret); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierUnseal handles POST /v1/barrier/unseal -- unseals the barrier
// with a secret credential.
// Required permission: seal:unseal (SO, Admin, Operator).
func (s *Server) handleBarrierUnseal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierUnsealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	defer secure.Wipe(req.Secret)

	if err := s.barrierSvc.BarrierUnseal(r.Context(), req.Secret); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierSeal handles POST /v1/barrier/seal -- transitions the barrier
// to the sealed state. When a request body is present it is decoded into a
// barrierSealRequest carrying the SO PIN; when absent SOPin is empty, which
// passes only on a node with no SO PIN provisioned yet.
// Required permission: seal:seal (SO, Admin, Operator).
func (s *Server) handleBarrierSeal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	// Decode the optional SO PIN body. An empty body is permitted —
	// json.Decoder.Decode returns io.EOF which we treat as a no-op.
	var req barrierSealRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}

	if err := pin.RequireSOPIN(r.Context(), s.pinBackend, req.SOPin); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	if err := s.barrierSvc.BarrierSeal(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierStatus handles GET /v1/barrier/status -- returns the current
// barrier status including sealed state, strategy, and initialization time.
// Required permission: seal:status (SO, Admin, Operator, Auditor).
func (s *Server) handleBarrierStatus(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	info, err := s.barrierSvc.BarrierStatus(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, barrierStatusToResponse(info))
}

// handleBarrierShamirInitialize handles POST /v1/barrier/shamir/initialize --
// initializes the barrier under Shamir custody, splitting the unseal key
// threshold-of-totalShares to the enrolled unseal-domain holders.
// Required permission: seal:initialize_shamir (SO role only).
func (s *Server) handleBarrierShamirInitialize(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req transport.BarrierInitializeShamirRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Refuse an initialized barrier before any PIN is compared, then
	// provision the SO / User PINs. PINs supplied with no backend configured
	// are refused, never ignored.
	if err := service.ProvisionInitializePINs(r.Context(), s.barrierSvc, s.pinBackend, req.SOPin, req.UserPin); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	result, err := s.barrierSvc.BarrierInitializeShamir(r.Context(), req.Threshold, req.TotalShares, seal.FactorPolicy(req.Policy))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shamirInitToResponse(result))
}

// barrierEnableEncryptionRequest is the JSON payload for
// POST /v1/barrier/enable-encryption.
type barrierEnableEncryptionRequest struct {
	// TenantID, when set, scopes the migration to that tenant's dedicated
	// barrier; empty means the system barrier (admin scope).
	TenantID string `json:"tenant_id,omitempty"`
	// Strategy is the target keyed strategy: "passphrase" or "shamir".
	Strategy string `json:"strategy"`
	// Mechanism is "swap" (default) or "in-place".
	Mechanism string `json:"mechanism,omitempty"`
	// Availability is "quiesced" (default) or "online".
	Availability string `json:"availability,omitempty"`
	// Passphrase is the unseal credential for a passphrase target. It is
	// []byte (base64 on the wire) so it can be zeroized; see
	// barrierInitializeRequest.
	Passphrase []byte `json:"passphrase,omitempty"`
	// ShamirThreshold / ShamirTotal configure an M-of-N Shamir target.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`
	ShamirTotal     int `json:"shamir_total,omitempty"`
}

// migrationAcceptWindow bounds how long the handler waits for
// EncryptionMigrator.EnableEncryption to return before giving up on
// delivering its outcome synchronously. EnableEncryption is a single
// synchronous call spanning both the fast, purely in-memory acceptance
// checks (target-strategy/mechanism/availability validation, tenant
// entitlement, shared-barrier-immutable authority, Shamir officer quorum,
// provider availability) AND the slow, destructive execution phase (Quiesce,
// which for a system-scope migration tears down the data-plane server this
// request is served on, followed by the re-encryption itself). Every
// synchronously-knowable rejection returns well within this window, before
// Quiesce ever runs, so racing the call against it lets the handler surface
// a real error instead of a false "migration_started". If the window elapses
// the migration has moved past acceptance into its long-running execution;
// its eventual outcome can no longer be delivered to this client and is
// logged only.
const migrationAcceptWindow = 750 * time.Millisecond

// handleBarrierEnableEncryption handles POST /v1/barrier/enable-encryption --
// migrates a previously-unencrypted barrier to a keyed strategy. It validates
// the request synchronously, then invokes the migrator and races its result
// against migrationAcceptWindow: a synchronous rejection (bad target,
// shared-barrier-immutable, insufficient officers, entitlement denied,
// provider unavailable) is returned to the caller as a proper error, while a
// migration that is still running once the window elapses is acknowledged
// with 202 "migration_started" — a system-scope migration recycles the
// data-plane server in process, so its outcome cannot be delivered to this
// client either way. Clients observe completion by polling
// GET /v1/barrier/status (the strategy changes from "none" to the keyed
// strategy).
// Required permission: seal:enable_encryption.
func (s *Server) handleBarrierEnableEncryption(w http.ResponseWriter, r *http.Request) {
	if s.encMigrator == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "encryption-migration"})
		return
	}
	var req barrierEnableEncryptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	scope := service.BarrierScope{Kind: service.ScopeSystem}
	if req.TenantID != "" {
		scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: req.TenantID}
	}

	// The request's passphrase is copied into protected memory and the
	// decoded copy wiped at once — the credential is what the migration reads
	// from here on, so nothing needs the request's plaintext past this line.
	creds, err := seal.CredentialsFor(r.Context(), req.Passphrase)
	secure.Wipe(req.Passphrase)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyID(req.Strategy),
		Mechanism:       migrationMechanism(req.Mechanism),
		Availability:    migrationAvailability(req.Availability),
		Credentials:     creds,
		ShamirThreshold: req.ShamirThreshold,
		ShamirTotal:     req.ShamirTotal,
	}
	// Validate synchronously so the caller gets immediate feedback on bad input
	// (the async path can no longer surface a structured error to this client).
	if err := opts.Validate(); err != nil {
		seal.DestroyCredentials(creds)
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// The migration runs on the request's context without its cancellation:
	// the request context is cancelled once this call returns, and the
	// migration must continue independent of its lifetime, while still
	// carrying the request's values (the caller's identity and tenant), as
	// on every transport.
	migrationCtx := context.WithoutCancel(r.Context())
	resultCh := make(chan error, 1)
	go func() {
		// The migration OWNS the credential from here: it outlives this
		// request, so destroying it in the handler would pull the
		// passphrase out from under a running migration. The destroy
		// belongs to the last reader, and that is this goroutine.
		defer seal.DestroyCredentials(creds)
		resultCh <- s.encMigrator.EnableEncryption(migrationCtx, scope, opts)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
		// Completed within the window with no error: an unusually fast
		// migration (e.g. an empty barrier) already finished. Fall through
		// and report "migration_started" — BarrierStatus already reflects
		// the committed strategy, so the wording is accurate either way.
	case <-time.After(migrationAcceptWindow):
		// Still running past acceptance: no client is left to deliver the
		// eventual outcome to, so only log it.
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

	writeJSON(w, map[string]interface{}{
		"status": "migration_started",
		"scope":  string(scope.Kind),
		"note":   "poll GET /v1/barrier/status; the strategy changes when the migration completes",
	})
}

// migrationMechanism maps the wire mechanism string to a seal.MigrationMechanism,
// defaulting to swap.
func migrationMechanism(s string) seal.MigrationMechanism {
	if s == string(seal.MechanismInPlace) {
		return seal.MechanismInPlace
	}
	return seal.MechanismSwap
}

// migrationAvailability maps the wire availability string to a
// seal.MigrationAvailability, defaulting to quiesced.
func migrationAvailability(s string) seal.MigrationAvailability {
	if s == string(seal.AvailabilityOnline) {
		return seal.AvailabilityOnline
	}
	return seal.AvailabilityQuiesced
}

// handleBarrierRecoveryGenerate handles POST /v1/barrier/recovery/generate --
// generates the independent recovery key and distributes it to the enrolled
// recovery-domain holders.
// Required permission: seal:generate_recovery (SO role only).
func (s *Server) handleBarrierRecoveryGenerate(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req transport.BarrierGenerateRecoveryKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Verify the SO PIN before generating recovery keys; fails closed
	// when no PIN backend is configured.
	if err := pin.RequireSOPIN(r.Context(), s.pinBackend, req.SOPin); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	result, err := s.barrierSvc.BarrierGenerateRecoveryKeys(r.Context(), req.Threshold, seal.FactorPolicy(req.Policy))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, recoveryKeysToResponse(result))
}

// handleBarrierRecoveryDelete handles DELETE /v1/barrier/recovery -- removes
// recovery key metadata from storage.
// Required permission: seal:generate_recovery (SO role only).
func (s *Server) handleBarrierRecoveryDelete(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierRecoveryStatus handles GET /v1/barrier/recovery/status --
// reports whether recovery key metadata exists in storage.
// Required permission: seal:status (SO, Admin, Operator, Auditor).
func (s *Server) handleBarrierRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	hasKeys, err := s.barrierSvc.BarrierHasRecoveryKeys(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Field name must be "has_keys" to match transport.BarrierHasRecoveryKeysResponse
	// (the shared SDK type gRPC/QUIC/unix all decode into); a prior "has_recovery_keys"
	// key here silently left every REST-transport caller's HasKeys field at its zero
	// value (false), regardless of the server's actual answer.
	writeJSON(w, map[string]interface{}{
		"has_keys": hasKeys,
	})
}

// ---------------------------------------------------------------------------
// Service-to-transport type mapping helpers
// ---------------------------------------------------------------------------

// barrierStatusToResponse converts a BarrierStatusInfo to a JSON-friendly
// map. The initialized_at field is omitted when zero. The shamir_threshold
// and shamir_total fields are included only when the strategy is shamir and
// the values are non-zero.
func barrierStatusToResponse(info *service.BarrierStatusInfo) map[string]interface{} {
	resp := map[string]interface{}{
		"sealed":          info.Sealed,
		"strategy":        info.Strategy,
		"hardware_backed": info.HardwareBacked,
	}
	if !info.InitializedAt.IsZero() {
		resp["initialized_at"] = info.InitializedAt.Format(time.RFC3339)
	}
	if info.ShamirThreshold > 0 {
		resp["shamir_threshold"] = info.ShamirThreshold
	}
	if info.ShamirTotal > 0 {
		resp["shamir_total"] = info.ShamirTotal
	}
	resp["cluster_seal"] = clusterSealOrInactive(info.ClusterSeal)
	return resp
}

// clusterSealOrInactive returns r, or the inactive report for a service that
// supplied none.
func clusterSealOrInactive(r *clusterseal.Report) *clusterseal.Report {
	if r == nil {
		return &clusterseal.Report{}
	}
	return r
}

// handleBarrierClusterUnseal handles POST /v1/barrier/cluster-unseal --
// clears a cluster-wide seal of the node barrier and returns its cluster_seal
// object. It unseals nothing: this node must have unsealed itself.
// Required permission: seal:unseal (system tenant only). It is on the
// sealed-window allowlist of the regular listener only (AuthenticatedOnly).
func (s *Server) handleBarrierClusterUnseal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}
	report, err := s.barrierSvc.BarrierClusterUnseal(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, clusterSealOrInactive(report))
}

// shamirInitToResponse converts a ShamirInitInfo to a JSON-friendly map.
// INV-1: share bytes are never included in initialization responses; Security
// Officers retrieve their shares via the claim handshake.
func shamirInitToResponse(info *service.ShamirInitInfo) map[string]interface{} {
	return map[string]interface{}{
		"threshold":    info.Threshold,
		"total_shares": info.TotalShares,
		"share_count":  info.ShareCount,
	}
}

// recoveryKeysToResponse converts a RecoveryKeysInfo to a JSON-friendly map.
// INV-1: recovery share bytes are never included in generate responses;
// recovery officers retrieve their shares via the claim handshake.
func recoveryKeysToResponse(info *service.RecoveryKeysInfo) map[string]interface{} {
	return map[string]interface{}{
		"threshold":   info.Threshold,
		"total":       info.Total,
		"share_count": info.ShareCount,
	}
}

// ---------------------------------------------------------------------------
// Barrier ceremony error mapping
// ---------------------------------------------------------------------------
