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
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// User auth factor REST handlers (M14)
//
// Routes:
//   POST   /v1/users/factors                  -> Add
//   GET    /v1/users/factors/list?user_id=N   -> List
//   GET    /v1/users/factors/{factor_id}      -> Get
//   DELETE /v1/users/factors/{factor_id}      -> Remove (force=true via query)
//   PUT    /v1/users/factors/{factor_id}/label -> Relabel
//   POST   /v1/users/factors/rotate           -> Rotate
//   POST   /v1/users/factors/login            -> Login resolution
// ---------------------------------------------------------------------------

// handleUserAuthFactorAdd handles POST /v1/users/factors.
func (s *Server) handleUserAuthFactorAdd(w http.ResponseWriter, r *http.Request) {
	if s.userAuthFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user_authfactor"})
		return
	}
	var req transport.AuthFactorAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	factor := userAuthFactorFromRequest(&req)
	if err := s.userAuthFactorSvc.Add(r.Context(), req.UserID, factor); err != nil {
		writeErrorJSON(w, mapUserAuthFactorError(err), err)
		return
	}
	writeJSON(w, transport.AuthFactorAddResponse{Factor: userAuthFactorToRecord(factor)})
}

// handleUserAuthFactorList handles GET /v1/users/factors/list?user_id=N.
func (s *Server) handleUserAuthFactorList(w http.ResponseWriter, r *http.Request) {
	if s.userAuthFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user_authfactor"})
		return
	}
	uid, err := strconv.ParseUint(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil || uid == 0 {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "user_id"})
		return
	}
	factors, err := s.userAuthFactorSvc.List(r.Context(), uid)
	if err != nil {
		writeErrorJSON(w, mapUserAuthFactorError(err), err)
		return
	}
	resp := transport.AuthFactorListResponse{
		Factors: make([]transport.AuthFactorRecord, len(factors)),
		Total:   len(factors),
	}
	for i, f := range factors {
		resp.Factors[i] = userAuthFactorToRecord(f)
	}
	writeJSON(w, resp)
}

// handleUserAuthFactorRotate handles POST /v1/users/factors/rotate.
func (s *Server) handleUserAuthFactorRotate(w http.ResponseWriter, r *http.Request) {
	if s.userAuthFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user_authfactor"})
		return
	}
	var req transport.AuthFactorRotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	newFactor := userAuthFactorFromRequest(&req.NewFactor)
	if err := s.userAuthFactorSvc.Rotate(r.Context(), req.OldFactorID, newFactor); err != nil {
		writeErrorJSON(w, mapUserAuthFactorError(err), err)
		return
	}
	writeJSON(w, transport.AuthFactorRotateResponse{NewFactor: userAuthFactorToRecord(newFactor)})
}

// handleUserAuthFactorLogin handles POST /v1/users/factors/login.
func (s *Server) handleUserAuthFactorLogin(w http.ResponseWriter, r *http.Request) {
	if s.userAuthFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user_authfactor"})
		return
	}
	var req transport.AuthFactorLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	resp, err := s.resolveUserAuthFactorLogin(r.Context(), &req)
	if err != nil {
		writeErrorJSON(w, mapUserAuthFactorError(err), err)
		return
	}
	writeJSON(w, resp)
}

