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

	// Replicated, when true, reads the cluster-wide replicated audit chain
	// (docs/audit/architecture.md#cluster-wide-replicated-trail) instead of
	// the node-local trail. A tenant not entitled to it answers an empty
	// page (Total: -1), never an error.
	Replicated bool `json:"replicated,omitempty"`
}

// AuditPage is the response for a paginated audit query.
type AuditPage struct {
	Events []AuditEvent `json:"events"`
	Next   string       `json:"next,omitempty"`
	Total  int          `json:"total"`
}

// AuditVerifyResult is the outcome of verifying the audit log's
// tamper-evidence hash chain (go-qrdb#453). Verified is the field a caller
// checks first: false is a TAMPER VERDICT reported through this result, not
// an error -- VerifyChain returns a non-nil error only when the
// verification could not be PERFORMED at all (unsupported backend, missing
// file, or a rotation race).
type AuditVerifyResult struct {
	// Verified is true only when every linked file's chain checked out.
	Verified bool `json:"verified"`

	// Reason is a human-readable description of the break. Empty when
	// Verified is true.
	Reason string `json:"reason,omitempty"`

	// Files is the number of rotation-set files that carry records and
	// were linked.
	Files int `json:"files"`

	// Records is the number of audit records verified, anchors excluded.
	// int64: a trail's record count is unbounded over its retention window.
	Records int64 `json:"records"`

	// Order lists the linked files in chain order, head first.
	Order []string `json:"order,omitempty"`

	// Genesis reports whether the walk reached the chain's origin.
	Genesis bool `json:"genesis"`

	// Seed is the hex digest the head claims to continue from, empty at
	// genesis. Handoff is the hex digest the tail ends on.
	Seed    string `json:"seed,omitempty"`
	Handoff string `json:"handoff,omitempty"`
}

// ChainContinuityResult is the outcome of verifying one tenant's
// cluster-wide REPLICATED audit chain's hash continuity
// (docs/audit/architecture.md#cluster-wide-replicated-trail). It mirrors
// pkg/audit.ChainContinuityReport for SDK transport use, and is
// deliberately a different shape from AuditVerifyResult above: there are no
// files, rotation or order concepts here -- one tenant has one
// Raft-replicated chain, not a set of NDJSON archives.
type ChainContinuityResult struct {
	// Verified is true only if every surviving event folds, in sequence,
	// into the chain's own authoritative head digest.
	Verified bool `json:"verified"`

	// Reason is a human-readable description of the break. Empty when
	// Verified is true.
	Reason string `json:"reason,omitempty"`

	// Records is the number of surviving events folded.
	Records int64 `json:"records"`

	// Head is the highest surviving sequence number.
	Head uint64 `json:"head"`

	// Low is the lowest surviving sequence number.
	Low uint64 `json:"low"`

	// Genesis is true when Low == 1: the walk reached the chain's true
	// origin rather than a retention-pruned tail.
	Genesis bool `json:"genesis"`
}

// AuditClient is the interface for audit event emission and retrieval over
// any transport.
// Authorization: EmitAudit requires "audit:emit"; QueryAudit requires
// "audit:query".
// TODO: switch to permission.AuditEmit/AuditQuery constants once Phase 2 lands.
type AuditClient interface {
	// EmitAudit records a compliance-grade audit event. The underlying
	// logger contract is best-effort and non-blocking; the error return
	// exists for transport symmetry (network transports surface dispatch
	// failures here). The embedded transport returns nil once the event
	// has been handed to the in-process logger.
	EmitAudit(ctx context.Context, ev AuditEvent) error

	// QueryAudit returns a paginated page of audit events matching the filter.
	QueryAudit(ctx context.Context, f AuditFilter) (*AuditPage, error)

	// VerifyChain verifies the audit log's tamper-evidence hash chain across
	// the active file and every rotation archive, and reports what the walk
	// covered. A non-nil error means the verification could not be
	// PERFORMED (unsupported backend, missing file, or a rotation race);
	// AuditVerifyResult.Verified == false is a TAMPER VERDICT, not an error.
	VerifyChain(ctx context.Context) (*AuditVerifyResult, error)

	// VerifyReplicatedChain verifies the hash-chain continuity of the
	// cluster-wide replicated audit chain for tenantID (empty means the
	// caller's own tenant; naming another tenant is refused). A tenant
	// whose replicated-audit entitlement is off is refused rather than
	// answering a trivially-true report for a chain that was never written
	// to. Requires "audit:verify_replicated" permission.
	VerifyReplicatedChain(ctx context.Context, tenantID string) (*ChainContinuityResult, error)
}
