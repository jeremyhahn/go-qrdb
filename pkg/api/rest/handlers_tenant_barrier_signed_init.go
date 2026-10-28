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

package rest

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// handleTenantBarrierSignedInitBegin handles
// POST /v1/tenants/{id}/barrier/signed-init/begin: it returns the init
// statement the tenant's first admin signs and the single-use reference
// signed-init/complete redeems it with.
func (s *Server) handleTenantBarrierSignedInitBegin(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantBarrierSignedInitBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx := r.Context()
	if err := s.authorizeTenantBarrierSignedInit(ctx, id); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	var roster *tenant.TenantBarrierShamirRoster
	if len(req.Officers) > 0 {
		roster = &tenant.TenantBarrierShamirRoster{
			Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
			Policy:   seal.FactorPolicy(req.Policy),
		}
	}
	begun, err := s.tenantSvc.TenantBarrierInitBegin(ctx, &tenant.TenantBarrierInitBeginRequest{
		TenantID:        id,
		Strategy:        seal.StrategyID(req.Strategy),
		KeyRef:          req.KeyRef,
		Threshold:       req.Threshold,
		Shares:          req.Shares,
		Roster:          roster,
		SignerPublicKey: req.PublicKey,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, transport.TenantBarrierSignedInitBeginResponse{
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
}

// handleTenantBarrierSignedInitComplete handles
// POST /v1/tenants/{id}/barrier/signed-init/complete: it redeems begin's
// reference with the detached signature over its statement and initializes
// the barrier under the signed statement.
func (s *Server) handleTenantBarrierSignedInitComplete(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantBarrierSignedInitCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	// The decoded credential is this handler's to own and to destroy, as in
	// handleTenantBarrierInit: it moves into protected memory at once.
	passphrase, err := tenant.NewSignedInitPassphrase(req.Passphrase)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	defer passphrase.Destroy()
	ctx := r.Context()
	if err := s.authorizeTenantBarrierSignedInit(ctx, id); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	result, err := s.tenantSvc.TenantBarrierInitComplete(ctx, &tenant.TenantBarrierInitCompleteRequest{
		TenantID:   id,
		Reference:  req.Reference,
		Signature:  req.Signature,
		Passphrase: passphrase,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	// Unlike init/complete, the body is written for every strategy: it is
	// the only place the caller learns the strategy the statement named.
	writeJSON(w, tenantBarrierInitBody(result))
}
