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

package bootstrap

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/retry"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// KeyBSigner is the firstboot.CertSigner qrdb's own transports compose
// tenant onboarding with: it signs the first admin's CSR with the tenant's
// OWN Key B CA (cert.CertProvider.IssueClientCert, through
// certenroll.IssueFromCSR), so the admin's login certificate is issued by the
// login CA of the tenant it logs in to. A composer with its own CA (go-xkms)
// injects its own firstboot.CertSigner instead.
type KeyBSigner struct {
	Certs cert.CertProvider
}

// SignAdminCSR implements firstboot.CertSigner.
func (s *KeyBSigner) SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) ([]byte, error) {
	issued, err := certenroll.IssueFromCSR(ctx, s.Certs, tenantID, subject, csrPEM, 0)
	if err != nil {
		return nil, err
	}
	return issued.CertPEM, nil
}

// RevokeCert lets ProvisionTenant revoke a certificate it signed for an
// onboarding it then undid.
func (s *KeyBSigner) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
	return s.Certs.RevokeCert(ctx, serial)
}

// now is the provisioning clock.
func (deps Dependencies) now() time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

// certRevoker is the optional capability of a firstboot.CertSigner to revoke
// what it signed.
type certRevoker interface {
	RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error)
}

// certBindingAdder binds a certificate to an account with its expiry. qrdb's
// user service implements it; ProvisionTenant uses it when deps.Users does.
type certBindingAdder interface {
	AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
}

// tenantUndoer and userDeleter are the undo capabilities an exclusive
// ProvisionTenant requires of deps.Tenants and deps.Users. An undone tenant
// create is not a deletion: see tenant.CreateUndoer.
type tenantUndoer interface {
	UndoCreateTenant(ctx context.Context, tenantID string) error
}

type userDeleter interface {
	DeleteUser(ctx context.Context, id uint64) error
}

// adminSigner is the signer for the first admin's CSR: PendingAdminSigner for
// an admin recorded as pending, Signer otherwise. A pending admin's CSR with
// no PendingAdminSigner is refused as system first boot refuses a CSR on a
// node with no cluster CA: the tenant's own CA is sealed behind the barrier
// that admin has yet to initialize, so nothing else could sign it.
func (deps Dependencies) adminSigner(pending bool) (firstboot.CertSigner, error) {
	if pending {
		if deps.PendingAdminSigner == nil {
			return nil, &ceremony.AdminCredentialRequiresCAError{Path: "csr-sign"}
		}
		return deps.PendingAdminSigner, nil
	}
	if deps.Signer == nil {
		return nil, &MissingProvisioningSeamError{Seam: "Dependencies.Signer to sign the admin's CSR"}
	}
	return deps.Signer, nil
}

// adminPlan is the first admin's credential, judged before anything is
// written.
type adminPlan struct {
	csrPEM  []byte
	signer  firstboot.CertSigner
	byoc    *certenroll.BYOC
	anchors []*x509.Certificate
}

