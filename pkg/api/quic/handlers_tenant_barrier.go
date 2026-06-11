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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

// quicTenantBarrierInitResponse is the JSON response for tenant.BarrierInit
// frames. INV-1: share bytes are never included; Security Officers retrieve
// their shares via the per-tenant claim handshake. ShareCount signals how
// many officer enclaves were provisioned.
type quicTenantBarrierInitResponse struct {
	TenantID    string `json:"tenant_id"`
	Strategy    string `json:"strategy"`
	Threshold   int    `json:"threshold,omitempty"`
	TotalShares int    `json:"total_shares,omitempty"`
	ShareCount  int    `json:"share_count,omitempty"`
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

// quicTenantBarrierUnsealShareRequest is the JSON payload for
// tenant.BarrierUnsealShare frames. Share carries a single Shamir share toward
// the per-tenant quorum threshold.
type quicTenantBarrierUnsealShareRequest struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}

// quicTenantBarrierUnsealProgress is the JSON response for
// tenant.BarrierUnsealShare frames. Mirrors tenant.TenantBarrierUnsealProgress.
type quicTenantBarrierUnsealProgress struct {
	TenantID  string `json:"tenant_id"`
	Required  int    `json:"required"`
	Submitted int    `json:"submitted"`
	Complete  bool   `json:"complete"`
	Sealed    bool   `json:"sealed"`
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
	TenantID        string `json:"tenant_id"`
	Sealed          bool   `json:"sealed"`
	Initialized     bool   `json:"initialized"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`
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

	result, err := s.tenantSvc.TenantBarrierInitWithStrategy(ctx, req.TenantID, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy))
	if err != nil {
		return nil, err
	}

	resp := quicTenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}
	if result != nil {
		resp = quicTenantBarrierInitResponse{
			TenantID:    result.TenantID,
			Strategy:    result.Strategy,
			Threshold:   result.Threshold,
			TotalShares: result.TotalShares,
			ShareCount:  result.ShareCount,
		}
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierUnsealShare submits a single Shamir share toward the
// per-tenant barrier's quorum threshold, automatically unsealing once the
// threshold is met. It returns the quorum progress.
func (s *Server) handleTenantBarrierUnsealShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierUnsealShareRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierUnseal); err != nil {
		return nil, err
	}

	prog, err := s.tenantSvc.TenantBarrierUnsealShare(ctx, req.TenantID, req.Share)
	if err != nil {
		return nil, err
	}

	resp := quicTenantBarrierUnsealProgress{
		TenantID:  prog.TenantID,
		Required:  prog.Required,
		Submitted: prog.Submitted,
		Complete:  prog.Complete,
		Sealed:    prog.Sealed,
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
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
		TenantID:        st.TenantID,
		Sealed:          st.Sealed,
		Initialized:     st.Initialized,
		Strategy:        st.Strategy,
		HardwareBacked:  st.HardwareBacked,
		ShamirThreshold: st.ShamirThreshold,
		ShamirTotal:     st.ShamirTotal,
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier Security Officer QUIC handlers
// ---------------------------------------------------------------------------

// quicTenantOfficerRegisterRequest is the JSON payload for
// tenant.BarrierOfficerRegister frames.
type quicTenantOfficerRegisterRequest struct {
	TenantID          string `json:"tenant_id"`
	Username          string `json:"username"`
	CSRPEM            string `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
}

// quicTenantOfficerRegisterResponse is the JSON response for
// tenant.BarrierOfficerRegister frames.
type quicTenantOfficerRegisterResponse struct {
	CertPEM    string `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

// quicTenantOfficerListRequest is the JSON payload for
// tenant.BarrierOfficerList and tenant.BarrierOfficerClaimStatus frames.
type quicTenantOfficerListRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantOfficerStatus is the JSON representation of a Security Officer
// enrollment record within a tenant barrier.
type quicTenantOfficerStatus struct {
	Username       string `json:"username"`
	CertThumbprint string `json:"cert_thumbprint"`
	ShareIndex     int    `json:"share_index"`
	AuthFactor     string `json:"auth_factor"`
	SharePending   bool   `json:"share_pending"`
	EnrolledAt     string `json:"enrolled_at,omitempty"`
	ClaimedAt      string `json:"claimed_at,omitempty"`
}

// quicTenantShareClaimBeginRequest is the JSON payload for
// tenant.BarrierShareClaimBegin frames.
type quicTenantShareClaimBeginRequest struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
}

// quicTenantShareClaimBeginResponse is the JSON response for
// tenant.BarrierShareClaimBegin frames.
type quicTenantShareClaimBeginResponse struct {
	NonceHex  string `json:"nonce_hex"`
	ExpiresAt int64  `json:"expires"` // unix timestamp (matches proto wire)
}

// quicTenantShareClaimCompleteRequest is the JSON payload for
// tenant.BarrierShareClaimComplete frames.
type quicTenantShareClaimCompleteRequest struct {
	TenantID  string `json:"tenant_id"`
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature string `json:"signature"` // base64-encoded
}

// quicTenantShareClaimCompleteResponse is the JSON response for
// tenant.BarrierShareClaimComplete frames. INV-11: ShareBytes appears only
// here, in the claiming officer's own session.
type quicTenantShareClaimCompleteResponse struct {
	ShareBytes string `json:"share_bytes"` // base64-encoded
}

// quicTenantSealVoteRequest is the JSON payload for
// tenant.BarrierShamirSealVote frames.
type quicTenantSealVoteRequest struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}

