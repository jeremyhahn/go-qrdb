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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mapTenantInfo converts a tenant.TenantInfo to a transport.TenantInfo.
func mapTenantInfo(info *tenant.TenantInfo) transport.TenantInfo {
	ti := transport.TenantInfo{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: transport.TenantIsolationType(info.EffectiveIsolationType()),
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
		MaxDatabases:         q.MaxDatabases,
		MaxRaftGroups:        q.MaxRaftGroups,
		MaxReplicasPerGroup:  q.MaxReplicasPerGroup,
		MaxTotalReplicas:     q.MaxTotalReplicas,
		MaxCollections:       q.MaxCollections,
		MaxKeys:              q.MaxKeys,
		MaxStorageBytes:      q.MaxStorageBytes,
		MaxDatabaseSizeBytes: q.MaxDatabaseSizeBytes,
		MaxUsers:             q.MaxUsers,
		MaxRoles:             q.MaxRoles,
		MaxCertificates:      q.MaxCertificates,
		MaxBackups:           q.MaxBackups,
		MaxVectorDims:        q.MaxVectorDims,
		MaxTSMetrics:         q.MaxTSMetrics,
	}
}

// mapEntitlements converts a quota.Entitlements to a transport.TenantEntitlements pointer.
func mapEntitlements(e *quota.Entitlements) *transport.TenantEntitlements {
	return &transport.TenantEntitlements{
		AllowedIsolationModes:     e.AllowedIsolationModes,
		AllowDedicatedRaft:        e.AllowDedicatedRaft,
		AllowVolumeStorage:        e.AllowVolumeStorage,
		AllowSystemManagedBarrier: e.AllowSystemManagedBarrier,
	}
}

// WithTenantService injects a TenantServicer into the transport.
func WithTenantService(svc tenant.TenantServicer) Option {
	return func(t *Transport) {
		t.tenantSvc = svc
	}
}

// CreateTenant creates a new tenant by delegating to the TenantServicer.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	info, err := t.tenantSvc.CreateTenant(ctx, req.ID, req.Name, tenant.IsolationType(req.IsolationType),
		tenant.WithBarrierAutoUnseal(req.BarrierAutoUnseal))
	if err != nil {
		return nil, err
	}

	return &transport.CreateTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// GetTenant retrieves a tenant by ID by delegating to the TenantServicer.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
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

// UpdateTenant applies a partial update to an existing tenant by delegating
// to the TenantServicer. Non-empty patch fields replace the stored value.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
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
// delegating to the TenantServicer.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	q := &quota.Quotas{
		MaxDatabases:         req.Quotas.MaxDatabases,
		MaxRaftGroups:        req.Quotas.MaxRaftGroups,
		MaxReplicasPerGroup:  req.Quotas.MaxReplicasPerGroup,
		MaxTotalReplicas:     req.Quotas.MaxTotalReplicas,
		MaxCollections:       req.Quotas.MaxCollections,
		MaxKeys:              req.Quotas.MaxKeys,
		MaxStorageBytes:      req.Quotas.MaxStorageBytes,
		MaxDatabaseSizeBytes: req.Quotas.MaxDatabaseSizeBytes,
		MaxUsers:             req.Quotas.MaxUsers,
		MaxRoles:             req.Quotas.MaxRoles,
		MaxCertificates:      req.Quotas.MaxCertificates,
		MaxBackups:           req.Quotas.MaxBackups,
		MaxVectorDims:        req.Quotas.MaxVectorDims,
		MaxTSMetrics:         req.Quotas.MaxTSMetrics,
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
// delegating to the TenantServicer.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	e := &quota.Entitlements{
		AllowedIsolationModes:     req.Entitlements.AllowedIsolationModes,
		AllowDedicatedRaft:        req.Entitlements.AllowDedicatedRaft,
		AllowVolumeStorage:        req.Entitlements.AllowVolumeStorage,
		AllowSystemManagedBarrier: req.Entitlements.AllowSystemManagedBarrier,
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
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}

	// Prefer share over key for unseal material.
	key := req.Key
	if len(req.Share) > 0 {
		key = req.Share
	}

	return t.tenantSvc.TenantBarrierUnseal(ctx, req.TenantID, key)
}

// TenantBarrierUnsealShare submits a single Shamir share toward a per-tenant
// barrier's quorum threshold by delegating to the TenantServicer.
func (t *Transport) TenantBarrierUnsealShare(ctx context.Context, req *transport.TenantBarrierUnsealShareRequest) (*transport.TenantBarrierUnsealProgress, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	progress, err := t.tenantSvc.TenantBarrierUnsealShare(ctx, req.TenantID, req.Share)
	if err != nil {
		return nil, err
	}
	if progress == nil {
		return &transport.TenantBarrierUnsealProgress{TenantID: req.TenantID}, nil
	}
	return &transport.TenantBarrierUnsealProgress{
		TenantID:  progress.TenantID,
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
		Sealed:    progress.Sealed,
	}, nil
}

// TenantBarrierOfficerRegister registers a Security Officer for the named
// tenant's barrier by delegating to the injected TenantServicer.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	result, err := t.tenantSvc.TenantBarrierOfficerRegister(ctx, req.TenantID, &ceremony.RegisterOfficerRequest{
		Username:          req.Username,
		CSRPEM:            req.CSRPEM,
		AuthenticatorType: ceremony.AuthenticatorType(req.AuthenticatorType),
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

// TenantBarrierOfficerList returns all enrolled Security Officers for the
// named tenant's barrier by delegating to the injected TenantServicer.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	officers, err := t.tenantSvc.TenantBarrierOfficerList(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return embeddedCeremonyOfficerStatusesToTransport(officers), nil
}

// TenantBarrierOfficerClaimStatus returns a per-officer claim status snapshot
// for the named tenant's barrier by delegating to the injected TenantServicer.
func (t *Transport) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	officers, err := t.tenantSvc.TenantBarrierOfficerClaimStatus(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return embeddedCeremonyOfficerStatusesToTransport(officers), nil
}

// TenantBarrierShareClaimBegin issues a challenge nonce to the named Security
// Officer for the tenant barrier share claim handshake by delegating to the
// injected TenantServicer.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	challenge, err := t.tenantSvc.TenantBarrierShareClaimBegin(ctx, req.TenantID, req.Username, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  hex.EncodeToString(challenge.Nonce),
		ExpiresAt: challenge.ExpiresAt,
	}, nil
}

// TenantBarrierShareClaimComplete verifies the Security Officer's nonce
// signature and returns the tenant barrier share bytes exactly once by
// delegating to the injected TenantServicer.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	result, err := t.tenantSvc.TenantBarrierShareClaimComplete(ctx, req.TenantID, req.Username, req.NonceHex, req.Signature, "")
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: result.ShareIndex,
		ShareBytes: result.ShareBytes,
	}, nil
}

// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for the
// named tenant's barrier by delegating to the injected TenantServicer.
func (t *Transport) TenantBarrierShamirSealVote(ctx context.Context, req *transport.TenantShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	progress, err := t.tenantSvc.TenantBarrierShamirSealVote(ctx, req.TenantID, req.Share)
	if err != nil {
		return nil, err
	}
	return &transport.ShamirSealVoteResponse{
		Required:  progress.Required,
		Submitted: progress.Submitted,
		Complete:  progress.Complete,
	}, nil
}

// embeddedCeremonyOfficerStatusesToTransport converts ceremony layer officer
// status records to the transport layer representation.
func embeddedCeremonyOfficerStatusesToTransport(officers []*ceremony.OfficerStatus) []*transport.OfficerStatus {
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