// planAdmin judges every input that can be refused -- the trust anchors, the
// CSR, the bring-your-own certificate against those anchors and the admin's
// identity -- so that a refusal happens before the tenant exists. pending
// reports whether the admin is recorded as pending rather than created, which
// decides the CSR's signer (Dependencies.adminSigner).
func planAdmin(cfg TenantProvisionConfig, deps Dependencies, mode AdminEnrollmentMode, adminUsername string, pending bool) (*adminPlan, error) {
	plan := &adminPlan{}
	now := deps.now()
	if len(cfg.TrustAnchorsPEM) > 0 {
		if deps.TrustAnchors == nil {
			return nil, &MissingProvisioningSeamError{Seam: "Dependencies.TrustAnchors to register trust anchors"}
		}
		anchors, err := trustanchor.ParseAnchors(cfg.TrustAnchorsPEM, now)
		if err != nil {
			return nil, &InvalidTrustAnchorsError{Cause: err}
		}
		plan.anchors = anchors
	}
	if mode != AdminEnrollCert {
		return plan, nil
	}
	// The admin's name ends up in a certificate's Subject and, under a trust
	// domain, its SPIFFE SAN. A name the CA would refuse to certify must be
	// refused here, before the tenant is created -- a refusal after it would
	// be undone by deleting the tenant, which retires its id. The trust domain
	// is a dependency, not a credential, so its absence is a missing seam.
	if deps.TrustDomain == "" {
		return nil, &MissingProvisioningSeamError{Seam: "Dependencies.TrustDomain to name the admin's SPIFFE ID"}
	}
	if _, err := cert.ClientSPIFFEURIs(deps.TrustDomain, cfg.TenantID, adminUsername); err != nil {
		return nil, &AdminCredentialRejectedError{Cause: err}
	}
	switch {
	case len(cfg.AdminCSRPEM) > 0 && len(cfg.AdminCertPEM) > 0:
		return nil, &ConflictingAdminCredentialError{}
	case len(cfg.AdminCSRPEM) > 0:
		signer, err := deps.adminSigner(pending)
		if err != nil {
			return nil, err
		}
		plan.signer = signer
		if _, err := cert.ParseCSR(cfg.AdminCSRPEM); err != nil {
			return nil, &AdminCredentialRejectedError{Cause: err}
		}
		plan.csrPEM = cfg.AdminCSRPEM
	case len(cfg.AdminCertPEM) > 0:
		// A new tenant has no anchors but the ones in this request, so the
		// certificate is verified against exactly those.
		byoc, err := certenroll.VerifyBYOCAgainst(plan.anchors, cfg.TenantID, adminUsername, cfg.AdminCertPEM, cfg.AdminChainPEM, deps.TrustDomain, now)
		if err != nil {
			return nil, &AdminCredentialRejectedError{Cause: err}
		}
		plan.byoc = byoc
	default:
		return nil, &MissingAdminCredentialError{}
	}
	return plan, nil
}

// adminCredential is the admin's certificate as onboarding binds it.
type adminCredential struct {
	certPEM    []byte
	thumbprint string
	notAfter   time.Time
	serial     string
}

// issue produces the admin's certificate from the plan: signed from the CSR,
// or the verified bring-your-own one as is.
func (p *adminPlan) issue(ctx context.Context, tenantID, adminUsername string) (*adminCredential, error) {
	if p.byoc != nil {
		return &adminCredential{
			certPEM:    trustanchor.EncodePEM(p.byoc.Certificate),
			thumbprint: p.byoc.Thumbprint,
			notAfter:   p.byoc.NotAfter,
		}, nil
	}
	certPEM, err := p.signer.SignAdminCSR(ctx, tenantID, adminUsername, p.csrPEM)
	if err != nil {
		return nil, &TenantAdminCertError{Cause: err}
	}
	certs, err := trustanchor.ParseCertificates(certPEM)
	if err != nil {
		return nil, &TenantAdminCertError{Cause: err}
	}
	return &adminCredential{
		certPEM:    certPEM,
		thumbprint: user.ComputeSPKIThumbprint(certs[0]),
		notAfter:   certs[0].NotAfter,
		serial:     cert.FormatSerial(certs[0].SerialNumber),
	}, nil
}

// bind binds the admin's certificate to its account: through the user
// service's AddCertBinding when deps.Users provides it (the binding then
// carries the certificate's expiry), else through deps.Binder.
func bindAdmin(ctx context.Context, deps Dependencies, tenantID string, adminUser *user.User, c *adminCredential) error {
	if adder, ok := deps.Users.(certBindingAdder); ok {
		if _, err := adder.AddCertBinding(ctx, adminUser.ID, c.thumbprint, c.notAfter.Unix()); err != nil {
			return &TenantAdminCertError{Cause: err}
		}
		return nil
	}
	if deps.Binder != nil {
		if err := deps.Binder.BindAdminCert(ctx, tenantID, adminUser.ID, c.thumbprint); err != nil {
			return &TenantAdminCertError{Cause: err}
		}
		return nil
	}
	return &MissingProvisioningSeamError{Seam: "a certificate binder (Dependencies.Users with AddCertBinding, or Dependencies.Binder)"}
}

