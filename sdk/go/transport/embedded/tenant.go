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
	"bytes"
	"context"
	"encoding/hex"
	"maps"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantinit"
)

// mapTenantInfo converts a tenant.TenantInfo to a transport.TenantInfo.
func mapTenantInfo(info *tenant.TenantInfo) transport.TenantInfo {
	ti := transport.TenantInfo{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: transport.TenantIsolationType(info.EffectiveIsolationType()),
		VolumePool:    info.VolumePool,
		Description:   info.Description,
		Metadata:      maps.Clone(info.Metadata),
		Status:        transport.TenantStatus(info.Status),
		CreatedAt:     info.CreatedAt,
		UpdatedAt:     info.UpdatedAt,
	}
	if info.Quotas != nil {
		ti.Quotas = mapQuotas(info.Quotas)
	}
	if info.Entitlements != nil {
		ti.Entitlements = mapEntitlements(info.Entitlements)
	}
	if refusal := tenant.RefusalOf(info.Refusal); refusal != nil {
		ti.Refusal = &transport.TenantRefusal{Code: refusal.Code, Message: refusal.Message}
	}
	return ti
}

// mapQuotas converts a quota.Quotas to a transport.TenantQuotas pointer.
func mapQuotas(q *quota.Quotas) *transport.TenantQuotas {
	return &transport.TenantQuotas{
		MaxDatabases:          q.MaxDatabases,
		MaxRaftGroups:         q.MaxRaftGroups,
		MaxReplicasPerGroup:   q.MaxReplicasPerGroup,
		MaxTotalReplicas:      q.MaxTotalReplicas,
		MaxCollections:        q.MaxCollections,
		MaxKeys:               q.MaxKeys,
		MaxStorageBytes:       q.MaxStorageBytes,
		MaxDatabaseSizeBytes:  q.MaxDatabaseSizeBytes,
		MaxUsers:              q.MaxUsers,
		MaxRoles:              q.MaxRoles,
		MaxCertificates:       q.MaxCertificates,
		MaxBackups:            q.MaxBackups,
		MaxVectorDims:         q.MaxVectorDims,
		MaxTSMetrics:          q.MaxTSMetrics,
		SystemBarrierQuota:    q.SystemBarrierQuota,
		MaxDedicatedWALGroups: q.MaxDedicatedWALGroups,
	}
}

// mapEntitlements converts a quota.Entitlements to a transport.TenantEntitlements pointer.
func mapEntitlements(e *quota.Entitlements) *transport.TenantEntitlements {
	return &transport.TenantEntitlements{
		AllowedIsolationModes:     e.AllowedIsolationModes,
		AllowDedicatedRaft:        e.AllowDedicatedRaft,
		AllowVolumeStorage:        e.AllowVolumeStorage,
		AllowSystemManagedBarrier: e.AllowSystemManagedBarrier,
		AllowedBarrierModes:       e.AllowedBarrierModes,
		AllowDedicatedWAL:         e.AllowDedicatedWAL,
		AllowedVolumePools:        e.AllowedVolumePools,
	}
}

// WithTenantService injects a TenantServicer into the transport.
func WithTenantService(svc tenant.TenantServicer) Option {
	return func(t *Transport) {
		t.tenantSvc = svc
	}
}