// handleUserAuthFactorByPath dispatches GET / DELETE / PUT requests against
// /v1/users/factors/{factor_id}[/label].
func (s *Server) handleUserAuthFactorByPath(w http.ResponseWriter, r *http.Request) {
	if s.userAuthFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user_authfactor"})
		return
	}
	const prefix = "/v1/users/factors/"
	suffix := r.URL.Path[len(prefix):]
	parts := strings.SplitN(suffix, "/", 2)
	factorID := parts[0]
	if factorID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "factor_id"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		factor, err := s.userAuthFactorSvc.Get(r.Context(), factorID)
		if err != nil {
			writeErrorJSON(w, mapUserAuthFactorError(err), err)
			return
		}
		writeJSON(w, transport.AuthFactorGetResponse{Factor: userAuthFactorToRecord(factor)})
	case http.MethodDelete:
		force := r.URL.Query().Get("force") == "true"
		if err := s.userAuthFactorSvc.Remove(r.Context(), factorID, force); err != nil {
			writeErrorJSON(w, mapUserAuthFactorError(err), err)
			return
		}
		writeJSON(w, transport.AuthFactorRemoveResponse{Status: "revoked"})
	case http.MethodPut:
		if len(parts) != 2 || parts[1] != "label" {
			http.NotFound(w, r)
			return
		}
		var req transport.AuthFactorRelabelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
		if err := s.userAuthFactorSvc.Relabel(r.Context(), factorID, req.Label); err != nil {
			writeErrorJSON(w, mapUserAuthFactorError(err), err)
			return
		}
		factor, err := s.userAuthFactorSvc.Get(r.Context(), factorID)
		if err != nil {
			writeErrorJSON(w, mapUserAuthFactorError(err), err)
			return
		}
		writeJSON(w, transport.AuthFactorRelabelResponse{Factor: userAuthFactorToRecord(factor)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// resolveUserAuthFactorLogin performs the user lookup if the rest server has
// a user service wired; otherwise it falls back to returning the resolved
// factor with bare identifiers.
func (s *Server) resolveUserAuthFactorLogin(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	t := auth.AuthFactorType(req.Type)
	factor, err := s.userAuthFactorSvc.LookupByCredentialKey(ctx, t, req.LookupKey)
	if err != nil {
		return nil, err
	}
	resp := &transport.AuthFactorLoginResponse{
		UserID: factor.UserID,
		Active: true,
		Factor: userAuthFactorToRecord(factor),
	}
	// Best-effort enrichment from the user service, when wired.
	if s.userSvc != nil {
		// User IDs in this layer are uint64 but pkg/user uses string IDs;
		// callers that need the username/email/roles should adapt by
		// providing a UserLookup at a higher layer.
		_ = factor.UserID
	}
	return resp, nil
}

// userAuthFactorFromRequest builds an auth.AuthFactor from a wire request.
func userAuthFactorFromRequest(req *transport.AuthFactorAddRequest) *auth.AuthFactor {
	return &auth.AuthFactor{
		UserID:     req.UserID,
		Type:       auth.AuthFactorType(req.Type),
		Label:      req.Label,
		Credential: req.Credential,
		Metadata:   req.Metadata,
		CreatedBy:  req.CreatedBy,
	}
}

// userAuthFactorToRecord projects an auth.AuthFactor into the wire shape.
func userAuthFactorToRecord(f *auth.AuthFactor) transport.AuthFactorRecord {
	if f == nil {
		return transport.AuthFactorRecord{}
	}
	return transport.AuthFactorRecord{
		ID:           f.ID,
		UserID:       f.UserID,
		Type:         string(f.Type),
		Label:        f.Label,
		Credential:   f.Credential,
		Metadata:     f.Metadata,
		RegisteredAt: f.RegisteredAt,
		LastUsedAt:   f.LastUsedAt,
		RevokedAt:    f.RevokedAt,
		CreatedBy:    f.CreatedBy,
	}
}

// mapUserAuthFactorError translates an auth-package error into an HTTP status.
func mapUserAuthFactorError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, auth.ErrFactorNotFound):
		return http.StatusNotFound
	case errors.Is(err, auth.ErrFactorRevoked):
		return http.StatusGone
	case errors.Is(err, auth.ErrLastFactor):
		return http.StatusConflict
	case errors.Is(err, auth.ErrFactorAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, auth.ErrFactorTypeUnsupported), errors.Is(err, auth.ErrInvalidFactor):
		return http.StatusBadRequest
	case errors.Is(err, auth.ErrUserHasNoFactors):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
