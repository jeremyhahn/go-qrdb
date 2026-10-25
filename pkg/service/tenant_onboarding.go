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

package service

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ActionTenantOnboard records OnboardTenant: a tenant created with its
// first admin.
const ActionTenantOnboard = "tenant.onboard"

// tenantOnboardingAuditComponent labels an onboarding record that could not
// be written, on qrdb_audit_write_failures_total{component=...}.
const tenantOnboardingAuditComponent = "tenant-onboarding"

// tenantOnboardingSettleTimeout bounds the write of an onboarding's outcome
// record, which is made after the tenant is committed or refused and so
// does not ride on the request's budget.
const tenantOnboardingSettleTimeout = 5 * time.Second

// TenantOnboardingServicer is the one service every transport's tenant-create
// verb delegates to. A tenant is created with its first admin's login
// credential, or not at all.
type TenantOnboardingServicer interface {
	// OnboardTenant creates a tenant and, as one unit, registers its trust
	// anchors, creates its first admin with the tenant's Admin role, and
	// binds the admin's certificate: signed from AdminCSRPEM by the tenant's
	// own Key B CA, or AdminCertPEM verified against the anchors. Every input
	// that can be refused is judged before anything is written, and a later
	// failure undoes what was created (bootstrap.ProvisionTenant, exclusive).
	// It returns the admin's certificate, never a key. The service bounds
	// the onboarding itself (DefaultTenantOnboardingTimeout); a caller's
	// sooner deadline still applies.
	OnboardTenant(ctx context.Context, req *OnboardTenantRequest) (*OnboardTenantResult, error)
}

// OnboardTenantRequest is a tenant to create with its first admin.
type OnboardTenantRequest struct {
	ID                string
	Name              string
	IsolationType     tenant.IsolationType
	BarrierAutoUnseal bool
	// VolumePool places an IsolationVolume/IsolationRaft tenant on this
	// pool (tenant.WithVolumePool). Empty uses the tenant's default pool.
	VolumePool string

	// AdminUsername is the first admin's account name. Required.
	AdminUsername string
	// AdminCSRPEM and AdminCertPEM name the admin's key: exactly one.
	AdminCSRPEM  []byte
	AdminCertPEM []byte
	// AdminChainPEM holds intermediates for AdminCertPEM. Optional.
	AdminChainPEM []byte
	// TrustAnchorsPEM registers the tenant's trust anchors. Required for
	// AdminCertPEM, optional otherwise.
	TrustAnchorsPEM []byte

	// SOUsername, one of SOCSRPEM and SOCertPEM, and SOPIN name the tenant's
	// optional security officer, enrolled with the so role alone: all of
	// them or none. The SO differs from the admin by name and by key.
	SOUsername string
	SOCSRPEM   []byte
	SOCertPEM  []byte
	// SOChainPEM holds intermediates for SOCertPEM. Optional.
	SOChainPEM []byte
	SOPIN      []byte
}

// so is the request's SO, or nil when it names none.
func (r *OnboardTenantRequest) so() *bootstrap.TenantSOConfig {
	if r.SOUsername == "" && len(r.SOCSRPEM) == 0 && len(r.SOCertPEM) == 0 && len(r.SOChainPEM) == 0 && len(r.SOPIN) == 0 {
		return nil
	}
	return &bootstrap.TenantSOConfig{
		Username: r.SOUsername,
		CSRPEM:   r.SOCSRPEM,
		CertPEM:  r.SOCertPEM,
		ChainPEM: r.SOChainPEM,
		PIN:      r.SOPIN,
	}
}

// OnboardTenantResult is a created tenant and its first admin's credential.
type OnboardTenantResult struct {
	Tenant *tenant.TenantInfo
	Admin  TenantAdminCredential
	// SO is the tenant's security officer's credential, shaped like the
	// admin's; nil when the request named no SO.
	SO *TenantAdminCredential
}

