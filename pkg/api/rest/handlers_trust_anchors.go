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
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// addTrustAnchorsRequest is the JSON payload for
// POST /v1/tenants/{id}/trust-anchors: a PEM bundle of CA certificates.
type addTrustAnchorsRequest struct {
	CertsPEM string `json:"certs_pem"`
}

// trustAnchorResponse is one registered trust anchor.
type trustAnchorResponse struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	CertPEM     string    `json:"cert_pem"`
	AddedAt     time.Time `json:"added_at,omitzero"`
}

// trustAnchorsResponse lists a tenant's trust anchors.
type trustAnchorsResponse struct {
	Anchors []trustAnchorResponse `json:"anchors"`
}

func anchorsToResponse(anchors []trustanchor.Anchor) trustAnchorsResponse {
	out := trustAnchorsResponse{Anchors: make([]trustAnchorResponse, 0, len(anchors))}
	for _, a := range anchors {
		out.Anchors = append(out.Anchors, trustAnchorResponse{
			Fingerprint: a.Fingerprint,
			Subject:     a.Subject,
			NotBefore:   a.NotBefore,
			NotAfter:    a.NotAfter,
			CertPEM:     string(a.CertPEM),
			AddedAt:     a.AddedAt,
		})
	}
	return out
}

// handleListTrustAnchors handles GET /v1/tenants/{id}/trust-anchors.
func (s *Server) handleListTrustAnchors(w http.ResponseWriter, r *http.Request) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.authorizeCertTenant(ctx, r.PathValue("id"), permission.TrustAnchorsList); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	anchors, err := s.certEnrollment.ListTrustAnchors(ctx, r.PathValue("id"))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, anchorsToResponse(anchors))
}

// handleAddTrustAnchors handles POST /v1/tenants/{id}/trust-anchors.
func (s *Server) handleAddTrustAnchors(w http.ResponseWriter, r *http.Request) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return
	}
	var req addTrustAnchorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.authorizeCertTenant(ctx, r.PathValue("id"), permission.TrustAnchorsAdd); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	anchors, err := s.certEnrollment.AddTrustAnchors(ctx, r.PathValue("id"), []byte(req.CertsPEM))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(anchorsToResponse(anchors))
}

// handleRemoveTrustAnchor handles
// DELETE /v1/tenants/{id}/trust-anchors/{fingerprint}.
func (s *Server) handleRemoveTrustAnchor(w http.ResponseWriter, r *http.Request) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.authorizeCertTenant(ctx, r.PathValue("id"), permission.TrustAnchorsRemove); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	if err := s.certEnrollment.RemoveTrustAnchor(ctx, r.PathValue("id"), r.PathValue("fingerprint")); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
