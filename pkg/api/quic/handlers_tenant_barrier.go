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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// authorizeTenantBarrierOp authorizes a per-tenant barrier operation against
// the TRUE target tenant carried in the frame (not the caller's ambient
// tenant). The coarse registration-site permission (methodPerms registry)
// only proves the caller holds the permission at all; this fine-grained gate delegates to the tenant-scoped
// authorizer, whose ScopeTenantOnly rule denies the system tenant and any
// cross-tenant access. When no auth middleware is configured the gate is a
// no-op, matching the rest of the server's unauthenticated-mode behavior.
func (s *Server) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// beginTenantBarrierInit issues the init nonce through the shared
// trust-on-first-init rule (middleware.BeginTenantBarrierInit -- the single
// cross-transport home of the bootstrap-window semantics and the init
// proof). When authMW is nil the deployment is unauthenticated: there is no
// identity to prove, and the empty challenge says so.
func (s *Server) beginTenantBarrierInit(ctx context.Context, tenantID string) (*initproof.Challenge, error) {
	if s.authMW == nil {
		return &initproof.Challenge{}, nil
	}
	return s.authMW.BeginTenantBarrierInit(ctx, tenantID, s.tenantSvc)
}

// authorizeTenantBarrierInit authorizes a tenant barrier initialization and
// redeems its proof of possession (middleware.AuthorizeTenantBarrierInitProof).
// When authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *Server) authorizeTenantBarrierInit(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInitProof(ctx, tenantID, s.tenantSvc, proof)
}

// authorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable
// (status/unseal) via the shared sealed-window rule
// (middleware.AuthorizeTenantBarrierSealedOp). When authMW is nil the gate
// is a no-op.
func (s *Server) authorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierSealedOp(ctx, tenantID, perm)
}

// ---------------------------------------------------------------------------
// Per-tenant barrier QUIC frame handlers.
//
// Method names dispatched from buildHandlers:
//
//	tenant.BarrierInitBegin    -- issue the init nonce for this connection
//	tenant.BarrierInitComplete -- initialize the per-tenant barrier, with proof
//	tenant.BarrierUnseal  -- unseal the per-tenant barrier
//	tenant.BarrierSeal    -- seal the per-tenant barrier
//	tenant.BarrierStatus  -- query barrier sealed/initialized state
//
// Wire format follows the audit and auth factor handlers: JSON-encoded
// request/response payloads carried inside QUIC wire frames. Protobuf is
// reserved for the metadata-CRUD tenant handlers; per-tenant barrier ops
// are intentionally schema-light and do not need a generated message type.
// ---------------------------------------------------------------------------

// quicTenantBarrierInitBeginRequest is the JSON payload for
// tenant.BarrierInitBegin frames.
type quicTenantBarrierInitBeginRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantBarrierInitBeginResponse is the JSON response for
// tenant.BarrierInitBegin frames: the nonce to sign, valid only on this
// connection, and its expiry in Unix seconds. Both are empty on an
// unauthenticated deployment, which asks no proof.
type quicTenantBarrierInitBeginResponse struct {
	Nonce   []byte `json:"nonce,omitempty"`
	Expires int64  `json:"expires,omitempty"`
}

// quicTenantBarrierInitRequest is the JSON payload for
// tenant.BarrierInitComplete frames. Passphrase carries the tenant admin's caller-supplied secret (required for
// the passphrase strategy; no derived fallback). Strategy selects the seal
// strategy; when empty the passphrase (software) strategy is used.
//
// Passphrase is []byte (base64 on the wire, matching the unseal frame's key)
// so it can be zeroized; a Go string cannot be.
type quicTenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase []byte `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`

	// Officers is the inline Shamir share-holder roster to validate and
	// register atomically with initialization; see
	// transport.TenantBarrierInitRequest.Officers. Refused on any strategy
	// but shamir.
	Officers []transport.OfficerRegisterRequest `json:"officers,omitempty"`
	// Policy is the factor policy Officers enrol under ("" selects the
	// holder engine's default, "designated").
	Policy string `json:"policy,omitempty"`

	// Nonce, PublicKey and Signature are the proof of possession: begin's
	// nonce, the DER SubjectPublicKeyInfo of the first admin's key, and that
	// key's signature over the tenant, the nonce and this connection's TLS
	// exporter (pkg/initproof).
	Nonce     []byte `json:"nonce,omitempty"`
	PublicKey []byte `json:"public_key,omitempty"`
	Signature []byte `json:"signature,omitempty"`

	// InitStatement is the first admin's signed epoch-1 init statement for
	// this barrier (barrierinit.InitStatement): every other node serving the
	// barrier initializes its own key from it.
	InitStatement []byte `json:"init_statement,omitempty"`
	// Roster and UnsealKeyCheck mark a Shamir init's second request: they
	// echo the split the first request prepared (tenant.WithPreparedShamir).
	Roster         []byte `json:"roster,omitempty"`
	UnsealKeyCheck []byte `json:"unseal_key_check,omitempty"`
}

