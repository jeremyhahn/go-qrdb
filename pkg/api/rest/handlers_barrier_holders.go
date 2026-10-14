// Copyright (c) 2026 Jeremy Hahn
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
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// restCertThumbprint returns the verified TLS client certificate's
// thumbprint, or service.ClientCertRequiredError when the connection
// carries none. The share claim requires it.
func restCertThumbprint(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", &service.ClientCertRequiredError{}
	}
	return mtls.Thumbprint(r.TLS.PeerCertificates[0].Raw), nil
}

// restOptionalThumbprint returns the verified TLS client certificate's
// thumbprint, or "" when none was presented. A quorum submission does not
// require mTLS — the factor proof authenticates the holder — but when a
// certificate is present the mtls factor binds to it.
func restOptionalThumbprint(r *http.Request) string {
	thumb, err := restCertThumbprint(r)
	if err != nil {
		return ""
	}
	return thumb
}

// decodeHolderJSON decodes the request body into v, answering 400 on failure.
func decodeHolderJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return false
	}
	return true
}

// barrierHolderGuard answers 501 when no barrier service is wired.
func (s *Server) barrierHolderGuard(w http.ResponseWriter) bool {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// System barrier share holders
// ---------------------------------------------------------------------------

// registerHolder serves POST /v1/barrier/officers (unseal domain) and
// POST /v1/barrier/recovery/officers (recovery domain).
func (s *Server) registerHolder(w http.ResponseWriter, r *http.Request, domain seal.QuorumDomain) {
	if !s.barrierHolderGuard(w) {
		return
	}
	var req transport.OfficerRegisterRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	result, err := s.barrierSvc.BarrierOfficerRegister(r.Context(), domain, holderwire.RegisterFrom(&req))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, holderwire.RegisterTo(result))
}

// handleBarrierOfficerRegister handles POST /v1/barrier/officers -- enrols a
// share holder (Security Officer or key custodian) of the unseal key.
// Required permission: seal:officer_register (Admin, SO).
func (s *Server) handleBarrierOfficerRegister(w http.ResponseWriter, r *http.Request) {
	s.registerHolder(w, r, seal.DomainUnseal)
}

// handleBarrierRecoveryOfficerRegister handles POST /v1/barrier/recovery/officers
// -- enrols a recovery-key holder.
// Required permission: seal:officer_register (Admin, SO).
func (s *Server) handleBarrierRecoveryOfficerRegister(w http.ResponseWriter, r *http.Request) {
	s.registerHolder(w, r, seal.DomainRecovery)
}

// handleBarrierOfficerList handles GET /v1/barrier/officers -- lists the
// unseal domain's holders and their claim state.
// Required permission: seal:officer_list (Admin, SO).
func (s *Server) handleBarrierOfficerList(w http.ResponseWriter, r *http.Request) {
	if !s.barrierHolderGuard(w) {
		return
	}
	officers, err := s.barrierSvc.BarrierOfficerList(r.Context(), seal.DomainUnseal)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, map[string]any{"officers": holderwire.StatusesTo(officers)})
}

// handleBarrierShareClaimBegin issues the claim nonce to a holder presenting
// its enrolled certificate:
//
//	POST /v1/barrier/officers/{username}/claim/begin
//	POST /v1/barrier/recovery/officers/{username}/claim/begin
func (s *Server) handleBarrierShareClaimBegin(w http.ResponseWriter, r *http.Request, domain seal.QuorumDomain, username string) {
	if !s.barrierHolderGuard(w) {
		return
	}
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	challenge, err := s.barrierSvc.BarrierShareClaimBegin(r.Context(), domain, username, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt})
}

// handleBarrierShareClaimComplete verifies the holder's nonce signature and
// delivers its sealed share envelope exactly once, to the holder's own
// session:
//
//	POST /v1/barrier/officers/{username}/claim/complete
//	POST /v1/barrier/recovery/officers/{username}/claim/complete
func (s *Server) handleBarrierShareClaimComplete(w http.ResponseWriter, r *http.Request, domain seal.QuorumDomain, username string) {
	if !s.barrierHolderGuard(w) {
		return
	}
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	var req transport.ShareClaimCompleteRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	result, err := s.barrierSvc.BarrierShareClaimComplete(r.Context(), domain, username, req.NonceHex, req.Signature, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope})
}

// holderClaimRoutes maps a claim action to its handler.
var holderClaimRoutes = map[string]func(*Server, http.ResponseWriter, *http.Request, seal.QuorumDomain, string){
	"begin":    (*Server).handleBarrierShareClaimBegin,
	"complete": (*Server).handleBarrierShareClaimComplete,
}

