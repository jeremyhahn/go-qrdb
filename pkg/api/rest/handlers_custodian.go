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
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// ---------------------------------------------------------------------------
// Local service interfaces
// ---------------------------------------------------------------------------

// custodianServicer is the narrow interface the REST handlers need from the
// custodian package. Satisfied by *custodian.service (via custodian.Servicer).
type custodianServicer interface {
	RegisterCustodian(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error)
	GetCustodian(ctx context.Context, id string) (*custodian.Custodian, error)
	ListCustodians(ctx context.Context, tenantID string) ([]*custodian.Custodian, error)
	RevokeCustodian(ctx context.Context, id string) error
}

// soPINVerifier is the narrow interface the REST handlers need from the SO
// authenticator. Satisfied by *auth.SOAuthenticator.
type soPINVerifier interface {
	VerifySOPIN(ctx context.Context, tenantID string, presentedPIN []byte) error
}

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

// enrollCustodianRequest is the JSON payload for POST /v1/ceremony/enroll-custodian.
type enrollCustodianRequest struct {
	Username     string `json:"username"`
	TenantID     string `json:"tenantID"`
	Method       string `json:"method"`
	EnrollmentID string `json:"enrollmentID"`
	PublicKeyPEM string `json:"publicKeyPEM"`
	ShareIndex   int    `json:"shareIndex"`
	// ShareBytes is base64-encoded raw Shamir share material.
	// It is zeroed by the service after sealing.
	ShareBytes []byte `json:"shareBytes"`
}

// revokeCustodianRequest is the JSON payload for POST /v1/ceremony/revoke-custodian.
type revokeCustodianRequest struct {
	CustodianID string `json:"custodianID"`
}

// custodianClaimShareBeginRequest is the JSON payload for
// POST /v1/ceremony/claim-share/begin.
type custodianClaimShareBeginRequest struct {
	CustodianID string `json:"custodianID"`
}

// custodianClaimShareCompleteRequest is the JSON payload for
// POST /v1/ceremony/claim-share/complete.
type custodianClaimShareCompleteRequest struct {
	CustodianID    string `json:"custodianID"`
	ChallengeID    string `json:"challengeID"`
	FactorResponse []byte `json:"factorResponse"`
}

// unsealShareRequest is the JSON payload for POST /v1/barrier/unseal-share.
type unsealShareRequest struct {
	CustodianID    string `json:"custodianID"`
	ChallengeID    string `json:"challengeID"`
	FactorResponse []byte `json:"factorResponse"`
	// ShareBytes is the plaintext Shamir share decrypted client-side.
	// Zeroed by the service after accumulation.
	ShareBytes []byte `json:"shareBytes"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleEnrollCustodian handles POST /v1/ceremony/enroll-custodian.
// Required permission: ceremony:enroll_custodian (SO only).
// Enrolls a custodian and returns the sealed share envelope.
func (s *Server) handleEnrollCustodian(w http.ResponseWriter, r *http.Request) {
	if s.custodianSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "custodian"})
		return
	}

	var req enrollCustodianRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.Username == "" || req.TenantID == "" || req.EnrollmentID == "" || req.PublicKeyPEM == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "required fields missing"})
		return
	}

	resp, err := s.custodianSvc.RegisterCustodian(r.Context(), custodian.RegisterRequest{
		Username:     req.Username,
		TenantID:     req.TenantID,
		Method:       custodian.FactorMethod(req.Method),
		EnrollmentID: req.EnrollmentID,
		PublicKeyPEM: req.PublicKeyPEM,
		ShareIndex:   req.ShareIndex,
		ShareBytes:   req.ShareBytes,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"custodian":      resp.Custodian,
		"sealedEnvelope": resp.SealedEnvelope,
	})
}

// handleRevokeCustodian handles POST /v1/ceremony/revoke-custodian.
// Required permission: ceremony:revoke_custodian (SO only).
func (s *Server) handleRevokeCustodian(w http.ResponseWriter, r *http.Request) {
	if s.custodianSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "custodian"})
		return
	}

	var req revokeCustodianRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.CustodianID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "custodianID"})
		return
	}

	if err := s.custodianSvc.RevokeCustodian(r.Context(), req.CustodianID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]string{"status": "revoked"})
}

// handleCustodianClaimShareBegin handles POST /v1/ceremony/claim-share/begin.
// Required permission: ceremony:claim_share_begin (enrolled Custodian).
// Returns a challenge nonce for the custodian to sign with their factor.
func (s *Server) handleCustodianClaimShareBegin(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req custodianClaimShareBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.CustodianID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "custodianID"})
		return
	}

	challenge, err := s.ceremonySvc.CeremonyClaimShareBegin(r.Context(), req.CustodianID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, challenge)
}

// handleCustodianClaimShareComplete handles POST /v1/ceremony/claim-share/complete.
// Required permission: ceremony:claim_share_complete (enrolled Custodian).
// Verifies the factor response and returns the custodian metadata confirming delivery.
func (s *Server) handleCustodianClaimShareComplete(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req custodianClaimShareCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.CustodianID == "" || req.ChallengeID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "custodianID or challengeID"})
		return
	}

	c, err := s.ceremonySvc.CeremonyClaimShareComplete(r.Context(), req.CustodianID, req.ChallengeID, req.FactorResponse)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, c)
}

// handleUnsealShare handles POST /v1/barrier/unseal-share.
// Required permission: barrier:unseal-share (enrolled Custodian).
// Accepts a plaintext Shamir share and factor proof; accumulates toward quorum.
func (s *Server) handleUnsealShare(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req unsealShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.CustodianID == "" || len(req.ShareBytes) == 0 {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "custodianID or shareBytes"})
		return
	}

	result, err := s.ceremonySvc.CeremonySubmitUnsealShare(r.Context(), req.CustodianID, req.ChallengeID, req.FactorResponse, req.ShareBytes)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, result)
}
