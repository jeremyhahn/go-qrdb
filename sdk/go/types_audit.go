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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
)

// ---------------------------------------------------------------------------
// Node audit logger
//
// A composing host (xkms, quicbaas) supplies the embedded node's
// compliance-grade audit sink through these re-exports so it never imports
// pkg/audit or pkg/server directly. The sink is the HMAC-chained subsystem
// that backs Client().Audit().EmitAudit / QueryAudit.
//
// Note: the bare name "AuditLogger" is already taken by the seal audit
// interface (types_seal.go), so the node sink interface is exposed as
// NodeAuditLogger.
// ---------------------------------------------------------------------------

// NodeAuditLogger is the embedded node's compliance-grade audit sink. It is
// the pkg/audit.AuditLogger interface that backs the node's audit subsystem.
type NodeAuditLogger = audit.AuditLogger

// WithAuditLogger attaches a compliance-grade audit sink to the embedded
// qrdb node. Without it the node has no default audit logger and
// Client().Audit().EmitAudit returns a missing-logger error.
func WithAuditLogger(l NodeAuditLogger) ServerOption {
	return qrdbserver.WithAuditLogger(l)
}

// NewAuditFileLogger returns an HMAC-chained, tamper-evident file audit
// logger writing to path. Use it as the node sink when persistent,
// compliance-grade audit storage is required.
func NewAuditFileLogger(path string) (NodeAuditLogger, error) {
	return audit.NewFileLogger(path)
}

// NewAuditRingBuffer returns an in-memory, queryable audit logger holding up
// to capacity events. Use it as the node sink when persistent file audit is
// disabled but audit emission and querying must still function.
func NewAuditRingBuffer(capacity int) NodeAuditLogger {
	return audit.NewRingBuffer(capacity)
}

// NodeAuditEvent is the canonical audit record the node sink accepts
// (pkg/audit.AuditEvent): actor, tenant, resource, action, outcome,
// attributes, and the tamper-evidence chain field.
type NodeAuditEvent = audit.AuditEvent

// NodeAuditFilter filters NodeAuditLogger.Query.
type NodeAuditFilter = audit.Filter

// NodeAuditPage is one page of queried audit events.
type NodeAuditPage = audit.Page

// NodeAuditWriteError is what a NodeAuditLogger returns from Emit when the
// event did not reach its durability boundary (pkg/audit.WriteError). Backend
// names the destination that failed.
//
// A composing host implementing its own NodeAuditLogger should return one of
// these rather than an ad-hoc error: the authorization middleware unwraps to
// it, and the audit feature's own tooling reports on it.
type NodeAuditWriteError = audit.WriteError

// ---------------------------------------------------------------------------
// Audit failure posture
//
// What the authorization middleware does when a decision's audit record
// cannot be written. Re-exported because it is a COMPLIANCE SETTING a
// composing host has to be able to state, read back and test against without
// importing pkg/auth/middleware.
// ---------------------------------------------------------------------------

// AuditFailurePolicy selects what the authorization middleware does when a
// decision's audit record cannot be written. Its zero value is
// AuditFailClosed.
type AuditFailurePolicy = authmw.AuditFailurePolicy

const (
	// AuditFailClosed refuses the request when its audit record could not be
	// written. The default, and the zero value.
	AuditFailClosed = authmw.AuditFailClosed

	// AuditFailOpen permits the request and counts the failure on
	// qrdb_auth_audit_write_failures_total{outcome="permitted"}. Selecting it
	// logs a warning at every startup naming the property given up.
	AuditFailOpen = authmw.AuditFailOpen
)

// ParseAuditFailurePolicy resolves a configured spelling ("fail-closed",
// "fail-open", or empty for the default) onto an AuditFailurePolicy. An
// unrecognised value is an error, never a guessed posture.
var ParseAuditFailurePolicy = authmw.ParseAuditFailurePolicy

// AuditDecisionError is returned by the authorization middleware when a
// decision's audit record could not be written and the policy is
// AuditFailClosed (pkg/auth/middleware.AuditError). It wraps the sink's own
// error -- typically a *NodeAuditWriteError -- and maps to HTTP 503 /
// gRPC Unavailable.
type AuditDecisionError = authmw.AuditError