// CreateTenant creates a new tenant by delegating to the TenantServicer.
// Minting a tenant is platform-operator-only (AuthMiddleware.AuthorizeTarget/ScopeCross)
// exactly as on every network transport, where tenants:create is ScopeCross.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	ctx, err := t.admit(ctx, "CreateTenant")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.onboarding == nil {
		return nil, &NilServiceError{Service: "TenantOnboardingServicer"}
	}
	if err := t.authorizeTenantSelf(ctx, permission.TenantsCreate); err != nil {
		return nil, err
	}

	result, err := t.onboarding.OnboardTenant(ctx, &service.OnboardTenantRequest{
		ID:                req.ID,
		Name:              req.Name,
		IsolationType:     tenant.IsolationType(req.IsolationType),
		BarrierAutoUnseal: req.BarrierAutoUnseal,
		VolumePool:        req.VolumePool,
		AdminUsername:     req.AdminUsername,
		AdminCSRPEM:       []byte(req.AdminCSRPEM),
		AdminCertPEM:      []byte(req.AdminCertPEM),
		AdminChainPEM:     []byte(req.AdminChainPEM),
		TrustAnchorsPEM:   []byte(req.TrustAnchorsPEM),
	})
	if err != nil {
		// An undone id a serving node has not yet released names its retry
		// delay, as the remote transports carry it.
		return nil, delayed("CreateTenant", err)
	}

	return &transport.CreateTenantResponse{
		Tenant: mapTenantInfo(result.Tenant),
		Admin: transport.TenantAdminCredential{
			UserID:            result.Admin.UserID,
			Username:          result.Admin.Username,
			CertPEM:           string(result.Admin.CertPEM),
			Serial:            result.Admin.Serial,
			Thumbprint:        result.Admin.Thumbprint,
			AwaitsBarrierInit: result.Admin.AwaitsBarrierInit,
		},
	}, nil
}

// anchorsToTransport converts a tenant's trust anchors.
func anchorsToTransport(anchors []trustanchor.Anchor) *transport.TrustAnchorList {
	out := &transport.TrustAnchorList{Anchors: make([]transport.TrustAnchor, 0, len(anchors))}
	for _, a := range anchors {
		out.Anchors = append(out.Anchors, transport.TrustAnchor{
			Fingerprint: a.Fingerprint,
			Subject:     a.Subject,
			NotBefore:   a.NotBefore,
			NotAfter:    a.NotAfter,
			CertPEM:     string(a.CertPEM),
			AddedAt:     a.AddedAt,
		})
	}
	return out
}

// AddTrustAnchors registers trust anchors of req.TenantID, confined to the
// caller's own tenant (tenant.Confine) as trust_anchors:add's ScopeSelfStrict
// confines it on the network transports.
func (t *Transport) AddTrustAnchors(ctx context.Context, req *transport.AddTrustAnchorsRequest) (*transport.TrustAnchorList, error) {
	ctx, err := t.admit(ctx, "AddTrustAnchors")
	if err != nil {
		return nil, err
	}
	if t.certEnroll == nil {
		return nil, &transport.NotImplementedError{Method: "AddTrustAnchors"}
	}
	tenantID, err := tenant.Confine(ctx, req.TenantID, "add trust anchors")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.TrustAnchorsAdd); err != nil {
		return nil, err
	}
	anchors, err := t.certEnroll.AddTrustAnchors(ctx, tenantID, []byte(req.CertsPEM))
	if err != nil {
		return nil, err
	}
	return anchorsToTransport(anchors), nil
}

// ListTrustAnchors lists tenantID's trust anchors, confined to the caller's
// own tenant.
func (t *Transport) ListTrustAnchors(ctx context.Context, tenantID string) (*transport.TrustAnchorList, error) {
	ctx, err := t.admit(ctx, "ListTrustAnchors")
	if err != nil {
		return nil, err
	}
	if t.certEnroll == nil {
		return nil, &transport.NotImplementedError{Method: "ListTrustAnchors"}
	}
	id, err := tenant.Confine(ctx, tenantID, "list trust anchors")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, id, permission.TrustAnchorsList); err != nil {
		return nil, err
	}
	anchors, err := t.certEnroll.ListTrustAnchors(ctx, id)
	if err != nil {
		return nil, err
	}
	return anchorsToTransport(anchors), nil
}

