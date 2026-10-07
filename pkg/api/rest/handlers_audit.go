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
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// auditQueryTimeout bounds audit scans which may span large time ranges.
const auditQueryTimeout = 30 * time.Second

// auditQueryResponse is the JSON payload returned by GET /v1/audit/events.
type auditQueryResponse struct {
	Events []audit.AuditEvent `json:"events"`
	Next   string             `json:"next,omitempty"`
	Total  int                `json:"total"`
}

// handleAuditQuery handles GET /v1/audit/events.
//
// Query parameters:
//
//	from      RFC3339 start time (inclusive)
//	to        RFC3339 end time (exclusive)
//	actor     filter by actor identity
//	resource  filter by resource
//	action    filter by action
//	outcome   filter by outcome prefix ("success" or "failure")
//	tenant_id  the caller's own tenant; any other is refused with 403
//	limit      max events per page (default 100, max 1000)
//	cursor     opaque pagination cursor from previous response
//	replicated "true" reads the cluster-wide replicated chain instead of the
//	           node-local trail (docs/audit/architecture.md#cluster-wide-replicated-trail).
//	           404-class when the caller's tenant is not entitled to it.
//
// Authorization: requires "audit:query" (permission.AuditQuery), and the query
// is confined to the caller's own tenant (tenant.ConfineAuditFilter) whichever
// backend answers it.
func (s *Server) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	replicated := q.Get("replicated") == "true"

	// s.replicatedAudit is a concrete *service.OptInAuditLogger: assigning a
	// nil one into the audit.AuditLogger interface variable below would
	// produce a non-nil interface wrapping a nil pointer, so the nil check
	// happens on the concrete type FIRST, before it is ever boxed.
	var logger audit.AuditLogger
	switch {
	case replicated && s.replicatedAudit == nil:
		http.Error(w, `{"error":"replicated audit not configured"}`, http.StatusNotImplemented)
		return
	case replicated:
		logger = s.replicatedAudit
	case s.auditLogger == nil:
		http.Error(w, `{"error":"audit not configured"}`, http.StatusNotImplemented)
		return
	default:
		logger = s.auditLogger
	}
	f := audit.Filter{
		Actor:    q.Get("actor"),
		Resource: q.Get("resource"),
		Action:   q.Get("action"),
		Outcome:  q.Get("outcome"),
		TenantID: q.Get("tenant_id"),
		Cursor:   q.Get("cursor"),
	}

	if v := q.Get("from"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, `{"error":"invalid from timestamp"}`, http.StatusBadRequest)
			return
		}
		f.From = ts
	}
	if v := q.Get("to"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, `{"error":"invalid to timestamp"}`, http.StatusBadRequest)
			return
		}
		f.To = ts
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, `{"error":"invalid limit"}`, http.StatusBadRequest)
			return
		}
		f.Limit = n
	}

	f, err := tenant.ConfineAuditFilter(r.Context(), f)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), auditQueryTimeout)
	defer cancel()

	page, err := logger.Query(ctx, f)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := auditQueryResponse{
		Events: page.Events,
		Next:   page.Next,
		Total:  page.Total,
	}
	if resp.Events == nil {
		resp.Events = []audit.AuditEvent{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Default().Error("audit: encode response failed", "error", err)
	}
}

// handleAuditVerify handles GET /v1/audit/verify.
//
// Verifies the audit log's tamper-evidence hash chain across the active
// file and every rotation archive. A 200 response with "verified": false
// is a TAMPER VERDICT, not a request failure -- exactly as
// POST /v1/backups/{id}/verify reports a checksum mismatch as a result
// field rather than an HTTP error. Only a genuine inability to PERFORM the
// verification (unsupported backend, missing file, a rotation race) maps to
// a non-2xx status; see pkg/audit.Verify.
//
// Authorization: requires "audit:verify" permission.
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	if s.auditLogger == nil {
		http.Error(w, `{"error":"audit not configured"}`, http.StatusNotImplemented)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), auditQueryTimeout)
	defer cancel()

	report, err := audit.Verify(ctx, s.auditLogger)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, report)
}

// handleAuditVerifyReplicated handles GET /v1/audit/replicated/verify.
//
// Verifies the hash-chain continuity of the cluster-wide replicated audit
// chain for the caller's own tenant (tenant.Confine; a tenant_id naming
// another tenant is refused with 403, exactly as the query endpoint). A 200
// response with "verified": false is a TAMPER VERDICT; a tenant whose
// replicated-audit entitlement is off is refused rather than answering a
// trivially-true report for a chain that was never written to.
//
// Authorization: requires "audit:verify" permission.
func (s *Server) handleAuditVerifyReplicated(w http.ResponseWriter, r *http.Request) {
	if s.replicatedAudit == nil {
		http.Error(w, `{"error":"replicated audit not configured"}`, http.StatusNotImplemented)
		return
	}

	tenantID, err := tenant.Confine(r.Context(), r.URL.Query().Get("tenant_id"), "verify replicated audit chain")
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), auditQueryTimeout)
	defer cancel()

	report, err := s.replicatedAudit.VerifyTenantChain(ctx, tenantID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, report)
}