// handleBarrierOfficersByPath dispatches the path-parameter claim routes:
//
//	/v1/barrier/officers/{username}/claim/begin|complete
//	/v1/barrier/recovery/officers/{username}/claim/begin|complete
func (s *Server) handleBarrierOfficersByPath(w http.ResponseWriter, r *http.Request) {
	const officersPrefix = "/v1/barrier/officers/"
	const recoveryOfficersPrefix = "/v1/barrier/recovery/officers/"

	domain := seal.DomainUnseal
	suffix, ok := strings.CutPrefix(r.URL.Path, officersPrefix)
	if !ok {
		suffix, ok = strings.CutPrefix(r.URL.Path, recoveryOfficersPrefix)
		domain = seal.DomainRecovery
	}
	parts := strings.SplitN(suffix, "/", 3)
	if !ok || len(parts) != 3 || parts[0] == "" || parts[1] != "claim" {
		writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "username"})
		return
	}
	handler, found := holderClaimRoutes[parts[2]]
	if !found {
		writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "action"})
		return
	}
	handler(s, w, r, domain, parts[0])
}

// handleBarrierQuorumBegin handles POST /v1/barrier/quorum/begin -- issues
// the factor challenge a holder answers to submit a share. Reachable while
// the barrier is sealed.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleBarrierQuorumBegin(w http.ResponseWriter, r *http.Request) {
	if !s.barrierHolderGuard(w) {
		return
	}
	var req transport.QuorumBeginRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	challenge, err := s.barrierSvc.BarrierQuorumBegin(r.Context(), holderwire.BeginFrom(&req))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, holderwire.ChallengeTo(challenge))
}

// handleBarrierQuorumSubmit handles POST /v1/barrier/quorum/submit -- counts
// one authenticated holder's share toward the open round. The TLS peer
// certificate, when present, is taken from the connection, never from the
// request.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleBarrierQuorumSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.barrierHolderGuard(w) {
		return
	}
	var req transport.QuorumSubmitRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	defer secure.Wipe(req.Share)
	out, err := s.barrierSvc.BarrierQuorumSubmit(r.Context(), holderwire.SubmitFrom(&req, restOptionalThumbprint(r)))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, holderwire.OutcomeTo(out))
}

// handleBarrierRekeyStart handles POST /v1/barrier/rekey/start -- opens a
// rekey round under SO PIN authorization.
// Required permission: seal:rekey (SO).
func (s *Server) handleBarrierRekeyStart(w http.ResponseWriter, r *http.Request) {
	if !s.barrierHolderGuard(w) {
		return
	}
	var req transport.RekeyStartRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	round, err := s.barrierSvc.BarrierRekeyStart(r.Context(), seal.QuorumDomain(req.Domain), req.Threshold, seal.FactorPolicy(req.Policy), req.SOPin)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, holderwire.RoundTo(round))
}

// handleBarrierOfficerRevoke handles POST /v1/barrier/officers/revoke --
// revokes a holder through a quorum-gated rekey, under SO PIN authorization.
// Required permission: seal:officer_revoke (Admin, SO).
func (s *Server) handleBarrierOfficerRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.barrierHolderGuard(w) {
		return
	}
	var req transport.OfficerRevokeRequest
	if !decodeHolderJSON(w, r, &req) {
		return
	}
	round, err := s.barrierSvc.BarrierOfficerRevoke(r.Context(), seal.QuorumDomain(req.Domain), req.Username, req.Threshold, req.SOPin)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, holderwire.RoundTo(round))
}

// ---------------------------------------------------------------------------
// Tenant barrier share holders
// ---------------------------------------------------------------------------

// tenantHolderCall runs one tenant holder operation: the tenant-scoped
// authorization gate, then — only for an authorized caller — decoding the
// request body into body (nil for a bodyless route), then call, under the
// caller's deadline and the error rendering every tenant holder route shares.
// Like the system holder routes it adds no timeout of its own.
func (s *Server) tenantHolderCall(w http.ResponseWriter, r *http.Request, tenantID string, perm permission.Permission, body any, call func(ctx context.Context) (any, error)) {
	ctx := r.Context()
	if err := s.authorizeTenantBarrierOp(ctx, tenantID, perm); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	if body != nil && !decodeHolderJSON(w, r, body) {
		return
	}
	out, err := call(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, out)
}

// handleTenantBarrierOfficerRegister handles
// POST /v1/tenants/{id}/barrier/officers -- enrols a tenant share holder.
// Required permission: tenant_barrier:officer_register (ScopeTenantOnly).
func (s *Server) handleTenantBarrierOfficerRegister(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantOfficerRegisterRequest
	s.tenantHolderCall(w, r, id, permission.TenantBarrierOfficerRegister, &req, func(ctx context.Context) (any, error) {
		reg := service.HolderRegistration(ctx, holderwire.TenantRegisterFrom(&req))
		result, err := s.tenantSvc.TenantBarrierOfficerRegister(ctx, id, &reg)
		if err != nil {
			return nil, err
		}
		return holderwire.TenantRegisterTo(result), nil
	})
}

