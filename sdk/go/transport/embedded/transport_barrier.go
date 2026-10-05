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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"
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
	if err := t.gate("BarrierInitialize"); err != nil {
		return err
	}
	// BarrierInitialize is a sealed-window verb (pkg/api/sealedwindow.VerbBarrierInitialize): public on every transport's boot-time sealed listener, because an
	// embedded node has no IAM store to authenticate against while sealed.
	// It is gated by secret (passphrase/Shamir share set) the strategy verifies, not an RBAC permission.
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	if err := service.ProvisionInitializePINs(ctx, t.barrierSvc, t.pinBackend, req.SOPin, req.UserPin); err != nil {
		return delayed("BarrierInitialize", err)
	}
	return t.barrierSvc.BarrierInitialize(ctx, req.Secret)
}

// BarrierUnseal unseals the barrier using the master key by delegating
// to the injected BarrierServicer.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if err := t.gate("BarrierUnseal"); err != nil {
		return err
	}
	// BarrierUnseal is a sealed-window verb (pkg/api/sealedwindow.VerbBarrierUnseal): public on every transport's boot-time sealed listener, because an
	// embedded node has no IAM store to authenticate against while sealed.
	// It is gated by the unseal secret the barrier strategy verifies, not an RBAC permission.
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
	if err := t.gate("BarrierSeal"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "BarrierSeal"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc.BarrierSeal(ctx)
}

// BarrierClusterUnseal clears a cluster-wide seal of the node barrier and
// returns its cluster seal state. It unseals nothing.
func (t *Transport) BarrierClusterUnseal(ctx context.Context) (*transport.ClusterSeal, error) {
	if err := t.gate("BarrierClusterUnseal"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierClusterUnseal"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	report, err := t.barrierSvc.BarrierClusterUnseal(ctx)
	if err != nil {
		return nil, err
	}
	return sealconv.FromReport(report), nil
}

// BarrierStatus returns the current state of the cryptographic barrier
// by delegating to the injected BarrierServicer.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if err := t.gate("BarrierStatus"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierStatus"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	info, err := t.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, delayed("BarrierStatus", err)
	}
	resp := &transport.BarrierStatusResponse{
		Sealed:          info.Sealed,
		Strategy:        info.Strategy,
		HardwareBacked:  info.HardwareBacked,
		ShamirThreshold: info.ShamirThreshold,
		ShamirTotal:     info.ShamirTotal,
		ClusterSeal:     sealconv.FromReport(info.ClusterSeal),
	}
	if !info.InitializedAt.IsZero() {
		resp.InitializedAt = info.InitializedAt.Format(time.RFC3339)
	}
	return resp, nil
}