// undoStack records, in order, how to take back what an exclusive
// ProvisionTenant created, so a failure leaves nothing behind.
type undoStack struct {
	steps []undoStep
}

type undoStep struct {
	name string
	fn   func(ctx context.Context) error
}

func (u *undoStack) push(name string, fn func(ctx context.Context) error) {
	u.steps = append(u.steps, undoStep{name: name, fn: fn})
}

// run undoes every step, last first. It runs on a context that outlives the
// request's own -- a cancelled request is the usual reason to undo -- and
// returns the steps that failed.
func (u *undoStack) run(ctx context.Context, logger *slog.Logger, tenantID string) []error {
	undoCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var failed []error
	for i := len(u.steps) - 1; i >= 0; i-- {
		step := u.steps[i]
		if err := step.fn(undoCtx); err != nil {
			logger.Error("bootstrap: undoing tenant provisioning failed",
				slog.String("tenant", tenantID), slog.String("step", step.name), slog.Any("error", err))
			failed = append(failed, &UndoStepError{Step: step.name, Cause: err})
		}
	}
	return failed
}

// UndoStepError names an undo step of an exclusive ProvisionTenant that
// failed.
type UndoStepError struct {
	Step  string
	Cause error
}

// Error implements error.
func (e *UndoStepError) Error() string { return e.Step + ": " + e.Cause.Error() }

// Unwrap exposes the cause.
func (e *UndoStepError) Unwrap() error { return e.Cause }

// HomeLeader reports whether this node leads a tenant's home Raft group, the
// one replica that completes the tenant's pending admin.
type HomeLeader interface {
	LeadsHome(tenantID string) bool
}

// CompletePendingTenantAdmin creates the account of a tenant admin onboarded
// before the tenant's identity records could be written (pkg/tenantadmin),
// once they can: it seeds the tenant's built-in roles, creates the admin
// account with the tenant's Admin role -- or adopts an existing one of that
// name -- binds the onboarding certificate to it, and deletes the pending
// record. It reports whether there was a pending admin to complete.
//
// It runs once per tenant, on the node leading the tenant's home group
// (Dependencies.HomeLeader): on any other node a pending admin is left to
// that leader and the call returns *AdminCompletionDeferredError having
// written nothing. Every write it makes is guarded -- the roles and the
// account are created only if absent, the binding is a compare-and-swap that
// recognises its own earlier commit, and the record is deleted only while it
// is still the one this call read -- so a retry, or a new leader finishing
// what a deposed one started, completes the admin exactly once. A call that
// finds the record already gone, or loses the delete to one that finished
// first, reports false with a nil error.
//
// It is safe to call whenever the tenant's barrier becomes usable: on its
// initialization, and on every unseal, so an attempt that failed part-way is
// finished by the next one.
//
// Uses: PendingAdmins, Users (with AddCertBinding, or Binder), Roles,
// HomeLeader, Logger.
func CompletePendingTenantAdmin(ctx context.Context, tenantID string, deps Dependencies) (bool, error) {
	if deps.Logger == nil {
		return false, &MissingLoggerError{}
	}
	if deps.PendingAdmins == nil {
		return false, nil
	}
	pending, err := deps.PendingAdmins.Get(ctx, tenantID)
	if err != nil {
		return false, &PendingTenantAdminError{Op: "read", Cause: err}
	}
	if pending == nil {
		return false, nil
	}
	if deps.HomeLeader != nil && !deps.HomeLeader.LeadsHome(tenantID) {
		return false, &AdminCompletionDeferredError{TenantID: tenantID}
	}
	if deps.Users == nil || deps.Roles == nil {
		return false, &MissingProvisioningSeamError{Seam: "Dependencies.Users and Dependencies.Roles to complete a pending admin"}
	}
	if _, err := seedTenantRoles(ctx, deps, tenantID); err != nil {
		return false, err
	}
	// System roles are undeletable and name-immutable (no-compat-cleanup
	// deleted the UUIDv5->UUIDv8 dual-read compatibility), so the role id is
	// always its canonical derivation -- no resolver, no store lookup, no
	// legacy fallback. See bootstrap.go's ProvisionTenant Step 3.
	adminRoleID := role.SystemRoleID(tenantID, role.RoleNameAdmin)
	adminUser, err := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             pending.Username,
		TenantID:             tenantID,
		RoleIDs:              []string{adminRoleID},
	})
	if err != nil {
		var exists *user.UsernameExistsError
		if !errors.As(err, &exists) {
			return false, &TenantAdminUserCreationError{Cause: err}
		}
		// A previous attempt created the account and failed before deleting
		// the record: adopt it.
		lookup := userResolver(deps)
		if lookup == nil {
			return false, &TenantAdminUserCreationError{Cause: err}
		}
		if adminUser, err = lookup.GetUserByUsername(ctx, tenantID, pending.Username); err != nil {
			return false, &TenantAdminUserCreationError{Cause: err}
		}
		// Adopt it only if it IS that admin: an account of the same name
		// that does not hold the tenant's Admin role is someone else's, and
		// binding the onboarded admin's key to it would hand the admin's
		// credential an account it was never issued for -- or hand that
		// account's holder the admin's place.
		if !slices.Contains(adminUser.RoleIDs, adminRoleID) {
			return false, &PendingAdminAccountConflictError{TenantID: tenantID, Username: pending.Username}
		}
	}
	c := &adminCredential{thumbprint: pending.Thumbprint, notAfter: pending.NotAfter, serial: pending.Serial}
	if err := bindAdmin(ctx, deps, tenantID, adminUser, c); err != nil {
		return false, err
	}
	deleted, err := deps.PendingAdmins.Delete(ctx, tenantID, pending)
	if err != nil {
		return false, &PendingTenantAdminError{Op: "delete", Cause: err}
	}
	if !deleted {
		// Another completion read the same record and deleted it first; the
		// account and binding this call found or made are the ones it made.
		return false, nil
	}
	deps.Logger.Info("bootstrap: pending tenant admin completed",
		slog.String("tenant", tenantID), slog.String("admin", pending.Username))
	return true, nil
}

