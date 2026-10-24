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
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ABAC policy routes. The body of a write and of every read is the policy
// document's own JSON (pkg/auth/abac.Policy), so spec.enabled is present
// exactly when the stored document sets it. Every route acts on the
// caller's own tenant; a tenant_id query parameter, or a document's
// metadata.tenant, naming another is refused by service.PolicyServicer.

// policyListResponse is the body of GET /v1/abac/policies.
type policyListResponse struct {
	Policies []*abac.Policy `json:"policies"`
}

// handleCreatePolicy handles POST /v1/abac/policies.
func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	var p abac.Policy
	if !decodeIAMRequest(w, r, &p) {
		return
	}
	stored, err := s.policySvc.CreatePolicy(r.Context(), &p)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(stored)
}

// handleListPolicies handles GET /v1/abac/policies.
func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := s.policySvc.ListPolicies(r.Context(), r.URL.Query().Get("tenant_id"))
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, policyListResponse{Policies: policies})
}

// handleGetPolicy handles GET /v1/abac/policies/{name}.
func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.policySvc.GetPolicy(r.Context(), r.URL.Query().Get("tenant_id"), r.PathValue("name"))
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, p)
}

// handleUpdatePolicy handles PUT /v1/abac/policies/{name}. The path names
// the policy: a body without metadata.name takes it, and a body naming a
// different policy is refused rather than writing that one.
func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var p abac.Policy
	if !decodeIAMRequest(w, r, &p) {
		return
	}
	name := r.PathValue("name")
	if p.Metadata.Name == "" {
		p.Metadata.Name = name
	}
	if p.Metadata.Name != name {
		writePolicyError(w, &service.PolicyInvalidError{Name: p.Metadata.Name, Reason: "metadata.name does not match the policy named by the path"})
		return
	}
	stored, err := s.policySvc.UpdatePolicy(r.Context(), &p)
	if err != nil {
		writePolicyError(w, err)
		return
	}
	writeJSON(w, stored)
}

// handleDeletePolicy handles DELETE /v1/abac/policies/{name}.
func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.policySvc.DeletePolicy(r.Context(), r.URL.Query().Get("tenant_id"), r.PathValue("name")); err != nil {
		writePolicyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writePolicyError renders a policy service error with its classified
// status.
func writePolicyError(w http.ResponseWriter, err error) {
	writeErrorJSON(w, mapHTTPError(err), err)
}