// BarrierInitializeShamir initializes the barrier under Shamir custody,
// distributing one sealed envelope to each enrolled unseal-key holder, by
// delegating to the injected BarrierServicer. When the request supplies SOPin / UserPin
// and a pin.PINBackend has been injected via WithPINBackend, the PINs
// are persisted on the backend before initialization.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if err := t.gate("BarrierInitializeShamir"); err != nil {
		return nil, err
	}
	// BarrierInitializeShamir is a sealed-window verb (pkg/api/sealedwindow.VerbShamirInitialize): public on every transport's boot-time sealed listener, because an
	// embedded node has no IAM store to authenticate against while sealed.
	// It is gated by the SO/user PINs ProvisionInitializePINs verifies, not an RBAC permission.
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	if err := service.ProvisionInitializePINs(ctx, t.barrierSvc, t.pinBackend, req.SOPin, req.UserPin); err != nil {
		return nil, delayed("BarrierInitializeShamir", err)
	}
	info, err := t.barrierSvc.BarrierInitializeShamir(ctx, req.Threshold, req.TotalShares, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, delayed("BarrierInitializeShamir", err)
	}
	return &transport.BarrierInitializeShamirResponse{
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
	if err := t.gate("BarrierEnableEncryption"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierEnableEncryption"); err != nil {
		return nil, err
	}
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
	creds, err := seal.CredentialsFor(ctx, req.Passphrase)
	secure.Wipe(req.Passphrase)
	if err != nil {
		return nil, delayed("BarrierEnableEncryption", err)
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
		return nil, delayed("BarrierEnableEncryption", err)
	}

	// The migration runs on the request's context without its cancellation:
	// the request context is cancelled once this call returns, and the
	// migration must continue independent of its lifetime, while still
	// carrying the request's values (the caller's identity and tenant), as
	// on every transport.
	migrationCtx := context.WithoutCancel(ctx)
	migrator := t.encMigrator
	resultCh := make(chan error, 1)
	go func() {
		// The migration OWNS the credential from here: it outlives this
		// call, so destroying it here would pull the passphrase out from
		// under a running migration. The destroy belongs to the last
		// reader, and that is this goroutine.
		defer creds.Secret.Destroy()
		resultCh <- migrator.EnableEncryption(migrationCtx, scope, opts)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			return nil, delayed("BarrierEnableEncryption", err)
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
	if err := t.gate("BarrierGenerateRecoveryKeys"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierGenerateRecoveryKeys"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	if err := pin.RequireSOPIN(ctx, t.pinBackend, req.SOPin); err != nil {
		return nil, delayed("BarrierGenerateRecoveryKeys", err)
	}
	info, err := t.barrierSvc.BarrierGenerateRecoveryKeys(ctx, req.Threshold, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, delayed("BarrierGenerateRecoveryKeys", err)
	}
	return &transport.BarrierRecoveryKeysResponse{
		ShareCount: info.ShareCount,
		Threshold:  info.Threshold,
		Total:      info.Total,
	}, nil
}

// BarrierDeleteRecoveryKeys deletes all stored recovery keys by delegating
// to the injected BarrierServicer.
func (t *Transport) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if err := t.gate("BarrierDeleteRecoveryKeys"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "BarrierDeleteRecoveryKeys"); err != nil {
		return err
	}
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
	if err := t.gate("BarrierHasRecoveryKeys"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierHasRecoveryKeys"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	hasKeys, err := t.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, delayed("BarrierHasRecoveryKeys", err)
	}
	return &transport.BarrierHasRecoveryKeysResponse{HasKeys: hasKeys}, nil
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

func handleBarrierDeleteRecoveryKeys(ctx context.Context, t *Transport, _, _ any) error {
	return t.BarrierDeleteRecoveryKeys(ctx)
}

// ---------------------------------------------------------------------------
// Conversion helpers

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// barrierService returns the injected BarrierServicer, or the typed refusal
// when the transport is closed or none was injected.
func (t *Transport) barrierService() (service.BarrierServicer, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.barrierSvc == nil {
		return nil, &NilServiceError{Service: "BarrierServicer"}
	}
	return t.barrierSvc, nil
}

// contextOptionalThumbprint returns the verified TLS client certificate's
// thumbprint carried on ctx, or "" when there is none. A quorum submission
// does not require one; when present the mtls factor binds to it.
func contextOptionalThumbprint(ctx context.Context) string {
	thumb, err := contextCertThumbprint(ctx)
	if err != nil {
		return ""
	}
	return thumb
}

// registerHolder enrols a holder of domain.
func (t *Transport) registerHolder(ctx context.Context, domain seal.QuorumDomain, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("registerHolder", err)
	}
	result, err := svc.BarrierOfficerRegister(ctx, domain, holderwire.RegisterFrom(req))
	if err != nil {
		return nil, delayed("registerHolder", err)
	}
	return holderwire.RegisterTo(result), nil
}

// OfficerRegister enrols a share holder of the unseal key.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if err := t.gate("OfficerRegister"); err != nil {
		return nil, err
	}
	// OfficerRegister is a sealed-window verb (pkg/api/sealedwindow.VerbOfficerRegister): public on every transport's boot-time sealed listener, because an
	// embedded node has no IAM store to authenticate against while sealed.
	// It is gated by the CA trust-on-first-use registration the sealed-window listener grants anonymously.
	return t.registerHolder(ctx, seal.DomainUnseal, req)
}

// RecoveryOfficerRegister enrols a holder of the recovery key.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if err := t.gate("RecoveryOfficerRegister"); err != nil {
		return nil, err
	}
	// RecoveryOfficerRegister is a sealed-window verb (pkg/api/sealedwindow.VerbRecoveryOfficerRegister): public on every transport's boot-time sealed listener, because an
	// embedded node has no IAM store to authenticate against while sealed.
	// It is gated by the CA trust-on-first-use registration the sealed-window listener grants anonymously.
	return t.registerHolder(ctx, seal.DomainRecovery, req)
}

// OfficerList lists the unseal key's holders and their claim state.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if err := t.gate("OfficerList"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "OfficerList"); err != nil {
		return nil, err
	}
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("OfficerList", err)
	}
	officers, err := svc.BarrierOfficerList(ctx, seal.DomainUnseal)
	if err != nil {
		return nil, delayed("OfficerList", err)
	}
	return holderwire.StatusesTo(officers), nil
}

