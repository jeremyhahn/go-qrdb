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

package embedded

import (
	"context"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BarrierClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BarrierClient implementation
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the cryptographic barrier with a single
// master key by delegating to the injected BarrierServicer. When the
// request supplies SOPin / UserPin and a pin.PINBackend has been injected
// via WithPINBackend, the PINs are persisted on the backend before the
// barrier is initialized.
func (t *Transport) BarrierInitialize(ctx context.Context, req *transport.BarrierInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	if err := applyBarrierInitializePINs(t.pinBackend, req.SOPin, req.UserPin); err != nil {
		return err
	}
	return t.barrierSvc.BarrierInitialize(ctx, req.Secret)
}

// BarrierUnseal unseals the barrier using the master key by delegating
// to the injected BarrierServicer.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierUnseal(ctx, req.Secret)
}

// BarrierSeal transitions the barrier to the sealed state by delegating
// to the injected BarrierServicer.
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierSeal(ctx)
}

// BarrierStatus returns the current state of the cryptographic barrier
// by delegating to the injected BarrierServicer.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	info, err := t.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, err
	}
	resp := &transport.BarrierStatusResponse{
		Sealed:          info.Sealed,
		Strategy:        info.Strategy,
		HardwareBacked:  info.HardwareBacked,
		ShamirThreshold: info.ShamirThreshold,
		ShamirTotal:     info.ShamirTotal,
	}
	if !info.InitializedAt.IsZero() {
		resp.InitializedAt = info.InitializedAt.Format(time.RFC3339)
	}
	return resp, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret
// sharing and returns the generated key shares by delegating to the
// injected BarrierServicer. When the request supplies SOPin / UserPin
// and a pin.PINBackend has been injected via WithPINBackend, the PINs
// are persisted on the backend before initialization.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	if err := applyBarrierInitializePINs(t.pinBackend, req.SOPin, req.UserPin); err != nil {
		return nil, err
	}
	info, err := t.barrierSvc.BarrierInitializeShamir(ctx, req.Secret, req.Threshold, req.TotalShares)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierInitializeShamirResponse{
		ShareCount:  info.ShareCount,
		Threshold:   info.Threshold,
		TotalShares: info.TotalShares,
	}, nil
}

// BarrierUnsealWithShare submits a single Shamir share toward the unseal
// threshold by delegating to the injected BarrierServicer.
func (t *Transport) BarrierUnsealWithShare(ctx context.Context, req *transport.BarrierUnsealShareRequest) (*transport.BarrierUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	progress, err := t.barrierSvc.BarrierUnsealWithShare(ctx, req.Share)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierUnsealShareResponse{
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
	}, nil
}

// BarrierUnsealWithShares submits multiple Shamir shares to unseal the
// barrier in a single request by delegating to the injected BarrierServicer.
func (t *Transport) BarrierUnsealWithShares(ctx context.Context, req *transport.BarrierUnsealSharesRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierUnsealWithShares(ctx, req.Shares)
}

// BarrierRekey generates new Shamir shares with the specified threshold
// and total count by delegating to the injected BarrierServicer. SO PIN
// verification is enforced when a pin.PINBackend is injected and has a
// SO PIN set.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	if err := verifyBarrierSOPIN(t.pinBackend, req.SOPin); err != nil {
		return nil, err
	}
	info, err := t.barrierSvc.BarrierRekey(ctx, req.Threshold, req.Total)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierRekeyResponse{
		ShareCount:  info.ShareCount,
		Threshold:   info.Threshold,
		TotalShares: info.TotalShares,
	}, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// by delegating to the injected BarrierServicer. SO PIN verification is
// enforced when a pin.PINBackend is injected and has a SO PIN set.

