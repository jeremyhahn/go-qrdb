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

package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AdminRebindLookupTimeout bounds how long CeremonyServiceWithAdminRebind
// waits for a binding lookup that FAILS (as opposed to answering "no
// binding") before treating a retry request as refused. Matches
// firstboot.AdminBindingLookupTimeout, the same bound the daemon's own boot
// check uses for the identical read. A var, not a const, so a test can
// shorten it rather than block for the production bound.
var AdminRebindLookupTimeout = firstboot.AdminBindingLookupTimeout

// AdminAlreadyBoundError is returned by CeremonyServiceWithAdminRebind's
// CeremonyInitialize when a retry of the admin-credential step (go-qrdb#548)
// is attempted after the bootstrap admin already holds a live certificate
// binding. The admin-credential step is retriable only until the FIRST
// binding commits -- once it has, re-running it would mint a second,
// unbound-until-reissued credential for no reason, and silently replacing a
// working binding is not what a caller re-running `qrdb init` is asking for.
type AdminAlreadyBoundError struct{}

// Error implements error.
func (e *AdminAlreadyBoundError) Error() string {
	return "service: the bootstrap admin already holds a certificate binding; " +
		"the admin-credential step is retriable only until the first binding commits"
}

// AdminRebindNoBinderError is returned by CeremonyServiceWithAdminRebind when
// it was constructed with no CertBinder (go-qrdb#548 review: B5): reissuing a
// certificate with nothing wired to bind it would hand the caller a
// credential that looks successful but authenticates nothing, which this
// wrapper refuses to do -- a composition gap, not a retriable condition.
type AdminRebindNoBinderError struct{}

// Error implements error.
func (e *AdminRebindNoBinderError) Error() string {
	return "service: admin-credential reissue has no CertBinder wired, so the reissued certificate cannot be bound"
}

// AdminRebindLookupError wraps a failure reading the admin user back after a
// successful reissue, so CeremonyInitialize can bind the reissued
// certificate to its ID. Never returned alongside a credential: the caller
// gets this error INSTEAD of a result, never both.
type AdminRebindLookupError struct {
	Cause error
}

