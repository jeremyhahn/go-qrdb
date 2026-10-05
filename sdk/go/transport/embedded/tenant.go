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
	"encoding/hex"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mapTenantInfo converts a tenant.TenantInfo to a transport.TenantInfo.
func mapTenantInfo(info *tenant.TenantInfo) transport.TenantInfo {
	ti := transport.TenantInfo{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: transport.TenantIsolationType(info.EffectiveIsolationType()),
		DataRoot:      info.DataRoot,
		CreatedAt:     info.CreatedAt,
		UpdatedAt:     info.UpdatedAt,
	}
	if info.Quotas != nil {
		ti.Quotas = mapQuotas(info.Quotas)
	}
	if info.Entitlements != nil {
		ti.Entitlements = mapEntitlements(info.Entitlements)
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
	}
}

// WithTenantService injects a TenantServicer into the transport.
func WithTenantService(svc tenant.TenantServicer) Option {
	return func(t *Transport) {
		t.tenantSvc = svc
	}
}

// CreateTenant creates a new tenant by delegating to the TenantServicer.
// Minting a tenant is platform-operator-only (tenant.RequirePlatformOperator)
// exactly as on every network transport, where tenants:create is ScopeCross.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if err := t.gate("CreateTenant"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := tenant.RequirePlatformOperator(ctx, "create a tenant"); err != nil {
		return nil, err
	}

	opts := []tenant.CreateTenantOption{tenant.WithBarrierAutoUnseal(req.BarrierAutoUnseal)}
	if req.DataRoot != "" {
		opts = append(opts, tenant.WithDataRoot(req.DataRoot))
	}

	info, err := t.tenantSvc.CreateTenant(ctx, req.ID, req.Name, tenant.IsolationType(req.IsolationType), opts...)
	if err != nil {
		return nil, err
	}

	return &transport.CreateTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// GetTenant retrieves a tenant by ID by delegating to the TenantServicer.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if err := t.gate("GetTenant"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
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
	if err := t.gate("ListTenants"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	tenants, err := t.tenantSvc.ListTenants(ctx)
	if err != nil {
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
	if err := t.gate("ListDeletedTenants"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
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
			DataRoot:      ts.DataRoot,
			CreatedAt:     ts.CreatedAt,
			DeletedAt:     ts.DeletedAt,
		})
	}
	return out, nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on this node by
// delegating to the TenantServicer.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	if err := t.gate("ReconcileDeletedTenants"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
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
// isolation tier is platform-operator-only
// (tenant.RequirePlatformOperator), exactly as AuthMiddleware.
// AuthorizeTenantUpdate re-checks tenants:set_isolation on every network
// transport.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if err := t.gate("UpdateTenant"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if req.IsolationType != "" {
		if err := tenant.RequirePlatformOperator(ctx, "set a tenant's isolation tier"); err != nil {
			return nil, err
		}
	}

	patch := tenant.TenantPatch{
		Name:          req.Name,
		IsolationType: tenant.IsolationType(req.IsolationType),
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
// the platform operator only (tenant.RequirePlatformOperator), exactly as
// tenants:set_quotas is ScopeCross on every network transport: a tenant
// Admin cannot raise its own limits.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if err := t.gate("UpdateTenantQuotas"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := tenant.RequirePlatformOperator(ctx, "set a tenant's quotas"); err != nil {
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
// set by the platform operator only (tenant.RequirePlatformOperator),
// exactly as tenants:set_entitlements is ScopeCross on every network
// transport: a tenant Admin cannot grant itself entitlements.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if err := t.gate("UpdateTenantEntitlements"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := tenant.RequirePlatformOperator(ctx, "set a tenant's entitlements"); err != nil {
		return nil, err
	}

	e := &quota.Entitlements{
		AllowedIsolationModes:     req.Entitlements.AllowedIsolationModes,
		AllowDedicatedRaft:        req.Entitlements.AllowDedicatedRaft,
		AllowVolumeStorage:        req.Entitlements.AllowVolumeStorage,
		AllowSystemManagedBarrier: req.Entitlements.AllowSystemManagedBarrier,
		AllowedBarrierModes:       req.Entitlements.AllowedBarrierModes,
		AllowDedicatedWAL:         req.Entitlements.AllowDedicatedWAL,
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
	if err := t.gate("DeleteTenant"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}

	return t.tenantSvc.DeleteTenant(ctx, tenantID)
}

// TenantBarrierInit initializes a per-tenant barrier by delegating to the
// TenantServicer using the strategy field from the request. When Strategy is
// empty the passphrase (software) strategy is used. For the Shamir strategy
// the response carries the generated M-of-N shares; for none/passphrase the
// share fields are empty.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if err := t.gate("TenantBarrierInit"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	result, err := t.tenantSvc.TenantBarrierInitWithStrategy(ctx, req.TenantID, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &transport.TenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}, nil
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:    result.TenantID,
		Strategy:    result.Strategy,
		ShareCount:  result.ShareCount,
		Threshold:   result.Threshold,
		TotalShares: result.TotalShares,
	}, nil
}

// TenantBarrierUnseal unseals a per-tenant barrier by delegating to
// the TenantServicer.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if err := t.gate("TenantBarrierUnseal"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
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
	if err := t.gate("TenantBarrierOfficerRegister"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
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
	if err := t.gate("TenantBarrierOfficerList"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	officers, err := svc.TenantBarrierOfficerList(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return holderwire.StatusesTo(officers), nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder, who
// must present its certificate on ctx.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if err := t.gate("TenantBarrierShareClaimBegin"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
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
	if err := t.gate("TenantBarrierShareClaimComplete"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
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
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if err := t.gate("TenantBarrierQuorumBegin"); err != nil {
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

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if err := t.gate("TenantBarrierQuorumSubmit"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	out, err := svc.TenantBarrierQuorumSubmit(ctx, req.TenantID, holderwire.TenantSubmitFrom(req, contextOptionalThumbprint(ctx)))
	if err != nil {
		return nil, err
	}
	return holderwire.OutcomeTo(service.QuorumOutcomeFrom(out)), nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	if err := t.gate("TenantBarrierRekeyStart"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
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
	if err := t.gate("TenantBarrierOfficerRevoke"); err != nil {
		return nil, err
	}
	svc, err := t.tenantService()
	if err != nil {
		return nil, err
	}
	round, err := svc.TenantBarrierOfficerRevoke(ctx, req.TenantID, req.Username, req.Threshold)
	if err != nil {
		return nil, err
	}
	return holderwire.RoundTo(round), nil
}