// claimBegin issues a claim nonce to a holder of domain, who must present
// its certificate on ctx.
func (t *Transport) claimBegin(ctx context.Context, domain seal.QuorumDomain, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("claimBegin", err)
	}
	ctx, thumbprint := claimContext(ctx)
	challenge, err := svc.BarrierShareClaimBegin(ctx, domain, req.Username, thumbprint)
	if err != nil {
		return nil, delayed("claimBegin", err)
	}
	return &transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt}, nil
}

// claimComplete delivers a holder's sealed envelope exactly once.
func (t *Transport) claimComplete(ctx context.Context, domain seal.QuorumDomain, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("claimComplete", err)
	}
	ctx, thumbprint := claimContext(ctx)
	result, err := svc.BarrierShareClaimComplete(ctx, domain, req.Username, req.NonceHex, req.Signature, thumbprint)
	if err != nil {
		return nil, delayed("claimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// ShareClaimBegin issues a claim nonce to an unseal-key holder.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	return t.claimBegin(ctx, seal.DomainUnseal, req)
}

// ShareClaimComplete delivers an unseal-key holder's sealed envelope once.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	return t.claimComplete(ctx, seal.DomainUnseal, req)
}

// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	return t.claimBegin(ctx, seal.DomainRecovery, req)
}

// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	return t.claimComplete(ctx, seal.DomainRecovery, req)
}

// BarrierQuorumBegin issues the factor challenge a holder answers.
func (t *Transport) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("BarrierQuorumBegin", err)
	}
	// No RBAC permission gates a quorum challenge -- a holder authenticates
	// through the factor it presents, not an RBAC role. ctx is marked with
	// the same claimContext provenance the share-claim handshake uses.
	ctx, _ = claimContext(ctx)
	challenge, err := svc.BarrierQuorumBegin(ctx, holderwire.BeginFrom(req))
	if err != nil {
		return nil, delayed("BarrierQuorumBegin", err)
	}
	return holderwire.ChallengeTo(challenge), nil
}

// BarrierQuorumSubmit submits one authenticated holder's share.
func (t *Transport) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("BarrierQuorumSubmit", err)
	}
	// Same provenance marking as BarrierQuorumBegin, and for the same
	// reason.
	ctx, thumbprint := claimContext(ctx)
	out, err := svc.BarrierQuorumSubmit(ctx, holderwire.SubmitFrom(req, thumbprint))
	if err != nil {
		return nil, delayed("BarrierQuorumSubmit", err)
	}
	return holderwire.OutcomeTo(out), nil
}

// BarrierRekeyStart opens a rekey round under SO PIN authorization.
func (t *Transport) BarrierRekeyStart(ctx context.Context, req *transport.RekeyStartRequest) (*transport.QuorumRound, error) {
	if err := t.gate("BarrierRekeyStart"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierRekeyStart"); err != nil {
		return nil, err
	}
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("BarrierRekeyStart", err)
	}
	round, err := svc.BarrierRekeyStart(ctx, seal.QuorumDomain(req.Domain), req.Threshold, seal.FactorPolicy(req.Policy), req.SOPin)
	if err != nil {
		return nil, delayed("BarrierRekeyStart", err)
	}
	return holderwire.RoundTo(round), nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey, under
// SO PIN authorization.
func (t *Transport) BarrierOfficerRevoke(ctx context.Context, req *transport.OfficerRevokeRequest) (*transport.QuorumRound, error) {
	if err := t.gate("BarrierOfficerRevoke"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BarrierOfficerRevoke"); err != nil {
		return nil, err
	}
	svc, err := t.barrierService()
	if err != nil {
		return nil, delayed("BarrierOfficerRevoke", err)
	}
	round, err := svc.BarrierOfficerRevoke(ctx, seal.QuorumDomain(req.Domain), req.Username, req.Threshold, req.SOPin)
	if err != nil {
		return nil, delayed("BarrierOfficerRevoke", err)
	}
	return holderwire.RoundTo(round), nil
}

// ---------------------------------------------------------------------------

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
// on initialize and to verify the SO PIN on seal, recovery generation, rekey
// start and holder revocation. Without one those operations fail closed with
// pin.ErrBackendNotConfigured. The backend is decorated with the SO PIN
// lockout (pin.NewLockoutBackend, idempotent) when the option is built.
func WithPINBackend(backend pin.PINBackend) Option {
	locked := pin.NewLockoutBackend(backend)
	return func(t *Transport) {
		t.pinBackend = locked
	}
}