// TenantAdminCredential is the first admin as onboarding left it.
type TenantAdminCredential struct {
	// UserID is the admin's account; zero when AwaitsBarrierInit.
	UserID   uint64
	Username string
	// CertPEM is the admin's certificate: signed from the CSR, or the
	// bring-your-own one as enrolled.
	CertPEM []byte
	// Serial is empty for a bring-your-own certificate.
	Serial     string
	Thumbprint string
	// AwaitsBarrierInit reports a tenant that keeps its identity records
	// behind its own barrier: the admin's account is created when the tenant
	// admin initializes that barrier, which only this certificate may do.
	AwaitsBarrierInit bool
}

// tenantOnboardingTenants is the slice of the tenant service onboarding needs.
type tenantOnboardingTenants interface {
	CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType, opts ...tenant.CreateTenantOption) (*tenant.TenantInfo, error)
	tenant.CreateUndoer
}

// TenantOnboardingService is the production TenantOnboardingServicer.
type TenantOnboardingService struct {
	tenants     tenantOnboardingTenants
	users       user.UserServicer
	roles       role.RoleServicer
	certs       cert.CertProvider
	anchors     trustanchor.Registry
	pending     tenantadmin.Store
	trustDomain string
	logger      *slog.Logger
	homes       bootstrap.HomeEnsurer
	audit       audit.AuditLogger
	auditPolicy audit.FailurePolicy
	timeout     time.Duration
	barrierPins bootstrap.FirstAdminPinner
	pendingSign firstboot.CertSigner
	clientTrust ClientTrustRefresher
	pendingSOs  tenantadmin.Store
	soPINs      atomic.Pointer[bootstrap.SOPINSetter]
	// strictSoD is TenantOnboardingDeps.SeparationOfDuties.
	strictSoD bool
}

// DefaultTenantOnboardingTimeout bounds an onboarding from the service's
// side, whatever transport carried it, so a cluster that cannot commit still
// answers a caller that set no deadline of its own. It is sized for a
// sequence of consensus rounds, not one: the tenant record, its pool
// reservation and barrier, then the tenant's roles, its admin's account and
// the certificate binding. Expiry is not a cheap refusal: exclusive
// provisioning undoes what it wrote, and an undone create holds its id until
// every serving node has cleaned up after it (or, before the tenant-undo
// feature is active, retires it for good), so a budget sized for a single
// read turns a slow host into an id that cannot be re-created at once.
const DefaultTenantOnboardingTimeout = 60 * time.Second

// ClientTrustRefresher rebuilds the client-certificate trust this node's
// handshakes check, returning once the rebuilt trust is the one they read.
// clienttrust.Pool.Refresh is the implementation.
type ClientTrustRefresher interface {
	Refresh(ctx context.Context) error
}

// ClientTrustRefresherFunc adapts a function to ClientTrustRefresher.
type ClientTrustRefresherFunc func(ctx context.Context) error

// Refresh calls f.
func (f ClientTrustRefresherFunc) Refresh(ctx context.Context) error { return f(ctx) }

// Compile-time interface check.
var _ TenantOnboardingServicer = (*TenantOnboardingService)(nil)