// RemoveTrustAnchor withdraws one of tenantID's trust anchors, confined to
// the caller's own tenant.
func (t *Transport) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	ctx, err := t.admit(ctx, "RemoveTrustAnchor")
	if err != nil {
		return err
	}
	if t.certEnroll == nil {
		return &transport.NotImplementedError{Method: "RemoveTrustAnchor"}
	}
	id, err := tenant.Confine(ctx, tenantID, "remove a trust anchor")
	if err != nil {
		return err
	}
	if err := t.authorizeCertTenant(ctx, id, permission.TrustAnchorsRemove); err != nil {
		return err
	}
	return t.certEnroll.RemoveTrustAnchor(ctx, id, fingerprint)
}

// GetTenant retrieves a tenant by ID by delegating to the TenantServicer.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	ctx, err := t.admit(ctx, "GetTenant")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantRecord(ctx, tenantID, permission.TenantsRead); err != nil {
		return nil, err
	}

	info, err := t.tenantSvc.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return &transport.GetTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// ListTenants lists all tenants by delegating to the TenantServicer.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	ctx, err := t.admit(ctx, "ListTenants")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantSelf(ctx, permission.TenantsList); err != nil {
		return nil, err
	}

	tenants, err := t.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	// Narrow to what the caller may enumerate: every tenant for a caller
	// holding tenants:list:cross, and only the caller's own tenant for
	// anyone else -- the same rule FilterTenantList applies on every
	// network transport, because the full list discloses every tenant's
	// configuration (data_root included) to a principal with no authority
	// over them.
	if tenants, err = t.filterTenantList(ctx, tenants); err != nil {
		return nil, err
	}

	result := make([]transport.TenantInfo, 0, len(tenants))
	for _, info := range tenants {
		result = append(result, mapTenantInfo(&info))
	}

	return &transport.ListTenantsResponse{
		Tenants: result,
	}, nil
}

// ListDeletedTenants returns one page of retired tenant ids by delegating to
// the TenantServicer.
func (t *Transport) ListDeletedTenants(ctx context.Context, req *transport.ListDeletedTenantsRequest) (*transport.ListDeletedTenantsResponse, error) {
	ctx, err := t.admit(ctx, "ListDeletedTenants")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantSelf(ctx, permission.TenantsListDeleted); err != nil {
		return nil, err
	}
	if req == nil {
		req = &transport.ListDeletedTenantsRequest{}
	}
	page, err := t.tenantSvc.ListDeletedTenants(ctx, req.PageToken, int(req.PageSize))
	if err != nil {
		return nil, err
	}
	out := &transport.ListDeletedTenantsResponse{
		Tenants:       make([]transport.DeletedTenant, 0, len(page.Tombstones)),
		NextPageToken: page.NextPageToken,
	}
	for _, ts := range page.Tombstones {
		out.Tenants = append(out.Tenants, transport.DeletedTenant{
			ID:            ts.ID,
			Name:          ts.Name,
			IsolationType: transport.TenantIsolationType(ts.IsolationType),
			VolumePool:    ts.VolumePool,
			CreatedAt:     ts.CreatedAt,
			DeletedAt:     ts.DeletedAt,
		})
	}
	return out, nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on this node by
// delegating to the TenantServicer.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	ctx, err := t.admit(ctx, "ReconcileDeletedTenants")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantSelf(ctx, permission.TenantsReconcileDeleted); err != nil {
		return nil, err
	}
	report, err := t.tenantSvc.ReconcileDeletedTenants(ctx)
	if rerr := tenant.ReconcileResponseError(report, err); rerr != nil {
		return nil, rerr
	}
	out := &transport.ReconcileDeletedTenantsResponse{Inspected: report.Inspected}
	for _, r := range report.Reclaimed {
		out.Reclaimed = append(out.Reclaimed, transport.TenantResiduePath{TenantID: r.TenantID, Path: r.Path})
	}
	for _, s := range report.Skipped {
		out.Skipped = append(out.Skipped, transport.TenantResiduePath{TenantID: s.TenantID, Path: s.Path, Reason: s.Reason})
	}
	for _, f := range report.Failed {
		out.Failed = append(out.Failed, transport.TenantResidueFailure{TenantID: f.TenantID, Op: f.Op, Path: f.Path, Error: f.Error})
	}
	// In-process, the node's own *tenant.ResidueReconcileFailuresError is kept
	// reachable through errors.As.
	return out, transport.ReconcileFailure(out, err)
}