// quicTenantBarrierInitResponse is the JSON response for
// tenant.BarrierInitComplete frames. INV-1: share bytes are never included; Security Officers retrieve
// their shares via the per-tenant claim handshake. ShareCount signals how
// many officer enclaves were provisioned.
type quicTenantBarrierInitResponse struct {
	TenantID    string                              `json:"tenant_id"`
	Strategy    string                              `json:"strategy"`
	Threshold   int                                 `json:"threshold,omitempty"`
	TotalShares int                                 `json:"total_shares,omitempty"`
	ShareCount  int                                 `json:"share_count,omitempty"`
	Holders     []transport.TenantBarrierHolderCert `json:"holders,omitempty"`
	// Roster and UnsealKeyCheck are the Shamir split the owner signs
	// between a Shamir init's two requests. Empty for other strategies.
	Roster         []byte `json:"roster,omitempty"`
	UnsealKeyCheck []byte `json:"unseal_key_check,omitempty"`
	// AdminPending is transport.TenantBarrierInitResponse.AdminPending.
	AdminPending bool `json:"admin_pending,omitempty"`
}

// quicTenantBarrierUnsealRequest is the JSON payload for tenant.BarrierUnseal
// frames: the tenant barrier's credential. A Shamir tenant barrier refuses it
// and is unsealed by its holder quorum (tenant.BarrierQuorumSubmit).
type quicTenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
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
	TenantID        string `json:"tenant_id"`
	Sealed          bool   `json:"sealed"`
	Initialized     bool   `json:"initialized"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`

	ClusterSeal *clusterseal.Report `json:"cluster_seal"`

	KeyFingerprint string `json:"key_fingerprint,omitempty"`

	Nodes []tenant.BarrierNode `json:"nodes,omitempty"`

	NodesUnreadable bool `json:"nodes_unreadable,omitempty"`
}

// missingTenantIDError is returned when a tenant-barrier frame omits the
// required tenant_id field.
type missingTenantIDError struct{}

// Error returns the error string.
func (e *missingTenantIDError) Error() string {
	return "tenant: tenant_id is required"
}

// handleTenantBarrierInitBegin issues the nonce tenant.BarrierInitComplete
// must sign, bound to this connection's TLS exporter.
func (s *Server) handleTenantBarrierInitBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}
	var req quicTenantBarrierInitBeginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}
	challenge, err := s.beginTenantBarrierInit(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	resp := quicTenantBarrierInitBeginResponse{Nonce: challenge.Nonce}
	if !challenge.ExpiresAt.IsZero() {
		resp.Expires = challenge.ExpiresAt.Unix()
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierInit initializes the per-tenant barrier
// (tenant.BarrierInitComplete), given proof of possession of the first
// admin's key over this connection.
func (s *Server) handleTenantBarrierInit(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req quicTenantBarrierInitRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	// The decoded credential is this handler's to own and to destroy. The
	// tenant service borrows it for the call and retains nothing, so a
	// deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.Passphrase)
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}

	proof := &authmw.TenantBarrierInitProof{Nonce: req.Nonce, PublicKey: req.PublicKey, Signature: req.Signature}
	if err := s.authorizeTenantBarrierInit(ctx, req.TenantID, proof); err != nil {
		return nil, err
	}

	var roster *tenant.TenantBarrierShamirRoster
	if len(req.Officers) > 0 {
		roster = &tenant.TenantBarrierShamirRoster{
			Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
			Policy:   seal.FactorPolicy(req.Policy),
		}
	}
	result, err := s.tenantSvc.TenantBarrierInitWithStrategy(ctx, req.TenantID, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy), roster,
		tenant.WithInitStatement(req.InitStatement),
		tenant.WithPreparedShamir(req.Roster, req.UnsealKeyCheck))
	if err != nil {
		return nil, err
	}

	resp := quicTenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}
	if result != nil {
		resp = quicTenantBarrierInitResponse{
			TenantID:       result.TenantID,
			Strategy:       result.Strategy,
			Threshold:      result.Threshold,
			TotalShares:    result.TotalShares,
			ShareCount:     result.ShareCount,
			Holders:        holderwire.TenantBarrierHoldersTo(result.Holders),
			Roster:         result.Roster,
			UnsealKeyCheck: result.UnsealKeyCheck,
			AdminPending:   result.AdminPending,
		}
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

	if err := s.authorizeTenantBarrierSealedOp(ctx, req.TenantID, permission.TenantBarrierUnseal); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.TenantBarrierUnseal(ctx, req.TenantID, req.Key); err != nil {
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

	if err := s.authorizeTenantBarrierSealedOp(ctx, req.TenantID, permission.TenantBarrierStatus); err != nil {
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
		ClusterSeal:     st.ClusterSeal,
		KeyFingerprint:  st.KeyFingerprint,
		Nodes:           st.Nodes,
		NodesUnreadable: st.NodesUnreadable,
	}
	if resp.ClusterSeal == nil {
		resp.ClusterSeal = &clusterseal.Report{}
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
