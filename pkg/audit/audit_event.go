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

package audit

import "time"

// AuditEvent is a compliance-grade structured audit record. Every barrier
// operation, ceremony step, RBAC decision, key operation, and tenant change
// is represented as an AuditEvent.
type AuditEvent struct {
	// Timestamp is the UTC time of the event; auto-set to time.Now() if zero.
	Timestamp time.Time `json:"timestamp"`

	// Actor is the identity performing the action (cert CN, JWT sub, API key).
	Actor string `json:"actor"`

	// TenantID is the tenant that owns the resource (empty for cluster-level ops).
	TenantID string `json:"tenantID,omitempty"`

	// Role is the actor's role at the time of the event.
	Role string `json:"role,omitempty"`

	// Resource identifies the resource being accessed (e.g. "barrier", "user:alice").
	Resource string `json:"resource"`

	// Action identifies the operation performed (e.g. "initialize", "unseal").
	Action string `json:"action"`

	// Outcome is the result: "success", "failure:<reason>", or "denied:<reason>".
	Outcome string `json:"outcome"`

	// Attributes carries optional context (share_index, algorithm, threshold, etc.).
	Attributes map[string]any `json:"attributes,omitempty"`

	// ClientIP is the source IP address of the client.
	ClientIP string `json:"clientIP,omitempty"`

	// RequestID is a trace correlation ID for cross-log correlation.
	RequestID string `json:"requestID,omitempty"`

	// PrevHash is the SHA-256 hex digest of the previous AuditEvent in the chain.
	// Format: hex-encoded SHA-256, empty for the first event.
	PrevHash string `json:"prevHash,omitempty"`
}

// Filter specifies criteria for querying audit events.
type Filter struct {
	// From and To bound the query time range (inclusive/exclusive).
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`

	// Actor filters to events by a specific actor identity.
	Actor string `json:"actor,omitempty"`

	// Resource filters to events on a specific resource.
	Resource string `json:"resource,omitempty"`

	// Action filters to a specific action.
	Action string `json:"action,omitempty"`

	// Outcome filters to "success" or "failure" (prefix match).
	Outcome string `json:"outcome,omitempty"`

	// TenantID filters to events for a specific tenant.
	TenantID string `json:"tenantID,omitempty"`

	// Limit caps the number of events returned. Default 100, max 1000.
	Limit int `json:"limit,omitempty"`

	// Cursor is an opaque pagination token from a previous Page.Next field.
	Cursor string `json:"cursor,omitempty"`
}

// Page is the response type for a Query call.
type Page struct {
	// Events contains the matching audit events in chronological order.
	Events []AuditEvent `json:"events"`

	// Next is an opaque cursor for fetching the next page. Empty when exhausted.
	Next string `json:"next,omitempty"`

	// Total is the total count of matching events, or -1 if unknown.
	Total int `json:"total"`
}

const (
	// OutcomeSuccess is the outcome for a successful operation.
	OutcomeSuccess = "success"

	// OutcomeFailure is the prefix for a failure outcome (append ":<reason>").
	OutcomeFailure = "failure"

	// OutcomeDenied is the prefix for a denied outcome (append ":<reason>").
	OutcomeDenied = "denied"

	// defaultLimit is the default page size for audit queries.
	defaultLimit = 100

	// maxLimit is the maximum page size for audit queries.
	maxLimit = 1000
)

// normalizeLimit clamps and defaults filter.Limit.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