// TenantOnboardingDeps are the collaborators of a TenantOnboardingService.
// Tenants, Users, Roles, Certs and Logger are required; Anchors is required
// for a tenant onboarded with trust anchors, and Pending for one whose
// identity records are behind its own barrier.
type TenantOnboardingDeps struct {
	Tenants     tenant.TenantServicer
	Users       user.UserServicer
	Roles       role.RoleServicer
	Certs       cert.CertProvider
	Anchors     trustanchor.Registry
	Pending     tenantadmin.Store
	TrustDomain string
	Logger      *slog.Logger
	// Homes provisions an isolated tenant's home store before its first record
	// is written (bootstrap.Dependencies.Homes). Nil leaves an isolated tenant's
	// onboarding to fail closed at its first record write.
	Homes bootstrap.HomeEnsurer
	// Audit is the compliance audit trail OnboardTenant records
	// tenant.onboard on. Nil disables emission.
	Audit audit.AuditLogger
	// AuditPolicy is the node's audit failure posture for OnboardTenant's
	// gating record. The zero value, audit.FailClosed, is the default.
	AuditPolicy audit.FailurePolicy
	// Timeout bounds each OnboardTenant. Zero or negative uses
	// DefaultTenantOnboardingTimeout.
	Timeout time.Duration
	// BarrierAnchors pins a pending first admin's thumbprint as the tenant
	// barrier's epoch-1 anchor (bootstrap.Dependencies.BarrierAnchors). Nil
	// pins nothing, which is right only for a node whose tenant service claims
	// no init statements.
	BarrierAnchors bootstrap.FirstAdminPinner

	// PendingAdminSigner signs the CSR of a first admin whose tenant keeps
	// its identity records -- and its Key B CA -- behind its own barrier
	// (bootstrap.Dependencies.PendingAdminSigner): the node's cluster CA
	// through ceremony.AdminCSRSigner, or a composer's own signer. Nil is a
	// node with no cluster CA key: a system-managed barrier serves from
	// creation, so the tenant's Key B CA signs; a self-managed one is sealed
	// until its admin initializes it, so such a CSR is refused with
	// *ceremony.AdminCredentialRequiresCAError before the tenant is created,
	// and the admin brings their own certificate instead.
	PendingAdminSigner firstboot.CertSigner

	// ClientTrust rebuilds this node's client-certificate trust after a
	// create commits and before it answers, so the admin certificate the
	// answer carries -- signed by the tenant's new login CA -- is admitted
	// on this node's next handshake. Nil skips the rebuild, which is right
	// only for a node whose handshakes do not check client certificates.
	ClientTrust ClientTrustRefresher

	// PendingSOs records a tenant SO pending behind its tenant's own
	// barrier (bootstrap.Dependencies.PendingSOs), beside Pending.
	PendingSOs tenantadmin.Store

	// SOPINs commits a tenant SO's PIN (bootstrap.Dependencies.SOPINs). A
	// node whose SO authenticator is built after this service attaches it
	// with AttachSOPINs. Nil refuses a request that names an SO.
	SOPINs bootstrap.SOPINSetter

	// SeparationOfDuties is the strict separation-of-duties posture, as
	// wiring.ResolveStrictSeparation resolves it: under it the tenant SO
	// must differ from the first admin by username and by key, outside it
	// the SO may be the admin (bootstrap.Dependencies.SeparationOfDuties).
	SeparationOfDuties bool
}

// NewTenantOnboardingService returns the production TenantOnboardingServicer.
func NewTenantOnboardingService(deps TenantOnboardingDeps) *TenantOnboardingService {
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultTenantOnboardingTimeout
	}
	s := &TenantOnboardingService{
		timeout:     timeout,
		tenants:     deps.Tenants,
		users:       deps.Users,
		roles:       deps.Roles,
		certs:       deps.Certs,
		anchors:     deps.Anchors,
		pending:     deps.Pending,
		trustDomain: deps.TrustDomain,
		logger:      deps.Logger,
		homes:       deps.Homes,
		audit:       deps.Audit,
		auditPolicy: deps.AuditPolicy,
		barrierPins: deps.BarrierAnchors,
		pendingSign: deps.PendingAdminSigner,
		clientTrust: deps.ClientTrust,
		pendingSOs:  deps.PendingSOs,
		strictSoD:   deps.SeparationOfDuties,
	}
	if deps.SOPINs != nil {
		s.AttachSOPINs(deps.SOPINs)
	}
	return s
}

// AttachSOPINs sets the seam a tenant SO's PIN is committed through, for a
// node whose SO authenticator is built after this service.
func (s *TenantOnboardingService) AttachSOPINs(pins bootstrap.SOPINSetter) {
	s.soPINs.Store(&pins)
}

