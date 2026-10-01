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
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// ---------------------------------------------------------------------------
// Auth factor request / response types
// ---------------------------------------------------------------------------

// enrollFactorRequest is the JSON payload for POST /v1/authfactors/{factor}/enroll.
type enrollFactorRequest struct {
	UserID       string         `json:"user_id"`
	TenantID     string         `json:"tenant_id,omitempty"`
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// enrollFactorResponse is the JSON payload returned by POST /v1/authfactors/{factor}/enroll.
type enrollFactorResponse struct {
	EnrollmentID string            `json:"enrollment_id"`
	FactorName   string            `json:"factor_name"`
	UserID       string            `json:"user_id"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	EnrolledAt   string            `json:"enrolled_at"`
}

// challengeFactorResponse is the JSON payload returned by POST /v1/authfactors/{enrollment_id}/challenge.
type challengeFactorResponse struct {
	ChallengeID  string         `json:"challenge_id"`
	EnrollmentID string         `json:"enrollment_id"`
	Nonce        []byte         `json:"nonce"`
	ExpiresAt    string         `json:"expires_at"`
	MethodData   map[string]any `json:"method_data,omitempty"`
}

// verifyFactorRequest is the JSON payload for POST /v1/authfactors/{enrollment_id}/verify.
type verifyFactorRequest struct {
	// FactorName optionally asserts the factor the challenge's enrollment
	// was made with. It never chooses the factor: a non-empty value that
	// differs from the enrollment's is refused (400, go-qrdb#577), exactly
	// as the other five transports refuse it.
	FactorName  string            `json:"factor_name,omitempty"`
	ChallengeID string            `json:"challenge_id"`
	Value       []byte            `json:"value"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// listFactorsResponse is the JSON payload returned by GET /v1/authfactors/list.
type listFactorsResponse struct {
	Enrollments []*enrollmentEntry `json:"enrollments"`
	Total       int                `json:"total"`
}

// enrollmentEntry is the JSON representation of a single enrollment.
type enrollmentEntry struct {
	EnrollmentID string            `json:"enrollment_id"`
	FactorName   string            `json:"factor_name"`
	UserID       string            `json:"user_id"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	EnrolledAt   string            `json:"enrolled_at"`
	RevokedAt    string            `json:"revoked_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Auth factor handlers
// ---------------------------------------------------------------------------

// handleAuthFactorEnroll handles POST /v1/authfactors/{factor}/enroll.
//
// TODO: apply permission.AuthFactorEnroll once Phase 2 ships.
func (s *Server) handleAuthFactorEnroll(w http.ResponseWriter, r *http.Request) {
	if s.authFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "authfactor"})
		return
	}

	factor := extractAuthFactorName(r.URL.Path, "/enroll")
	if factor == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "factor"})
		return
	}

	var req enrollFactorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.UserID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "user_id"})
		return
	}

	params := authfactors.EnrollParams{
		UserID:       req.UserID,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	}
	if params.MethodParams == nil {
		params.MethodParams = make(map[string]any)
	}

	enrollment, err := s.authFactorSvc.EnrollFactor(r.Context(), factor, req.UserID, params)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(enrollmentToResponse(enrollment))
}

// handleAuthFactorChallenge handles POST /v1/authfactors/{enrollment_id}/challenge.
//
// TODO: apply permission.AuthFactorChallenge once Phase 2 ships.
func (s *Server) handleAuthFactorChallenge(w http.ResponseWriter, r *http.Request) {
	if s.authFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "authfactor"})
		return
	}

	enrollmentID := extractAuthFactorName(r.URL.Path, "/challenge")
	if enrollmentID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "enrollment_id"})
		return
	}

	challenge, err := s.authFactorSvc.ChallengeFactor(r.Context(), enrollmentID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, challengeFactorResponse{
		ChallengeID:  challenge.ChallengeID,
		EnrollmentID: challenge.EnrollmentID,
		Nonce:        challenge.Nonce,
		ExpiresAt:    challenge.ExpiresAt.Format(time.RFC3339),
		MethodData:   challenge.MethodData,
	})
}