// Error implements error.
func (e *AdminRebindLookupError) Error() string {
	return "service: could not look up the bootstrap admin to bind the reissued certificate: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminRebindLookupError) Unwrap() error { return e.Cause }

// AdminRebindAdminNotFoundError is returned when a reissue succeeds but the
// bootstrap admin user does not exist yet to bind the fresh certificate to
// -- first boot has not created it on this cluster yet. The reissued
// certificate is discarded rather than returned unbound; the caller's next
// `qrdb init` after first boot completes reissues again.
type AdminRebindAdminNotFoundError struct{}

// Error implements error.
func (e *AdminRebindAdminNotFoundError) Error() string {
	return "service: admin-credential reissue succeeded but the bootstrap admin does not exist yet to bind it to; retry once first boot has created it"
}

// AdminRebindBindError wraps a failure binding the reissued certificate's
// SPKI thumbprint to the admin user, including a failure computing that
// thumbprint from the certificate ReissueAdminCredential just produced.
type AdminRebindBindError struct {
	Cause error
}

// Error implements error.
func (e *AdminRebindBindError) Error() string {
	return "service: failed to bind the reissued admin certificate: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminRebindBindError) Unwrap() error { return e.Cause }

// CeremonyServiceWithAdminRebind wraps a CeremonyServicer so that calling
// CeremonyInitialize on an ALREADY-initialized ceremony is treated as a retry
// of the admin-credential step (go-qrdb#548) rather than refused outright,
// gated on the bootstrap admin not already holding a live binding -- a check
// pkg/ceremony cannot make itself, because it has no user store.
//
// This is the recovery path for the gap between Initialize returning and its
// result reaching the replicated system store: if a crash (or any other
// failure) loses that in-memory value before a composer durably records it,
// re-running `qrdb init` with the SAME flags re-issues a fresh credential
// through the identical validation Initialize applies.
//
// Reissue BINDS the fresh certificate itself, in the same call, through
// binder (go-qrdb#548 review: B5): ReissueAdminCredential only mints a
// certificate, exactly as Initialize's own admin-credential step does, and
// an operator re-running `qrdb init` to repair a lost binding is asking for
// a working login, not a certificate to bind by hand. A reissue this
// wrapper cannot bind -- no binder wired, the admin does not exist yet, or
// the bind call itself fails -- is reported as an error, NEVER returned as
// a successful-looking result: this wrapper must not hand back a credential
// that authenticates nothing.
type CeremonyServiceWithAdminRebind struct {
	CeremonyServicer
	core     *ceremony.CeremonyService
	users    firstboot.AdminLookup
	binder   firstboot.CertBinder
	notifier firstboot.AdminBoundNotifier
}

// NewCeremonyServiceWithAdminRebind wraps inner, whose underlying
// *ceremony.CeremonyService is core, adding the admin-credential retry path
// gated against users and bound through binder. notifier -- optional, nil
// is a no-op -- is told (AdminBound) once a reissued certificate binds, so
// the awaiting-admin-credential gate (pkg/firstboot's
// AwaitingAdminCredentialProbe) can leave that state without re-reading the
// user store (go-qrdb#560 review B1).
func NewCeremonyServiceWithAdminRebind(inner CeremonyServicer, core *ceremony.CeremonyService, users firstboot.AdminLookup, binder firstboot.CertBinder, notifier firstboot.AdminBoundNotifier) *CeremonyServiceWithAdminRebind {
	return &CeremonyServiceWithAdminRebind{CeremonyServicer: inner, core: core, users: users, binder: binder, notifier: notifier}
}

// CeremonyInitialize delegates to the wrapped CeremonyInitialize while the
// ceremony is uninitialized. Once it is initialized, this call can only mean
// a retry of the admin-credential step: it refuses when the admin already has
// a live binding (*AdminAlreadyBoundError), and otherwise reissues the
// credential through the ceremony core, bypassing the wrapped Initialize
// entirely (which would refuse with ceremony.ErrAlreadyInitialized).
func (s *CeremonyServiceWithAdminRebind) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error) {
	status, err := s.CeremonyServicer.CeremonyStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.State == string(ceremony.StateAwaitingInit) {
		return s.CeremonyServicer.CeremonyInitialize(ctx, ca, opts)
	}

	bindErr := firstboot.VerifyAdminCertBindingWithin(ctx, s.users, AdminRebindLookupTimeout)
	if bindErr == nil {
		return nil, &AdminAlreadyBoundError{}
	}
	// Any refusal OTHER than "no binding"/"missing" (an unverifiable lookup,
	// for instance) is a condition the caller must resolve before retrying,
	// not evidence that a retry is safe -- propagate it unchanged rather than
	// treating an inconclusive check as "go ahead".
	var required *firstboot.AdminCertBindingRequiredError
	if !errors.As(bindErr, &required) {
		return nil, bindErr
	}
	switch required.Reason {
	case firstboot.AdminBindingMissing, firstboot.AdminBindingNone, firstboot.AdminBindingAllExpired:
		// No live binding yet: the retry may proceed.
	default:
		return nil, bindErr
	}

	var coreOpts *ceremony.InitializeOptions
	if opts != nil {
		coreOpts = &ceremony.InitializeOptions{
			SOPin:            opts.SOPin,
			AdminCSRPEM:      []byte(opts.AdminCSRPEM),
			AdminCertPEM:     []byte(opts.AdminCertPEM),
			AdminCertPoolPEM: []byte(opts.AdminCertPoolPEM),
			AdminGenerate:    opts.AdminGenerate,
		}
	}
	result, err := s.core.ReissueAdminCredential(ctx, coreOpts)
	if err != nil {
		return nil, err
	}
	if err := s.bindReissuedCredential(ctx, result.AdminCertPEM); err != nil {
		return nil, err
	}
	return ceremonyInitResultFromCore(result), nil
}

// bindReissuedCredential binds certPEM -- ReissueAdminCredential's just-minted
// certificate -- to the bootstrap admin's SPKI thumbprint, so a caller never
// receives a reissued credential this wrapper has not already made usable
// (go-qrdb#548 review: B5). Unlike the boot-time pending-admin-cert hand-off
// (which exists for the window BEFORE the admin user exists, racing across
// nodes), the admin reissue reaches is, by construction, already past that
// window: CeremonyInitialize only treats a call as a reissue once the
// ceremony itself is no longer awaiting its first Initialize, and binding a
// thumbprint is a single additive write (AddCertBinding) with no
// create-if-absent race to coordinate, so there is nothing the pending-cert
// store's extra durability would add here that a direct bind does not
// already give.
func (s *CeremonyServiceWithAdminRebind) bindReissuedCredential(ctx context.Context, certPEM []byte) error {
	if s.binder == nil {
		return &AdminRebindNoBinderError{}
	}
	admin, err := s.users.GetUserByUsername(ctx, tenantid.System, user.BootstrapAdminUsername)
	if err != nil {
		return &AdminRebindLookupError{Cause: err}
	}
	if admin == nil {
		return &AdminRebindAdminNotFoundError{}
	}
	thumbprint, tperr := spkiThumbprintFromCertPEM(certPEM)
	if tperr != nil {
		return &AdminRebindBindError{Cause: tperr}
	}
	if err := s.binder.BindAdminCert(ctx, tenantid.System, admin.ID, thumbprint); err != nil {
		return &AdminRebindBindError{Cause: err}
	}
	if s.notifier != nil {
		s.notifier.AdminBound()
	}
	return nil
}

// spkiThumbprintFromCertPEM parses a PEM certificate and returns its
// canonical SPKI thumbprint. Mirrors pkg/firstboot's and pkg/bootstrap's own
// unexported helpers of the same name and shape -- neither is importable
// here without a cycle, so this is the service layer's own copy of the same
// three-line operation.
func spkiThumbprintFromCertPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("service: reissued admin cert PEM has no CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return user.ComputeSPKIThumbprint(cert), nil
}