// quicTenantCertThumbprint extracts the mTLS peer cert thumbprint from the
// context. Returns a ClientCertRequiredError when no peer certificate is present.
func quicTenantCertThumbprint(ctx context.Context) (string, error) {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return "", &ClientCertRequiredError{}
	}
	return ceremony.CertThumbprint(tlsState.PeerCertificates[0].Raw), nil
}

// handleTenantBarrierOfficerRegister registers a Security Officer for the
// named tenant's barrier.
func (s *Server) handleTenantBarrierOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantOfficerRegisterRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierOfficerRegister); err != nil {
		return nil, err
	}

	result, err := s.tenantSvc.TenantBarrierOfficerRegister(ctx, req.TenantID, &ceremony.RegisterOfficerRequest{
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: ceremony.AuthenticatorType(req.AuthenticatorType),
	})
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(quicTenantOfficerRegisterResponse{
		CertPEM:    string(result.CertPEM),
		ShareIndex: result.ShareIndex,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierOfficerList returns all enrolled Security Officers for
// the named tenant's barrier.
func (s *Server) handleTenantBarrierOfficerList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantOfficerListRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierOfficerList); err != nil {
		return nil, err
	}

	officers, err := s.tenantSvc.TenantBarrierOfficerList(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(map[string]interface{}{
		"officers": quicTenantOfficerStatusesToResponse(officers),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierOfficerClaimStatus returns a per-officer claim status
// snapshot for the named tenant's barrier.
func (s *Server) handleTenantBarrierOfficerClaimStatus(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantOfficerListRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierOfficerList); err != nil {
		return nil, err
	}

	officers, err := s.tenantSvc.TenantBarrierOfficerClaimStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(map[string]interface{}{
		"officers": quicTenantOfficerStatusesToResponse(officers),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierShareClaimBegin issues a challenge nonce to the named
// officer for the tenant barrier claim handshake.
func (s *Server) handleTenantBarrierShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantShareClaimBeginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	// Share-claim is identity-gated: the registered officer's mTLS
	// certificate is the credential; the service enforces the per-tenant
	// registry lookup, thumbprint match, and nonce signature.
	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.IdentityGated); err != nil {
		return nil, err
	}

	thumbprint, err := quicTenantCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}

	challenge, err := s.tenantSvc.TenantBarrierShareClaimBegin(ctx, req.TenantID, req.Username, thumbprint)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(quicTenantShareClaimBeginResponse{
		NonceHex:  hex.EncodeToString(challenge.Nonce),
		ExpiresAt: challenge.ExpiresAt.Unix(),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierShareClaimComplete verifies the officer's nonce
// signature and returns the tenant barrier share bytes exactly once
// (INV-1, INV-11).
func (s *Server) handleTenantBarrierShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantShareClaimCompleteRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	// Identity-gated: see handleTenantBarrierShareClaimBegin.
	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.IdentityGated); err != nil {
		return nil, err
	}

	thumbprint, err := quicTenantCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}

	sigBytes, decErr := base64.StdEncoding.DecodeString(req.Signature)
	if decErr != nil {
		return nil, &InvalidArgumentError{Reason: "signature must be valid base64"}
	}

	result, err := s.tenantSvc.TenantBarrierShareClaimComplete(ctx, req.TenantID, req.Username, req.NonceHex, sigBytes, thumbprint)
	if err != nil {
		return nil, err
	}

	// INV-11: share_bytes appears only here, in the claiming officer's own session.
	out, err := json.Marshal(quicTenantShareClaimCompleteResponse{
		ShareBytes: base64.StdEncoding.EncodeToString(result.ShareBytes),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierShamirSealVote submits a Shamir share as a seal vote for
// the named tenant's barrier.
func (s *Server) handleTenantBarrierShamirSealVote(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantSealVoteRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	if err := s.authorizeTenantBarrierOp(ctx, req.TenantID, permission.TenantBarrierSealVote); err != nil {
		return nil, err
	}

	progress, err := s.tenantSvc.TenantBarrierShamirSealVote(ctx, req.TenantID, req.Share)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(map[string]interface{}{
		"required":  progress.Required,
		"submitted": progress.Submitted,
		"complete":  progress.Complete,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// quicTenantOfficerStatusesToResponse converts a slice of ceremony.OfficerStatus
// to JSON-serializable response objects.
func quicTenantOfficerStatusesToResponse(officers []*ceremony.OfficerStatus) []quicTenantOfficerStatus {
	out := make([]quicTenantOfficerStatus, len(officers))
	for i, o := range officers {
		s := quicTenantOfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     o.ShareIndex,
			AuthFactor:     o.AuthFactor,
			SharePending:   o.SharePending,
		}
		if !o.EnrolledAt.IsZero() {
			s.EnrolledAt = o.EnrolledAt.Format(time.RFC3339)
		}
		if !o.ClaimedAt.IsZero() {
			s.ClaimedAt = o.ClaimedAt.Format(time.RFC3339)
		}
		out[i] = s
	}
	return out
}
