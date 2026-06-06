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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// authorizeTenantBarrierOp authorizes a per-tenant barrier operation against
// the TRUE target tenant carried in the frame (not the caller's ambient
// tenant). The coarse quicPermissions mapping only proves the caller holds
// the permission at all; this fine-grained gate delegates to the tenant-scoped
// authorizer, whose ScopeTenantOnly rule denies the system tenant and any
// cross-tenant access. When no auth middleware is configured the gate is a
// no-op, matching the rest of the server's unauthenticated-mode behavior.
func (s *Server) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// ---------------------------------------------------------------------------
// Per-tenant barrier QUIC frame handlers.
//
// Method names dispatched from buildHandlers:
//
//	tenant.BarrierInit    -- initialize the per-tenant barrier
//	tenant.BarrierUnseal  -- unseal the per-tenant barrier
//	tenant.BarrierSeal    -- seal the per-tenant barrier
//	tenant.BarrierStatus  -- query barrier sealed/initialized state
//
// Wire format follows the audit and auth factor handlers: JSON-encoded
// request/response payloads carried inside QUIC wire frames. Protobuf is
// reserved for the metadata-CRUD tenant handlers; per-tenant barrier ops
// are intentionally schema-light and do not need a generated message type.
// ---------------------------------------------------------------------------

// quicTenantBarrierInitRequest is the JSON payload for tenant.BarrierInit frames.
// Passphrase carries the tenant admin's caller-supplied secret (required for
// the passphrase strategy; no derived fallback). Strategy selects the seal
// strategy; when empty the passphrase (software) strategy is used.
type quicTenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase string `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
}

// quicTenantBarrierUnsealRequest is the JSON payload for tenant.BarrierUnseal frames.
//
// Either Share or Key may be supplied; the server uses whichever is non-empty
// (Key takes precedence to mirror the seal package's unseal semantics where a
// caller-provided key short-circuits any Shamir reconstruction).
type quicTenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
	Share    []byte `json:"share,omitempty"`
	Key      []byte `json:"key,omitempty"`
}

// quicTenantBarrierSealRequest is the JSON payload for tenant.BarrierSeal frames.
type quicTenantBarrierSealRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantBarrierStatusRequest is the JSON payload for tenant.BarrierStatus frames.
type quicTenantBarrierStatusRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantBarrierStatusResponse is the JSON response for tenant.BarrierStatus
// frames. Field shape matches transport.TenantBarrierStatusResponse so the
// client transport can unmarshal directly into the SDK type.
type quicTenantBarrierStatusResponse struct {
	TenantID       string `json:"tenant_id"`
	Sealed         bool   `json:"sealed"`
	Initialized    bool   `json:"initialized"`
	Strategy       string `json:"strategy"`
	HardwareBacked bool   `json:"hardware_backed"`
}

// missingTenantIDError is returned when a tenant-barrier frame omits the
// required tenant_id field.
type missingTenantIDError struct{}

// Error returns the error string.
func (e *missingTenantIDError) Error() string {
	return "tenant: tenant_id is required"
}

// handleTenantBarrierInit initializes the per-tenant barrier. The threshold
// and shares parameters are forwarded to the tenant service for future Shamir
// integration; a zero pair selects deterministic credentials.
func (s *Server) handleTenantBarrierInit(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierInitRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierInit); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.TenantBarrierInitWithStrategy(ctx, req.TenantID, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy)); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleTenantBarrierUnseal unseals the per-tenant barrier using caller-supplied
// key material. The key material is required; the service rejects empty material
// (there is no derived fallback).
func (s *Server) handleTenantBarrierUnseal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierUnsealRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierUnseal); err != nil {
		return nil, err
	}

	// Prefer Key (raw root key) over Share (Shamir share) when both are
	// provided. This matches the SDK's request schema where Key is the
	// authoritative material for direct unseal.
	material := req.Key
	if len(material) == 0 {
		material = req.Share
	}

	if err := s.tenantSvc.TenantBarrierUnseal(ctx, req.TenantID, material); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleTenantBarrierSeal seals the per-tenant barrier, returning it to the
// sealed state.
func (s *Server) handleTenantBarrierSeal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierSealRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierSeal); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.TenantBarrierSeal(ctx, req.TenantID); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleTenantBarrierStatus returns the current sealed/initialized state and
// active strategy metadata for a per-tenant barrier.
func (s *Server) handleTenantBarrierStatus(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierStatus); err != nil {
		return nil, err
	}

	st, err := s.tenantSvc.TenantBarrierStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	resp := quicTenantBarrierStatusResponse{
		TenantID:       st.TenantID,
		Sealed:         st.Sealed,
		Initialized:    st.Initialized,
		Strategy:       st.Strategy,
		HardwareBacked: st.HardwareBacked,
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
