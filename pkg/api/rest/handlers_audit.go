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
//	tenant_id the caller's own tenant; any other is refused with 403
//	limit     max events per page (default 100, max 1000)
//	cursor    opaque pagination cursor from previous response
//
// Authorization: requires "audit:query" (permission.AuditQuery), and the query
// is confined to the caller's own tenant (tenant.ConfineAuditFilter).
func (s *Server) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	if s.auditLogger == nil {
		http.Error(w, `{"error":"audit not configured"}`, http.StatusNotImplemented)
		return
	}

	q := r.URL.Query()
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

	page, err := s.auditLogger.Query(ctx, f)
	if err != nil {
		http.Error(w, `{"error":"audit query failed"}`, http.StatusInternalServerError)
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
