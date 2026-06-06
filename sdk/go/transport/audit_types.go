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

package transport

import (
	"context"
	"time"
)

// AuditEvent is a compliance-grade structured audit record. It mirrors
// pkg/audit.AuditEvent for SDK transport use.
type AuditEvent struct {
	Timestamp  time.Time      `json:"timestamp"`
	Actor      string         `json:"actor"`
	TenantID   string         `json:"tenantID,omitempty"`
	Role       string         `json:"role,omitempty"`
	Resource   string         `json:"resource"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Attributes map[string]any `json:"attributes,omitempty"`
	ClientIP   string         `json:"clientIP,omitempty"`
	RequestID  string         `json:"requestID,omitempty"`
	PrevHash   string         `json:"prevHash,omitempty"`
}

// AuditFilter specifies criteria for querying audit events.
type AuditFilter struct {
	From     time.Time `json:"from,omitempty"`
	To       time.Time `json:"to,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Resource string    `json:"resource,omitempty"`
	Action   string    `json:"action,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
	TenantID string    `json:"tenantID,omitempty"`
	Limit    int       `json:"limit,omitempty"`
	Cursor   string    `json:"cursor,omitempty"`
}

// AuditPage is the response for a paginated audit query.
type AuditPage struct {
	Events []AuditEvent `json:"events"`
	Next   string       `json:"next,omitempty"`
	Total  int          `json:"total"`
}

// AuditClient is the interface for audit event retrieval over any transport.
// Authorization: requires "audit:query" permission.
// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
type AuditClient interface {
	// QueryAudit returns a paginated page of audit events matching the filter.
	QueryAudit(ctx context.Context, f AuditFilter) (*AuditPage, error)
}