// handleAuthFactorVerify handles POST /v1/authfactors/{enrollment_id}/verify.
//
// TODO: apply permission.AuthFactorVerify once Phase 2 ships.
func (s *Server) handleAuthFactorVerify(w http.ResponseWriter, r *http.Request) {
	if s.authFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "authfactor"})
		return
	}

	enrollmentID := extractAuthFactorName(r.URL.Path, "/verify")
	if enrollmentID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "enrollment_id"})
		return
	}

	var req verifyFactorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.ChallengeID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "challenge_id"})
		return
	}

	resp := &authfactors.Response{
		Value:    req.Value,
		Metadata: req.Metadata,
	}
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]string)
	}

	if err := s.authFactorSvc.VerifyFactor(r.Context(), req.FactorName, req.ChallengeID, resp); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "verified"})
}

// handleAuthFactorList handles GET /v1/authfactors/list.
//
// Query params:
//   - subject (optional) filter by user ID
//
// TODO: apply permission.AuthFactorList once Phase 2 ships.
func (s *Server) handleAuthFactorList(w http.ResponseWriter, r *http.Request) {
	if s.authFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "authfactor"})
		return
	}

	subject := r.URL.Query().Get("subject")

	enrollments, err := s.authFactorSvc.ListFactors(r.Context(), subject)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	entries := make([]*enrollmentEntry, len(enrollments))
	for i, e := range enrollments {
		entries[i] = enrollmentToEntry(e)
	}

	writeJSON(w, listFactorsResponse{
		Enrollments: entries,
		Total:       len(entries),
	})
}

// handleAuthFactorRevoke handles DELETE /v1/authfactors/{enrollment_id}.
//
// TODO: apply permission.AuthFactorRevoke once Phase 2 ships.
func (s *Server) handleAuthFactorRevoke(w http.ResponseWriter, r *http.Request) {
	if s.authFactorSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "authfactor"})
		return
	}

	// Path: /v1/authfactors/{id} — strip prefix.
	id := strings.TrimPrefix(r.URL.Path, "/v1/authfactors/")
	if id == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "enrollment_id"})
		return
	}

	if err := s.authFactorSvc.RevokeFactor(r.Context(), id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractAuthFactorName parses the factor name or enrollment ID from a
// /v1/authfactors/{name}/suffix path.
// suffix is e.g. "/enroll", "/challenge", "/verify".
func extractAuthFactorName(path string, suffix string) string {
	// Strip /v1/authfactors/ prefix.
	rest := strings.TrimPrefix(path, "/v1/authfactors/")
	// Strip suffix.
	name, found := strings.CutSuffix(rest, suffix)
	if !found {
		return ""
	}
	return name
}

// enrollmentToResponse converts an Enrollment to the REST wire shape.
func enrollmentToResponse(e *authfactors.Enrollment) *enrollFactorResponse {
	return &enrollFactorResponse{
		EnrollmentID: e.EnrollmentID,
		FactorName:   e.FactorName,
		UserID:       e.UserID,
		TenantID:     e.TenantID,
		Metadata:     e.Metadata,
		EnrolledAt:   e.EnrolledAt.Format(time.RFC3339),
	}
}

// enrollmentToEntry converts an Enrollment to the list-entry wire shape.
func enrollmentToEntry(e *authfactors.Enrollment) *enrollmentEntry {
	entry := &enrollmentEntry{
		EnrollmentID: e.EnrollmentID,
		FactorName:   e.FactorName,
		UserID:       e.UserID,
		TenantID:     e.TenantID,
		Metadata:     e.Metadata,
		EnrolledAt:   e.EnrolledAt.Format(time.RFC3339),
	}
	if !e.RevokedAt.IsZero() {
		entry.RevokedAt = e.RevokedAt.Format(time.RFC3339)
	}
	return entry
}

// handleAuthFactorByPath dispatches auth factor requests that carry a path
// parameter (/v1/authfactors/{factor_or_id}/{action} or DELETE /v1/authfactors/{id}).
func (s *Server) handleAuthFactorByPath(w http.ResponseWriter, r *http.Request) {
	// Path suffix after /v1/authfactors/
	rest := strings.TrimPrefix(r.URL.Path, "/v1/authfactors/")

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/enroll"):
		s.handleAuthFactorEnroll(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/challenge"):
		s.handleAuthFactorChallenge(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(rest, "/verify"):
		s.handleAuthFactorVerify(w, r)
	case r.Method == http.MethodDelete:
		s.handleAuthFactorRevoke(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