// UpdateTenant applies a partial update to an existing tenant by delegating
// to the TenantServicer. Non-empty patch fields replace the stored value.
// Renaming a tenant is self-service, but a patch that also changes the
// isolation tier is platform-operator-only, exactly as
// AuthMiddleware.AuthorizeTenantUpdate checks tenants:set_isolation
// (ScopeCross) on every network transport -- the SAME decision below, and
// the only one: there is no separate structural check here.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	ctx, err := t.admit(ctx, "UpdateTenant")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	// The ENTRY check: tenants:update against the caller's OWN (ambient)
	// tenant, exactly as REST's route-level middleware authorizes it
	// (s.route(http.MethodPut, "/v1/tenants/{id}", permission.TenantsUpdate, ...))
	// before its handler ever runs -- the step that charges the rate-limit
	// token and writes the audit record. AuthorizeTenantTarget, which
	// authorizeTenantUpdate below calls as the RE-check against the tenant
	// the request NAMES, deliberately charges neither on a grant (see its
	// own doc comment): without this entry check first, a successful
	// embedded UpdateTenant was charged and audited nowhere at all.
	if err := t.authorizeTenantSelf(ctx, permission.TenantsUpdate); err != nil {
		return nil, err
	}

	patch := tenant.TenantPatch{
		Name:          req.Name,
		IsolationType: tenant.IsolationType(req.IsolationType),
		Description:   req.Description,
		Metadata:      req.Metadata,
	}
	if req.Status != nil {
		s := tenant.Status(*req.Status)
		patch.Status = &s
	}
	// The RE-check: tenants:update against the named tenant, plus
	// tenants:set_isolation (platform-operator only) when the patch also
	// changes the isolation tier, or tenants:set_status when it changes the
	// status -- the same two-permission rule
	// AuthorizeTenantUpdate enforces on every network transport.
	if err := t.authorizeTenantUpdate(ctx, req.TenantID, patch); err != nil {
		return nil, err
	}
	info, err := t.tenantSvc.UpdateTenant(ctx, req.TenantID, patch)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// UpdateTenantQuotas updates the resource quotas for a tenant by
// delegating to the TenantServicer. A tenant's quotas are its plan, set by
// the platform operator only (AuthMiddleware/ScopeCross), exactly as
// tenants:set_quotas is ScopeCross on every network transport: a tenant
// Admin cannot raise its own limits.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	ctx, err := t.admit(ctx, "UpdateTenantQuotas")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantsSetQuotas); err != nil {
		return nil, err
	}

	q := &quota.Quotas{
		MaxDatabases:          req.Quotas.MaxDatabases,
		MaxRaftGroups:         req.Quotas.MaxRaftGroups,
		MaxReplicasPerGroup:   req.Quotas.MaxReplicasPerGroup,
		MaxTotalReplicas:      req.Quotas.MaxTotalReplicas,
		MaxCollections:        req.Quotas.MaxCollections,
		MaxKeys:               req.Quotas.MaxKeys,
		MaxStorageBytes:       req.Quotas.MaxStorageBytes,
		MaxDatabaseSizeBytes:  req.Quotas.MaxDatabaseSizeBytes,
		MaxUsers:              req.Quotas.MaxUsers,
		MaxRoles:              req.Quotas.MaxRoles,
		MaxCertificates:       req.Quotas.MaxCertificates,
		MaxBackups:            req.Quotas.MaxBackups,
		MaxVectorDims:         req.Quotas.MaxVectorDims,
		MaxTSMetrics:          req.Quotas.MaxTSMetrics,
		SystemBarrierQuota:    req.Quotas.SystemBarrierQuota,
		MaxDedicatedWALGroups: req.Quotas.MaxDedicatedWALGroups,
	}

	info, err := t.tenantSvc.UpdateQuotas(ctx, req.TenantID, q)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateTenantQuotasResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// UpdateTenantEntitlements updates the feature entitlements for a tenant by