// sopins is the attached SO PIN seam, or nil.
func (s *TenantOnboardingService) sopins() bootstrap.SOPINSetter {
	if p := s.soPINs.Load(); p != nil {
		return *p
	}
	return nil
}

// onboardEvent renders a tenant-onboarding event keyed by req.ID -- the
// AFFECTED tenant being created.
func onboardEvent(ctx context.Context, req *OnboardTenantRequest) audit.AuditEvent {
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	return audit.AuditEvent{
		Actor:    actor,
		TenantID: req.ID,
		Resource: "tenant",
		Action:   ActionTenantOnboard,
		Attributes: map[string]any{
			"admin_username": req.AdminUsername,
			"isolation_type": string(req.IsolationType),
		},
	}
}

// OnboardTenant implements TenantOnboardingServicer.
func (s *TenantOnboardingService) OnboardTenant(ctx context.Context, req *OnboardTenantRequest) (*OnboardTenantResult, error) {
	if s.tenants == nil || s.users == nil || s.roles == nil || s.certs == nil || s.logger == nil {
		return nil, &TenantOnboardingNotConfiguredError{}
	}
	if req == nil || req.ID == "" {
		return nil, &TenantOnboardingFieldError{Field: "id"}
	}
	if req.AdminUsername == "" {
		return nil, &TenantOnboardingFieldError{Field: "admin_username"}
	}
	// The tenant ID is its principals' SPIFFE trust domain: refuse one no
	// certificate could ever name before anything is written.
	if err := tenant.ValidateTenantIDTrustDomain(req.ID, s.trustDomain); err != nil {
		return nil, err
	}
	isolation := req.IsolationType
	if isolation == "" {
		// Unspecified isolation is a prefix tenant, as tenant creation has
		// always read it (TenantInfo.EffectiveIsolationType).
		isolation = tenant.IsolationPrefix
	}
	opts := []tenant.CreateTenantOption{tenant.WithBarrierAutoUnseal(req.BarrierAutoUnseal)}
	if req.VolumePool != "" {
		opts = append(opts, tenant.WithVolumePool(req.VolumePool))
	}

	ev := onboardEvent(ctx, req)
	if s.audit != nil {
		ev.Outcome = audit.OutcomePermitted
		if err := audit.EmitGated(ctx, s.audit, tenantOnboardingAuditComponent, s.auditPolicy, ev); err != nil {
			return nil, err
		}
	}
	// The outcome is recorded after the tenant is committed or refused, so
	// it does not ride on the request's budget, which the provisioning may
	// have spent: it writes on its own bound.
	settle := func(err error) {
		if s.audit == nil {
			return
		}
		if err == nil {
			ev.Outcome = audit.OutcomeSuccess
		} else {
			ev.Outcome = audit.OutcomeFailure + ":onboard-failed"
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tenantOnboardingSettleTimeout)
		defer cancel()
		audit.EmitBestEffort(actx, s.audit, tenantOnboardingAuditComponent, ev)
	}

	keyB := &bootstrap.KeyBSigner{Certs: s.certs}
	// With no cluster CA, a pending admin's CSR can be signed only by a Key B
	// that is readable at create: one behind a system-managed barrier.
	pendingSign := s.pendingSign
	if pendingSign == nil && req.BarrierAutoUnseal {
		pendingSign = keyB
	}
	deps := bootstrap.Dependencies{
		Tenants:            s.tenants,
		Users:              s.users,
		Roles:              s.roles,
		Signer:             keyB,
		PendingAdminSigner: pendingSign,
		TrustAnchors:       s.anchors,
		PendingAdmins:      s.pending,
		BarrierAnchors:     s.barrierPins,
		TrustDomain:        s.trustDomain,
		Homes:              s.homes,
		PendingSOs:         s.pendingSOs,
		SOPINs:             s.sopins(),
		SeparationOfDuties: s.strictSoD,
		Logger:             s.logger,
	}
	// The service bounds the provisioning; the caller's sooner deadline still
	// applies. The audit outcome is settled on its own bound, so an
	// onboarding the deadline cut short is still recorded.
	provisionCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := bootstrap.ProvisionTenant(provisionCtx, bootstrap.TenantProvisionConfig{
		TenantID:        req.ID,
		DisplayName:     req.Name,
		Isolation:       isolation,
		AdminUsername:   req.AdminUsername,
		AdminCSRPEM:     req.AdminCSRPEM,
		AdminCertPEM:    req.AdminCertPEM,
		AdminChainPEM:   req.AdminChainPEM,
		TrustAnchorsPEM: req.TrustAnchorsPEM,
		CreateOptions:   opts,
		AdminEnrollment: bootstrap.AdminEnrollCert,
		Exclusive:       true,
		SO:              req.so(),
	}, deps)
	if err != nil {
		settle(err)
		return nil, err
	}
	if result.AdminCompletionDue {
		// The admin's account is a chain of raft writes after the commit:
		// it completes alongside the answer, retried on its own bound, never
		// on the request's. A completion that runs out of its bound is
		// finished by the tenant's next barrier unseal or status read.
		go s.completeAdmin(context.WithoutCancel(ctx), req.ID, deps)
	}
	s.refreshClientTrust(ctx, req.ID)
	settle(nil)
	// The record the create committed is the answer: reading it back would
	// spend the request's budget on one more read and, failing, report a
	// committed tenant as not created.
	var so *TenantAdminCredential
	if result.SOUsername != "" {
		so = &TenantAdminCredential{
			UserID:            result.SOUserID,
			Username:          result.SOUsername,
			CertPEM:           result.SOCertPEM,
			Serial:            result.SOCertSerial,
			Thumbprint:        result.SOCertThumbprint,
			AwaitsBarrierInit: result.SOAwaitsBarrierInit,
		}
	}
	return &OnboardTenantResult{
		SO:     so,
		Tenant: result.Tenant,
		Admin: TenantAdminCredential{
			UserID:            result.AdminUserID,
			Username:          result.AdminUsername,
			CertPEM:           result.AdminCertPEM,
			Serial:            result.AdminCertSerial,
			Thumbprint:        result.AdminCertThumbprint,
			AwaitsBarrierInit: result.AdminAwaitsBarrierInit,
		},
	}, nil
}

