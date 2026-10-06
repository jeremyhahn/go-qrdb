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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
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
	// It returns the admin's certificate, never a key.
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
}

// OnboardTenantResult is a created tenant and its first admin's credential.
type OnboardTenantResult struct {
	Tenant *tenant.TenantInfo
	Admin  TenantAdminCredential
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
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
	DeleteTenant(ctx context.Context, tenantID string) error
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
	audit       audit.AuditLogger
	auditPolicy audit.FailurePolicy
}

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
	// Audit is the compliance audit trail OnboardTenant records
	// tenant.onboard on. Nil disables emission.
	Audit audit.AuditLogger
	// AuditPolicy is the node's audit failure posture for OnboardTenant's
	// gating record. The zero value, audit.FailClosed, is the default.
	AuditPolicy audit.FailurePolicy
}

// NewTenantOnboardingService returns the production TenantOnboardingServicer.
func NewTenantOnboardingService(deps TenantOnboardingDeps) *TenantOnboardingService {
	return &TenantOnboardingService{
		tenants:     deps.Tenants,
		users:       deps.Users,
		roles:       deps.Roles,
		certs:       deps.Certs,
		anchors:     deps.Anchors,
		pending:     deps.Pending,
		trustDomain: deps.TrustDomain,
		logger:      deps.Logger,
		audit:       deps.Audit,
		auditPolicy: deps.AuditPolicy,
	}
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
	settle := func(err error) {
		if s.audit == nil {
			return
		}
		if err == nil {
			ev.Outcome = audit.OutcomeSuccess
		} else {
			ev.Outcome = audit.OutcomeFailure + ":onboard-failed"
		}
		audit.EmitBestEffort(ctx, s.audit, tenantOnboardingAuditComponent, ev)
	}

	result, err := bootstrap.ProvisionTenant(ctx, bootstrap.TenantProvisionConfig{
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
	}, bootstrap.Dependencies{
		Tenants:       s.tenants,
		Users:         s.users,
		Roles:         s.roles,
		Signer:        &bootstrap.KeyBSigner{Certs: s.certs},
		TrustAnchors:  s.anchors,
		PendingAdmins: s.pending,
		TrustDomain:   s.trustDomain,
		Logger:        s.logger,
	})
	if err != nil {
		settle(err)
		return nil, err
	}
	info, err := s.tenants.GetTenant(ctx, req.ID)
	if err != nil {
		settle(err)
		return nil, err
	}
	settle(nil)
	return &OnboardTenantResult{
		Tenant: info,
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