// embeddedMigrationAcceptWindow bounds how long BarrierEnableEncryption waits
// for EncryptionMigrator.EnableEncryption to return before giving up on
// delivering its outcome synchronously. Mirrors migrationAcceptWindow /
// grpcMigrationAcceptWindow / quicMigrationAcceptWindow /
// barrierEnableEncryptionMigrationAcceptWindow on the REST/gRPC/QUIC/MCP
// transports -- see the REST doc comment (pkg/api/rest/handlers_barrier.go)
// for the full rationale: every synchronously-knowable rejection returns
// well within this window, before the destructive Quiesce phase (which for
// a system-scope migration tears down and rebuilds this in-process node's
// own data plane) ever runs.
const embeddedMigrationAcceptWindow = 750 * time.Millisecond

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a
// keyed strategy by delegating to the injected EncryptionMigrator (see
// WithEncryptionMigrator). Like the REST/gRPC/Unix/QUIC/MCP handlers it
// validates synchronously, then invokes the migrator and races its result
// against embeddedMigrationAcceptWindow: a synchronous rejection is returned
// to the caller as a real error, while a migration still running once the
// window elapses is acknowledged with "migration_started"; the caller polls
// BarrierStatus.
//
// When no EncryptionMigrator is configured (a composer that never wired one,
// standalone mode) it refuses with the same service.ErrMigrationProviderUnavailable
// every other transport uses for that state, rather than the transport-
// specific NotImplementedError this method returned unconditionally before
// sdk/go/node started wiring its own migrator through here.
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.encMigrator == nil {
		return nil, service.ErrMigrationProviderUnavailable
	}

	scope := service.BarrierScope{Kind: service.ScopeSystem}
	if req.TenantID != "" {
		scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: req.TenantID}
	}

	mechanism := seal.MechanismSwap
	if req.Mechanism == string(seal.MechanismInPlace) {
		mechanism = seal.MechanismInPlace
	}
	availability := seal.AvailabilityQuiesced
	if req.Availability == string(seal.AvailabilityOnline) {
		availability = seal.AvailabilityOnline
	}

	// The request's passphrase is copied into protected memory and the
	// decoded copy wiped at once -- the credential is what the migration
	// reads from here on, so nothing needs the request's plaintext past this
	// line.
	creds, err := seal.CredentialsFor(req.Passphrase)
	secure.Wipe(req.Passphrase)
	if err != nil {
		return nil, err
	}
	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyID(req.Strategy),
		Mechanism:       mechanism,
		Availability:    availability,
		Credentials:     creds,
		ShamirThreshold: req.ShamirThreshold,
		ShamirTotal:     req.ShamirTotal,
	}
	if err := opts.Validate(); err != nil {
		creds.Secret.Destroy()
		return nil, err
	}

	// A background context is used because ctx is cancelled once this call
	// returns, and the migration must continue independent of this request's
	// lifetime.
	migrator := t.encMigrator
	resultCh := make(chan error, 1)
	go func() {
		// The migration OWNS the credential from here: it outlives this
		// call, so destroying it here would pull the passphrase out from
		// under a running migration. The destroy belongs to the last
		// reader, and that is this goroutine.
		defer creds.Secret.Destroy()
		resultCh <- migrator.EnableEncryption(context.Background(), scope, opts)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			return nil, err
		}
		// Completed within the window with no error: an unusually fast
		// migration (e.g. an empty barrier) already finished. Fall through
		// and report "migration_started" -- BarrierStatus already reflects
		// the committed strategy, so the wording is accurate either way.
	case <-time.After(embeddedMigrationAcceptWindow):
		// Still running past acceptance: no caller is left to deliver the
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

	return &transport.BarrierEnableEncryptionResponse{
		Status: "migration_started",
		Scope:  string(scope.Kind),
	}, nil
}

func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	if err := verifyBarrierSOPIN(t.pinBackend, req.SOPin); err != nil {
		return nil, err
	}
	info, err := t.barrierSvc.BarrierGenerateRecoveryKeys(ctx, req.Threshold, req.Total)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierRecoveryKeysResponse{
		ShareCount: info.ShareCount,
		Threshold:  info.Threshold,
		Total:      info.Total,
	}, nil
}

// BarrierRecoverWithKeys unseals the barrier using recovery keys by
// delegating to the injected BarrierServicer.
func (t *Transport) BarrierRecoverWithKeys(ctx context.Context, req *transport.BarrierRecoverWithKeysRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierRecoverWithKeys(ctx, req.Keys)
}

// BarrierDeleteRecoveryKeys deletes all stored recovery keys by delegating
// to the injected BarrierServicer.
func (t *Transport) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierDeleteRecoveryKeys(ctx)
}