// AdminCompletionBounds bounds CompletePendingTenantAdminWithin. A zero
// field takes its default.
type AdminCompletionBounds struct {
	// Budget is the time every attempt together may take. Default 2m.
	Budget time.Duration
	// AttemptTimeout bounds one attempt. Default 10s.
	AttemptTimeout time.Duration
	// Delay is the wait before the first retry; each later wait doubles,
	// up to MaxDelay. Defaults 250ms and 5s.
	Delay    time.Duration
	MaxDelay time.Duration
}

// withDefaults returns b with every zero field set to its default.
func (b AdminCompletionBounds) withDefaults() AdminCompletionBounds {
	if b.Budget <= 0 {
		b.Budget = 2 * time.Minute
	}
	if b.AttemptTimeout <= 0 {
		b.AttemptTimeout = 10 * time.Second
	}
	if b.Delay <= 0 {
		b.Delay = 250 * time.Millisecond
	}
	if b.MaxDelay <= 0 {
		b.MaxDelay = 5 * time.Second
	}
	return b
}

// CompletePendingTenantAdminWithin runs CompletePendingTenantAdmin until it
// succeeds, each attempt on its own AttemptTimeout and all of them within
// Budget of ctx. It is how a caller completes the admin of a tenant whose
// barrier was serving when ProvisionTenant committed it
// (TenantProvisionResult.AdminCompletionDue), after answering its own
// request: hand it a context that request's end does not cancel.
//
// An attempt cut off or failed part-way is retried, which
// CompletePendingTenantAdmin's guarded writes make safe. On a node that does
// not lead the tenant's home group each attempt defers, so the loop waits
// for the record to go (the leader completed it) or for this node to take
// the leadership, and then completes it here. It stops without
// retrying on tenant.ErrTenantSealed -- the barrier-ready hook completes the
// admin when the barrier next unseals -- and on an error no retry can
// change. When Budget runs out it returns the error of the last attempt the
// budget did not cut off, so the caller learns why the admin is still
// pending rather than only that time ran out; an attempt the budget ends
// part-way reports nothing new. Whatever it returns, a pending record left
// behind is completed by
// the hook on the tenant's next barrier unseal or status read.
func CompletePendingTenantAdminWithin(ctx context.Context, tenantID string, deps Dependencies, b AdminCompletionBounds) (bool, error) {
	if deps.Logger == nil {
		return false, &MissingLoggerError{}
	}
	b = b.withDefaults()
	// The account and its roles are the tenant's own records, written as
	// ProvisionTenant writes them.
	ctx, cancel := context.WithTimeout(tenant.WithTenantID(ctx, tenantID), b.Budget)
	defer cancel()
	var done bool
	var prev error // the last attempt's error, for an attempt the budget cuts off
	err := retry.Do(ctx, &retry.Config{
		MaxAttempts:   math.MaxInt,
		Backoff:       &retry.ExponentialBackoff{InitialDelay: b.Delay, MaxDelay: b.MaxDelay, Multiplier: 2},
		RetryableFunc: adminCompletionRetryable,
		OnRetry: func(attempt int, err error, wait time.Duration) {
			deps.Logger.Warn("bootstrap: tenant admin completion failed; retrying",
				slog.String("tenant", tenantID), slog.Int("attempt", attempt+1),
				slog.Duration("retry_in", wait), slog.Any("error", err))
		},
	}, func(ctx context.Context) error {
		actx, cancel := context.WithTimeout(ctx, b.AttemptTimeout)
		defer cancel()
		var err error
		done, err = CompletePendingTenantAdmin(actx, tenantID, deps)
		if err != nil && prev != nil && ctx.Err() != nil &&
			(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			// The budget ran out under this attempt: what it reports is
			// the cut-off, not why the admin is still pending.
			return prev
		}
		prev = err
		return err
	})
	if err != nil {
		return false, err
	}
	return done, nil
}