// refreshClientTrust rebuilds this node's client-certificate trust so the
// handshake that follows the answer admits the admin certificate it carries.
// The tenant is committed whatever this returns, so a failed rebuild is
// logged, not reported: the node's periodic rebuild admits the certificate
// within its interval.
func (s *TenantOnboardingService) refreshClientTrust(ctx context.Context, tenantID string) {
	if s.clientTrust == nil {
		return
	}
	if err := s.clientTrust.Refresh(ctx); err != nil {
		s.logger.Warn("tenant onboarding: client trust not rebuilt before answering; the periodic rebuild will admit the admin certificate",
			"tenant", tenantID, "error", err)
	}
}

// completeAdmin creates the account of a tenant admin whose barrier was
// serving when the tenant was committed (bootstrap's AdminCompletionDue),
// retried within bootstrap's default bounds, and logs how it ended.
func (s *TenantOnboardingService) completeAdmin(ctx context.Context, tenantID string, deps bootstrap.Dependencies) {
	done, err := bootstrap.CompletePendingTenantAdminWithin(ctx, tenantID, deps, bootstrap.AdminCompletionBounds{})
	switch {
	case err == nil && done:
		s.logger.Info("tenant admin account created after its tenant's create answered",
			slog.String("tenant", tenantID))
	case err == nil:
	default:
		s.logger.Error("tenant admin completion stopped; the tenant's next barrier unseal or status read completes it",
			slog.String("tenant", tenantID), slog.Any("error", err))
	}
}