// delegating to the TenantServicer. A tenant's entitlements are its plan,
// set by the platform operator only (AuthMiddleware/ScopeCross),
// exactly as tenants:set_entitlements is ScopeCross on every network
// transport: a tenant Admin cannot grant itself entitlements.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	ctx, err := t.admit(ctx, "UpdateTenantEntitlements")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantsSetEntitlements); err != nil {
		return nil, err
	}

	e := &quota.Entitlements{
		AllowedIsolationModes:     req.Entitlements.AllowedIsolationModes,
		AllowDedicatedRaft:        req.Entitlements.AllowDedicatedRaft,
		AllowVolumeStorage:        req.Entitlements.AllowVolumeStorage,
		AllowSystemManagedBarrier: req.Entitlements.AllowSystemManagedBarrier,
		AllowedBarrierModes:       req.Entitlements.AllowedBarrierModes,
		AllowDedicatedWAL:         req.Entitlements.AllowDedicatedWAL,
		AllowedVolumePools:        req.Entitlements.AllowedVolumePools,
	}

	info, err := t.tenantSvc.UpdateEntitlements(ctx, req.TenantID, e)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateTenantEntitlementsResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// DeleteTenant deletes a tenant by delegating to the TenantServicer.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	ctx, err := t.admit(ctx, "DeleteTenant")
	if err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantRecord(ctx, tenantID, permission.TenantsDelete); err != nil {
		return err
	}

	return t.tenantSvc.DeleteTenant(ctx, tenantID)
}

// TenantBarrierInit initializes a per-tenant barrier by delegating to the
// TenantServicer using the strategy field from the request. When Strategy is
// empty the passphrase (software) strategy is used. For the Shamir strategy
// the response carries the generated M-of-N shares; for none/passphrase the
// share fields are empty.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierInit")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantBarrierInit(ctx, req.TenantID); err != nil {
		return nil, err
	}

	// The owner's signed statement lets every replica of a clustered tenant
	// initialize its own barrier key; without a Signer there is none.
	return tenantinit.Run(req, func(round tenantinit.Round) (*transport.TenantBarrierInitResponse, error) {
		var roster *tenant.TenantBarrierShamirRoster
		if len(req.Officers) > 0 && !round.Complete {
			roster = &tenant.TenantBarrierShamirRoster{
				Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
				Policy:   seal.FactorPolicy(req.Policy),
			}
		}
		result, err := t.tenantSvc.TenantBarrierInitWithStrategy(ctx, req.TenantID, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy), roster,
			tenant.WithInitStatement(round.Statement),
			tenant.WithPreparedShamir(round.Roster, round.UnsealKeyCheck))
		if err != nil {
			return nil, err
		}
		if result == nil {
			return &transport.TenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}, nil
		}
		return &transport.TenantBarrierInitResponse{
			TenantID:       result.TenantID,
			Strategy:       result.Strategy,
			ShareCount:     result.ShareCount,
			Threshold:      result.Threshold,
			TotalShares:    result.TotalShares,
			Holders:        holderwire.TenantBarrierHoldersTo(result.Holders),
			Roster:         result.Roster,
			UnsealKeyCheck: result.UnsealKeyCheck,
			AdminPending:   result.AdminPending,
		}, nil
	})
}