// CompletePendingTenantAdminOnce makes one CompletePendingTenantAdmin attempt
// on ctx, within b.AttemptTimeout, writing the tenant's own records as
// CompletePendingTenantAdminWithin does. It is how a request that must not
// wait for the tenant's home group -- a Shamir barrier init, which unseals
// only its own node -- tries the admin once and leaves a record it cannot
// complete yet to the barrier-ready hook's next unseal or status read.
func CompletePendingTenantAdminOnce(ctx context.Context, tenantID string, deps Dependencies, b AdminCompletionBounds) (bool, error) {
	ctx, cancel := context.WithTimeout(tenant.WithTenantID(ctx, tenantID), b.withDefaults().AttemptTimeout)
	defer cancel()
	return CompletePendingTenantAdmin(ctx, tenantID, deps)
}

// adminCompletionRetryable reports whether a failed
// CompletePendingTenantAdmin attempt is worth repeating: everything but a
// sealed tenant and an error no retry can change.
func adminCompletionRetryable(err error) bool {
	var conflict *PendingAdminAccountConflictError
	var seam *MissingProvisioningSeamError
	return !errors.Is(err, tenant.ErrTenantSealed) && !errors.As(err, &conflict) && !errors.As(err, &seam)
}

// userResolver returns the username lookup provisioning should use: the
// explicit dependency when supplied, otherwise the Users surface itself when
// it implements firstboot.UserResolver.
func userResolver(deps Dependencies) firstboot.UserResolver {
	if deps.UserResolver != nil {
		return deps.UserResolver
	}
	if r, ok := deps.Users.(firstboot.UserResolver); ok {
		return r
	}
	return nil
}

// pendingFrom is the record a dedicated-isolation tenant's onboarding
// stores.
func pendingFrom(adminUsername string, c *adminCredential) *tenantadmin.Pending {
	return &tenantadmin.Pending{
		Username:   adminUsername,
		Thumbprint: c.thumbprint,
		NotAfter:   c.notAfter,
		Serial:     c.serial,
	}
}