// BarrierHasRecoveryKeys checks whether recovery keys have been generated
// by delegating to the injected BarrierServicer.
func (t *Transport) BarrierHasRecoveryKeys(ctx context.Context) (*transport.BarrierHasRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	hasKeys, err := t.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierHasRecoveryKeysResponse{HasKeys: hasKeys}, nil
}

// BarrierGenerateRootToken generates a new root token using the provided
// Shamir shares by delegating to the injected BarrierServicer.
func (t *Transport) BarrierGenerateRootToken(ctx context.Context, req *transport.BarrierGenerateRootTokenRequest) (*transport.BarrierRootTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	info, err := t.barrierSvc.BarrierGenerateRootToken(ctx, req.Shares)
	if err != nil {
		return nil, err
	}
	return &transport.BarrierRootTokenResponse{
		Token:     info.Token,
		CreatedAt: info.CreatedAt.Format(time.RFC3339),
	}, nil
}

// ---------------------------------------------------------------------------
// Request dispatch handlers
// ---------------------------------------------------------------------------

func handleBarrierInitialize(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.BarrierInitializeRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierInitialize"}
	}
	return t.BarrierInitialize(ctx, r)
}

func handleBarrierUnseal(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.BarrierUnsealRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierUnseal"}
	}
	return t.BarrierUnseal(ctx, r)
}

func handleBarrierSeal(ctx context.Context, t *Transport, _, _ any) error {
	return t.BarrierSeal(ctx)
}

func handleBarrierStatus(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.BarrierStatus(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierStatusResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierInitializeShamir(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BarrierInitializeShamirRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierInitializeShamir"}
	}
	result, err := t.BarrierInitializeShamir(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierInitializeShamirResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierUnsealWithShare(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BarrierUnsealShareRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierUnsealWithShare"}
	}
	result, err := t.BarrierUnsealWithShare(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierUnsealShareResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierUnsealWithShares(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.BarrierUnsealSharesRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierUnsealWithShares"}
	}
	return t.BarrierUnsealWithShares(ctx, r)
}

func handleBarrierRekey(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BarrierRekeyRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierRekey"}
	}
	result, err := t.BarrierRekey(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierRekeyResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierGenerateRecoveryKeys(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BarrierGenerateRecoveryKeysRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierGenerateRecoveryKeys"}
	}
	result, err := t.BarrierGenerateRecoveryKeys(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierRecoveryKeysResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierRecoverWithKeys(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.BarrierRecoverWithKeysRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierRecoverWithKeys"}
	}
	return t.BarrierRecoverWithKeys(ctx, r)
}

func handleBarrierDeleteRecoveryKeys(ctx context.Context, t *Transport, _, _ any) error {
	return t.BarrierDeleteRecoveryKeys(ctx)
}

func handleBarrierHasRecoveryKeys(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierHasRecoveryKeysResponse); ok {
		*out = result
	}
	return nil
}

func handleBarrierGenerateRootToken(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BarrierGenerateRootTokenRequest)
	if !ok {
		return &InvalidRequestError{Method: "BarrierGenerateRootToken"}
	}
	result, err := t.BarrierGenerateRootToken(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BarrierRootTokenResponse); ok {
		*out = result
	}
	return nil
}

// OfficerRegister registers a Security Officer for the system barrier by
// delegating to the injected BarrierServicer.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	result, err := t.barrierSvc.BarrierOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, err
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    result.CertPEM,
		ShareIndex: result.ShareIndex,
	}, nil
}

// OfficerList returns all enrolled Security Officers for the system barrier
// by delegating to the injected BarrierServicer.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	officers, err := t.barrierSvc.BarrierOfficerList(ctx)
	if err != nil {
		return nil, err
	}
	return embeddedBarrierOfficerStatusesToTransport(officers), nil
}

// OfficerClaimStatus returns a per-officer claim status snapshot for the
// system barrier by delegating to the injected BarrierServicer.
func (t *Transport) OfficerClaimStatus(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	officers, err := t.barrierSvc.BarrierOfficerClaimStatus(ctx)
	if err != nil {
		return nil, err
	}
	return embeddedBarrierOfficerStatusesToTransport(officers), nil
}

