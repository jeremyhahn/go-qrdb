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

package rest

import (
	"context"
	"net/http"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Shamir custody of a database barrier (docs/databases/api.md). The routes
// live under /v1/databases/{name}/barrier/; the permission each needs is
// enforced by the route registry, and the database service confines every
// call to a database in the caller's tenant. Claims and quorum submissions
// are identity-gated: the holder's certificate or factor proof is the
// credential.

// databaseHolderRoutes maps a database sub-path and method to its handler.
// officers/{username}/claim/{begin,complete} carry the username and are
// matched separately.
var databaseHolderRoutes = map[string]func(s *Server, w http.ResponseWriter, r *http.Request, name string){
	http.MethodPost + " barrier/officers":          (*Server).handleDatabaseBarrierOfficerRegister,
	http.MethodGet + " barrier/officers":           (*Server).handleDatabaseBarrierOfficerList,
	http.MethodPost + " barrier/shamir/initialize": (*Server).handleDatabaseBarrierShamirInitialize,
	http.MethodPost + " barrier/quorum/begin":      (*Server).handleDatabaseBarrierQuorumBegin,
	http.MethodPost + " barrier/quorum/submit":     (*Server).handleDatabaseBarrierQuorumSubmit,
	http.MethodPost + " barrier/rekey/start":       (*Server).handleDatabaseBarrierRekeyStart,
	http.MethodPost + " barrier/officers/revoke":   (*Server).handleDatabaseBarrierOfficerRevoke,
}

// dispatchDatabaseHolder serves a database share-holder sub-path, reporting
// whether it was one.
func (s *Server) dispatchDatabaseHolder(w http.ResponseWriter, r *http.Request, name, subPath string) bool {
	if h, ok := databaseHolderRoutes[r.Method+" "+subPath]; ok {
		h(s, w, r, name)
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	rest, ok := strings.CutPrefix(subPath, "barrier/officers/")
	if !ok {
		return false
	}
	username, step, ok := strings.Cut(rest, "/claim/")
	if !ok || username == "" || strings.Contains(username, "/") {
		return false
	}
	switch step {
	case "begin":
		s.handleDatabaseBarrierShareClaimBegin(w, r, name, username)
		return true
	case "complete":
		s.handleDatabaseBarrierShareClaimComplete(w, r, name, username)
		return true
	}
	return false
}

// databaseHolderCall runs call under the request timeout against the
// database service's holder custody, decoding the request body into body
// first (nil for a bodyless route), and renders the result or error.
func (s *Server) databaseHolderCall(w http.ResponseWriter, r *http.Request, body any, call func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error)) {
	hs, ok := s.databaseSvc.(service.DatabaseHolderServicer)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "database share holders"})
		return
	}
	if body != nil && !decodeHolderJSON(w, r, body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	out, err := call(ctx, hs)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, out)
}

// handleDatabaseBarrierOfficerRegister handles
// POST /v1/databases/{name}/barrier/officers -- enrols a share holder.
// Required permission: databases:officer_register.
func (s *Server) handleDatabaseBarrierOfficerRegister(w http.ResponseWriter, r *http.Request, name string) {
	var req transport.OfficerRegisterRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		result, err := hs.DatabaseBarrierOfficerRegister(ctx, name, holderwire.RegisterFrom(&req))
		if err != nil {
			return nil, err
		}
		return holderwire.RegisterTo(result), nil
	})
}

// handleDatabaseBarrierOfficerList handles
// GET /v1/databases/{name}/barrier/officers -- lists the holders.
// Required permission: databases:officer_list.
func (s *Server) handleDatabaseBarrierOfficerList(w http.ResponseWriter, r *http.Request, name string) {
	s.databaseHolderCall(w, r, nil, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		officers, err := hs.DatabaseBarrierOfficerList(ctx, name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"officers": holderwire.StatusesTo(officers)}, nil
	})
}

// databaseShamirInitializeRequest is the body of the custody initialization:
// the factor policy. The M-of-N is the one the database was created with.
type databaseShamirInitializeRequest struct {
	Policy string `json:"policy,omitempty"`
}