// TenantBarrierSignedInitBegin returns a tenant barrier's init statement to
// sign away from the node and the single-use reference that redeems it.
func (t *Transport) TenantBarrierSignedInitBegin(ctx context.Context, req *transport.TenantBarrierSignedInitBeginRequest) (*transport.TenantBarrierSignedInitBeginResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierSignedInitBegin")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierSignedInitBegin", Err: &transport.NilRequestError{}}
	}
	if err := t.authorizeTenantBarrierInit(ctx, req.TenantID); err != nil {
		return nil, err
	}
	var roster *tenant.TenantBarrierShamirRoster
	if len(req.Officers) > 0 {
		roster = &tenant.TenantBarrierShamirRoster{
			Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
			Policy:   seal.FactorPolicy(req.Policy),
		}
	}
	begun, err := t.tenantSvc.TenantBarrierInitBegin(ctx, &tenant.TenantBarrierInitBeginRequest{
		TenantID:        req.TenantID,
		Strategy:        seal.StrategyID(req.Strategy),
		KeyRef:          req.KeyRef,
		Threshold:       req.Threshold,
		Shares:          req.Shares,
		Roster:          roster,
		SignerPublicKey: req.PublicKey,
	})
	if err != nil {
		return nil, err
	}
	return &transport.TenantBarrierSignedInitBeginResponse{
		TenantID:    begun.TenantID,
		Strategy:    begun.Strategy,
		Reference:   begun.Reference,
		Statement:   begun.Statement,
		ExpiresAt:   begun.ExpiresAt,
		Threshold:   begun.Threshold,
		TotalShares: begun.TotalShares,
		ShareCount:  begun.ShareCount,
		Holders:     holderwire.TenantBarrierHoldersTo(begun.Holders),
	}, nil
}

// TenantBarrierSignedInitComplete initializes a tenant barrier under the
// begun statement and the signature over it.
func (t *Transport) TenantBarrierSignedInitComplete(ctx context.Context, req *transport.TenantBarrierSignedInitCompleteRequest) (*transport.TenantBarrierInitResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierSignedInitComplete")
	if err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierSignedInitComplete", Err: &transport.NilRequestError{}}
	}
	if err := t.authorizeTenantBarrierInit(ctx, req.TenantID); err != nil {
		return nil, err
	}
	// The caller's passphrase is borrowed, as for TenantBarrierInit: a copy,
	// not the caller's bytes, moves into protected memory and is wiped.
	passphrase, err := tenant.NewSignedInitPassphrase(bytes.Clone(req.Passphrase))
	if err != nil {
		return nil, err
	}
	defer passphrase.Destroy()
	result, err := t.tenantSvc.TenantBarrierInitComplete(ctx, &tenant.TenantBarrierInitCompleteRequest{
		TenantID:   req.TenantID,
		Reference:  req.Reference,
		Signature:  req.Signature,
		Passphrase: passphrase,
	})
	if err != nil {
		return nil, err
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:       result.TenantID,
		Strategy:       result.Strategy,
		ShareCount:     result.ShareCount,
		Threshold:      result.Threshold,
		TotalShares:    result.TotalShares,
		Holders:        holderwire.TenantBarrierHoldersTo(result.Holders),
		Roster:         result.Roster,
		UnsealKeyCheck: result.UnsealKeyCheck,
		AdminPending:   result.AdminPending,
	}, nil
}

// TenantBarrierUnseal unseals a per-tenant barrier by delegating to
// the TenantServicer.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	ctx, err := t.admit(ctx, "TenantBarrierUnseal")
	if err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantBarrierSealedOp(ctx, req.TenantID, permission.TenantBarrierUnseal); err != nil {
		return err
	}

	return t.tenantSvc.TenantBarrierUnseal(ctx, req.TenantID, req.Key)
}

// tenantService returns the injected TenantServicer, or the typed refusal
// when the transport is closed or none was injected.
func (t *Transport) tenantService() (tenant.TenantServicer, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	return t.tenantSvc, nil
}

// TenantBarrierOfficerRegister enrols a share holder of a tenant barrier.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierOfficerRegister")
	if err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantBarrierOfficerRegister); err != nil {
		return nil, err
	}
	reg := service.HolderRegistration(ctx, holderwire.TenantRegisterFrom(req))
	result, err := svc.TenantBarrierOfficerRegister(ctx, req.TenantID, &reg)
	if err != nil {
		return nil, err
	}
	return holderwire.TenantRegisterTo(result), nil
}