// ShareClaimBegin issues a challenge nonce to the named Security Officer for
// the system barrier share claim handshake by delegating to the injected
// BarrierServicer.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	challenge, err := t.barrierSvc.BarrierShareClaimBegin(ctx, req.Username, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt,
	}, nil
}

// ShareClaimComplete verifies the Security Officer's nonce signature and
// returns the system barrier share bytes exactly once by delegating to the
// injected BarrierServicer.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	result, err := t.barrierSvc.BarrierShareClaimComplete(ctx, req.Username, req.NonceHex, req.Signature, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: result.ShareIndex,
		ShareBytes: result.ShareBytes,
	}, nil
}

// ShamirSealVote submits a Shamir share as a seal vote for the system barrier
// by delegating to the injected BarrierServicer.
func (t *Transport) ShamirSealVote(ctx context.Context, req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	progress, err := t.barrierSvc.BarrierShamirSealVote(ctx, req.Share)
	if err != nil {
		return nil, err
	}
	return &transport.ShamirSealVoteResponse{
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
	}, nil
}

// RecoveryOfficerRegister registers a Security Officer for recovery key
// distribution by delegating to the injected BarrierServicer.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	result, err := t.barrierSvc.BarrierRecoveryOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, err
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    result.CertPEM,
		ShareIndex: result.ShareIndex,
	}, nil
}

// RecoveryShareClaimBegin issues a challenge nonce to the named recovery
// officer for the recovery share claim handshake by delegating to the injected
// BarrierServicer.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	challenge, err := t.barrierSvc.BarrierRecoveryShareClaimBegin(ctx, req.Username, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt,
	}, nil
}

// RecoveryShareClaimComplete verifies the recovery officer's nonce signature
// and returns the recovery share bytes exactly once by delegating to the
// injected BarrierServicer.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	result, err := t.barrierSvc.BarrierRecoveryShareClaimComplete(ctx, req.Username, req.NonceHex, req.Signature, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: result.ShareIndex,
		ShareBytes: result.ShareBytes,
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// embeddedBarrierOfficerStatusesToTransport converts service layer officer
// status records to the transport layer representation.
func embeddedBarrierOfficerStatusesToTransport(officers []*service.BarrierOfficerStatus) []*transport.OfficerStatus {
	out := make([]*transport.OfficerStatus, len(officers))
	for i, o := range officers {
		out[i] = &transport.OfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     o.ShareIndex,
			AuthFactor:     o.AuthFactor,
			EnrolledAt:     o.EnrolledAt,
			ClaimedAt:      o.ClaimedAt,
			SharePending:   o.SharePending,
		}
	}
	return out
}

// WithBarrierService injects a BarrierServicer into the transport.
func WithBarrierService(svc service.BarrierServicer) Option {
	return func(t *Transport) {
		t.barrierSvc = svc
	}
}

// WithEncryptionMigrator injects the EncryptionMigrator BarrierEnableEncryption
// delegates to, mirroring server.WithEncryptionMigrator on the network
// transports (REST/gRPC/Unix/QUIC/MCP). When nil (the default) or omitted,
// BarrierEnableEncryption refuses with service.ErrMigrationProviderUnavailable.
//
// A composer (sdk/go/node) that wants EnableEncryption reachable through this
// transport should pass an adapter that resolves to its OWN live migrator on
// every call rather than a value captured once at construction time: a
// system-scope migration recycles the data plane in process, and the
// migrator backing it can be rebuilt across that recycle.
func WithEncryptionMigrator(m service.EncryptionMigrator) Option {
	return func(t *Transport) {
		t.encMigrator = m
	}
}

// WithPINBackend injects the canonical pin.PINBackend used by the
// embedded transport's barrier handlers to provision SO PIN / User PIN
// on initialize and to verify SO PIN on rekey / recovery-generate. When
// nil the PIN fields supplied in barrier requests are ignored.
func WithPINBackend(backend pin.PINBackend) Option {
	return func(t *Transport) {
		t.pinBackend = backend
	}
}