// handleDatabaseBarrierShamirInitialize handles
// POST /v1/databases/{name}/barrier/shamir/initialize.
// Required permission: databases:initialize_shamir.
func (s *Server) handleDatabaseBarrierShamirInitialize(w http.ResponseWriter, r *http.Request, name string) {
	var req databaseShamirInitializeRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		info, err := hs.DatabaseBarrierShamirInitialize(ctx, name, seal.FactorPolicy(req.Policy))
		if err != nil {
			return nil, err
		}
		return transport.BarrierInitializeShamirResponse{Threshold: info.Threshold, TotalShares: info.TotalShares, ShareCount: info.ShareCount}, nil
	})
}

// handleDatabaseBarrierShareClaimBegin handles
// POST /v1/databases/{name}/barrier/officers/{username}/claim/begin.
// Identity-gated: the holder's enrolled certificate is the credential.
func (s *Server) handleDatabaseBarrierShareClaimBegin(w http.ResponseWriter, r *http.Request, name, username string) {
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	s.databaseHolderCall(w, r, nil, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, name, username, thumbprint)
		if err != nil {
			return nil, err
		}
		return transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt}, nil
	})
}

// handleDatabaseBarrierShareClaimComplete handles
// POST /v1/databases/{name}/barrier/officers/{username}/claim/complete --
// delivers the holder's envelope exactly once.
// Identity-gated: the holder's enrolled certificate is the credential.
func (s *Server) handleDatabaseBarrierShareClaimComplete(w http.ResponseWriter, r *http.Request, name, username string) {
	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	var req transport.ShareClaimCompleteRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		result, err := hs.DatabaseBarrierShareClaimComplete(ctx, name, username, req.NonceHex, req.Signature, thumbprint)
		if err != nil {
			return nil, err
		}
		return transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
	})
}

// handleDatabaseBarrierQuorumBegin handles
// POST /v1/databases/{name}/barrier/quorum/begin.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleDatabaseBarrierQuorumBegin(w http.ResponseWriter, r *http.Request, name string) {
	var req transport.QuorumBeginRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		challenge, err := hs.DatabaseBarrierQuorumBegin(ctx, name, holderwire.BeginFrom(&req))
		if err != nil {
			return nil, err
		}
		return holderwire.ChallengeTo(challenge), nil
	})
}

// handleDatabaseBarrierQuorumSubmit handles
// POST /v1/databases/{name}/barrier/quorum/submit.
// Identity-gated: the holder's factor proof is the credential.
func (s *Server) handleDatabaseBarrierQuorumSubmit(w http.ResponseWriter, r *http.Request, name string) {
	var req transport.QuorumSubmitRequest
	// The share is decoded inside databaseHolderCall, so the wipe must read
	// req.Share when it runs, not when it is deferred.
	defer func() { secure.Wipe(req.Share) }()
	peer := restOptionalThumbprint(r)
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		out, err := hs.DatabaseBarrierQuorumSubmit(ctx, name, holderwire.SubmitFrom(&req, peer))
		if err != nil {
			return nil, err
		}
		return holderwire.OutcomeTo(out), nil
	})
}

// databaseRekeyStartRequest is the body of a database rekey.
type databaseRekeyStartRequest struct {
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
}

// handleDatabaseBarrierRekeyStart handles
// POST /v1/databases/{name}/barrier/rekey/start.
// Required permission: databases:rekey.
func (s *Server) handleDatabaseBarrierRekeyStart(w http.ResponseWriter, r *http.Request, name string) {
	var req databaseRekeyStartRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		round, err := hs.DatabaseBarrierRekeyStart(ctx, name, req.Threshold, seal.FactorPolicy(req.Policy))
		if err != nil {
			return nil, err
		}
		return holderwire.RoundTo(round), nil
	})
}

// databaseOfficerRevokeRequest is the body of a database holder revocation.
type databaseOfficerRevokeRequest struct {
	Username  string `json:"username"`
	Threshold int    `json:"threshold,omitempty"`
}

// handleDatabaseBarrierOfficerRevoke handles
// POST /v1/databases/{name}/barrier/officers/revoke.
// Required permission: databases:officer_revoke.
func (s *Server) handleDatabaseBarrierOfficerRevoke(w http.ResponseWriter, r *http.Request, name string) {
	var req databaseOfficerRevokeRequest
	s.databaseHolderCall(w, r, &req, func(ctx context.Context, hs service.DatabaseHolderServicer) (any, error) {
		round, err := hs.DatabaseBarrierOfficerRevoke(ctx, name, req.Username, req.Threshold)
		if err != nil {
			return nil, err
		}
		return holderwire.RoundTo(round), nil
	})
}
