// Copyright 2026 Jeremy Hahn
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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// authorizeTenantBarrierSignedInit authorizes either half of a two-phase
// signed init as a plain init is authorized -- RBAC, or the tenant's
// trust-on-first-init window -- without the connection-bound proof: the
// signed statement, anchored to the tenant's first admin, is the proof of
// possession.
func (s *Server) authorizeTenantBarrierSignedInit(ctx context.Context, tenantID string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, s.tenantSvc)
}

// handleTenantBarrierSignedInitBegin handles tenant.BarrierSignedInitBegin:
// it returns the init statement the tenant's first admin signs and the
// single-use reference tenant.BarrierSignedInitComplete redeems it with.
func (s *Server) handleTenantBarrierSignedInitBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}
	var req transport.TenantBarrierSignedInitBeginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}
	if err := s.authorizeTenantBarrierSignedInit(ctx, req.TenantID); err != nil {
		return nil, err
	}
	var roster *tenant.TenantBarrierShamirRoster
	if len(req.Officers) > 0 {
		roster = &tenant.TenantBarrierShamirRoster{
			Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
			Policy:   seal.FactorPolicy(req.Policy),
		}
	}
	begun, err := s.tenantSvc.TenantBarrierInitBegin(ctx, &tenant.TenantBarrierInitBeginRequest{
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
	out, err := json.Marshal(transport.TenantBarrierSignedInitBeginResponse{
		TenantID:    begun.TenantID,
		Strategy:    begun.Strategy,
		Reference:   begun.Reference,
		Statement:   begun.Statement,
		ExpiresAt:   begun.ExpiresAt,
		Threshold:   begun.Threshold,
		TotalShares: begun.TotalShares,
		ShareCount:  begun.ShareCount,
		Holders:     holderwire.TenantBarrierHoldersTo(begun.Holders),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTenantBarrierSignedInitComplete handles
// tenant.BarrierSignedInitComplete: it redeems begin's reference with the
// detached signature over its statement and initializes the barrier under
// the signed statement.
func (s *Server) handleTenantBarrierSignedInitComplete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}
	var req transport.TenantBarrierSignedInitCompleteRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	// The decoded credential is this handler's to own and to destroy, as in
	// handleTenantBarrierInit.
	defer secure.Wipe(req.Passphrase)
	if req.TenantID == "" {
		return nil, &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}
	if err := s.authorizeTenantBarrierSignedInit(ctx, req.TenantID); err != nil {
		return nil, err
	}
	result, err := s.tenantSvc.TenantBarrierInitComplete(ctx, &tenant.TenantBarrierInitCompleteRequest{
		TenantID:   req.TenantID,
		Reference:  req.Reference,
		Signature:  req.Signature,
		Passphrase: req.Passphrase,
	})
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(quicTenantBarrierInitResponse{
		TenantID:       result.TenantID,
		Strategy:       result.Strategy,
		Threshold:      result.Threshold,
		TotalShares:    result.TotalShares,
		ShareCount:     result.ShareCount,
		Holders:        holderwire.TenantBarrierHoldersTo(result.Holders),
		Roster:         result.Roster,
		UnsealKeyCheck: result.UnsealKeyCheck,
		AdminPending:   result.AdminPending,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