// TenantBarrierOfficerList lists a tenant barrier's holders.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	ctx, err := t.admit(ctx, "TenantBarrierOfficerList")
	if err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.authorizeTenantTarget(ctx, tenantID, permission.TenantBarrierOfficerList); err != nil {
		return nil, err
	}
	officers, err := svc.TenantBarrierOfficerList(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return holderwire.StatusesTo(officers), nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder, who
// must present its certificate on ctx. IdentityGated: an authenticated
// caller is required (requireIdentity), rate limited and audited, exactly
// as permission.IdentityGated is on every network transport; the holder's
// BINDING authorization is the certificate or in-process claim claimContext
// resolves, which requireIdentity does not replace.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierShareClaimBegin")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.limitHolderAct(ctx, "TenantBarrierShareClaimBegin", tenantScopeKey(req.TenantID)); err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	challenge, err := svc.TenantBarrierShareClaimBegin(ctx, req.TenantID, req.Username, thumbprint)
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{NonceHex: hex.EncodeToString(challenge.Nonce), ExpiresAt: challenge.ExpiresAt}, nil
}

// TenantBarrierShareClaimComplete delivers a tenant holder's sealed envelope
// exactly once.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	ctx, err := t.admit(ctx, "TenantBarrierShareClaimComplete")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.limitHolderAct(ctx, "TenantBarrierShareClaimComplete", tenantScopeKey(req.TenantID)); err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	result, err := svc.TenantBarrierShareClaimComplete(ctx, req.TenantID, req.Username, req.NonceHex, req.Signature, thumbprint)
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
// IdentityGated: no RBAC PERMISSION gates a quorum challenge -- a holder's
// BINDING authorization is the factor it presents, not an RBAC role -- but
// an authenticated caller is required (requireIdentity), rate limited and
// audited, exactly as permission.IdentityGated is on every network
// transport. ctx carries no claimContext marking: that marker exists for
// the share-CLAIM handshake's certificate-binding fallback, which the
// quorum handshake never reads, so marking ctx for it was a no-op.
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	ctx, err := t.admit(ctx, "TenantBarrierQuorumBegin")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	challenge, err := svc.TenantBarrierQuorumBegin(ctx, req.TenantID, seal.QuorumOp(req.Op), req.Username, req.Factor)
	if err != nil {
		return nil, err
	}
	return holderwire.ChallengeTo(challenge), nil
}

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's
// share. IdentityGated -- see TenantBarrierQuorumBegin's doc comment.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	ctx, err := t.admit(ctx, "TenantBarrierQuorumSubmit")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	out, err := svc.TenantBarrierQuorumSubmit(ctx, req.TenantID, holderwire.TenantSubmitFrom(req, quorumThumbprint(ctx)))
	if err != nil {
		return nil, err
	}
	return holderwire.OutcomeTo(service.QuorumOutcomeFrom(out)), nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	ctx, err := t.admit(ctx, "TenantBarrierRekeyStart")
	if err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantBarrierRekey); err != nil {
		return nil, err
	}
	round, err := svc.TenantBarrierRekeyStart(ctx, req.TenantID, req.Threshold, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, err
	}
	return holderwire.RoundTo(round), nil
}

// TenantBarrierOfficerRevoke revokes a tenant holder through a quorum-gated
// rekey.
func (t *Transport) TenantBarrierOfficerRevoke(ctx context.Context, req *transport.TenantOfficerRevokeRequest) (*transport.QuorumRound, error) {
	ctx, err := t.admit(ctx, "TenantBarrierOfficerRevoke")
	if err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantBarrierOfficerRevoke); err != nil {
		return nil, err
	}
	round, err := svc.TenantBarrierOfficerRevoke(ctx, req.TenantID, req.Username, req.Threshold)
	if err != nil {
		return nil, err
	}
	return holderwire.RoundTo(round), nil
}