// handleTenantBarrierOfficerList handles
// GET /v1/tenants/{id}/barrier/officers -- lists a tenant barrier's holders.
// Required permission: tenant_barrier:officer_list (ScopeTenantOnly).
func (s *Server) handleTenantBarrierOfficerList(w http.ResponseWriter, r *http.Request, id string) {
	s.tenantHolderCall(w, r, id, permission.TenantBarrierOfficerList, nil, func(ctx context.Context) (any, error) {
		officers, err := s.tenantSvc.TenantBarrierOfficerList(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"officers": holderwire.StatusesTo(officers)}, nil
	})
}

// handleTenantBarrierShareClaimBegin handles
// POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin.
// Identity-gated: the holder's enrolled certificate is the credential.
func (s *Server) handleTenantBarrierShareClaimBegin(w http.ResponseWriter, r *http.Request, id, username string) {
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	s.tenantHolderCall(w, r, id, permission.IdentityGated, nil, func(ctx context.Context) (any, error) {
		challenge, err := s.tenantSvc.TenantBarrierShareClaimBegin(ctx, id, username, thumbprint)
		if err != nil {
			return nil, err
		}
		return transport.ShareClaimBeginResponse{NonceHex: hex.EncodeToString(challenge.Nonce), ExpiresAt: challenge.ExpiresAt}, nil
	})
}

// handleTenantBarrierShareClaimComplete handles
// POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete --
// delivers a tenant holder's sealed share envelope exactly once.
// Identity-gated: the holder's enrolled certificate is the credential.
func (s *Server) handleTenantBarrierShareClaimComplete(w http.ResponseWriter, r *http.Request, id, username string) {
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	var req transport.ShareClaimCompleteRequest
	s.tenantHolderCall(w, r, id, permission.IdentityGated, &req, func(ctx context.Context) (any, error) {
		result, err := s.tenantSvc.TenantBarrierShareClaimComplete(ctx, id, username, req.NonceHex, req.Signature, thumbprint)
		if err != nil {
			return nil, err
		}
		return transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
	})
}

// handleTenantBarrierQuorumBegin handles
// POST /v1/tenants/{id}/barrier/quorum/begin.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleTenantBarrierQuorumBegin(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantQuorumBeginRequest
	s.tenantHolderCall(w, r, id, permission.IdentityGated, &req, func(ctx context.Context) (any, error) {
		challenge, err := s.tenantSvc.TenantBarrierQuorumBegin(ctx, id, seal.QuorumOp(req.Op), req.Username, req.Factor)
		if err != nil {
			return nil, err
		}
		return holderwire.ChallengeTo(challenge), nil
	})
}

// handleTenantBarrierQuorumSubmit handles
// POST /v1/tenants/{id}/barrier/quorum/submit.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleTenantBarrierQuorumSubmit(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantQuorumSubmitRequest
	// The share is decoded inside tenantHolderCall, so the wipe must read
	// req.Share when it runs, not when it is deferred.
	defer func() { secure.Wipe(req.Share) }()
	peer := restOptionalThumbprint(r)
	s.tenantHolderCall(w, r, id, permission.IdentityGated, &req, func(ctx context.Context) (any, error) {
		out, err := s.tenantSvc.TenantBarrierQuorumSubmit(ctx, id, holderwire.TenantSubmitFrom(&req, peer))
		if err != nil {
			return nil, err
		}
		return holderwire.OutcomeTo(service.QuorumOutcomeFrom(out)), nil
	})
}

// handleTenantBarrierRekeyStart handles
// POST /v1/tenants/{id}/barrier/rekey/start.
// Required permission: tenant_barrier:rekey (ScopeTenantOnly).
func (s *Server) handleTenantBarrierRekeyStart(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantRekeyStartRequest
	s.tenantHolderCall(w, r, id, permission.TenantBarrierRekey, &req, func(ctx context.Context) (any, error) {
		round, err := s.tenantSvc.TenantBarrierRekeyStart(ctx, id, req.Threshold, seal.FactorPolicy(req.Policy))
		if err != nil {
			return nil, err
		}
		return holderwire.RoundTo(round), nil
	})
}

// handleTenantBarrierOfficerRevoke handles
// POST /v1/tenants/{id}/barrier/officers/revoke.
// Required permission: tenant_barrier:officer_revoke (ScopeTenantOnly).
func (s *Server) handleTenantBarrierOfficerRevoke(w http.ResponseWriter, r *http.Request, id string) {
	var req transport.TenantOfficerRevokeRequest
	s.tenantHolderCall(w, r, id, permission.TenantBarrierOfficerRevoke, &req, func(ctx context.Context) (any, error) {
		round, err := s.tenantSvc.TenantBarrierOfficerRevoke(ctx, id, req.Username, req.Threshold)
		if err != nil {
			return nil, err
		}
		return holderwire.RoundTo(round), nil
	})
}
