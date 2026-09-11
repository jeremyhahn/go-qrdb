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
// What a GATING call site does when the event that gates an operation cannot
// be written -- the authorization middleware's decision, and (go-qrdb#445) the
// security officer's PIN verification. Re-exported because it is a COMPLIANCE
// SETTING a composing host has to be able to state, read back and test against
// without importing pkg/audit or pkg/auth/middleware.
//
// ONE posture governs both gates. There is no second setting and no second
// type; auth.audit_failure_policy is the whole lever.
// ---------------------------------------------------------------------------

// AuditFailurePolicy selects what a gating call site does when its audit
// record cannot be written. Its zero value is AuditFailClosed.
type AuditFailurePolicy = audit.FailurePolicy

const (
	// AuditFailClosed refuses the operation when its audit record could not be
	// written. The default, and the zero value.
	AuditFailClosed = audit.FailClosed

	// AuditFailOpen permits the operation and counts the failure on
	// qrdb_auth_audit_write_failures_total{outcome="permitted"} (the
	// authorization gate) or qrdb_audit_gate_failures_total{outcome="permitted"}
	// (every other gate). Selecting it logs a warning at every startup naming
	// the property given up.
	AuditFailOpen = audit.FailOpen
)

// ParseAuditFailurePolicy resolves a configured spelling ("fail-closed",
// "fail-open", or empty for the default) onto an AuditFailurePolicy. An
// unrecognised value is an error, never a guessed posture.
var ParseAuditFailurePolicy = audit.ParseFailurePolicy

// AuditGateError is returned by a gating call site when the event that gates
// the operation could not be written and the policy is AuditFailClosed
// (pkg/audit.GateError). Component names the gate that refused --
// "authorize", "so-auth", "so-dual-factor". It wraps the sink's own error --
// typically a *NodeAuditWriteError -- and maps to HTTP 503 / gRPC Unavailable
// on every transport.
type AuditGateError = audit.GateError

// InvalidAuditFailurePolicyError is what ParseAuditFailurePolicy returns for a
// spelling this build does not understand. A typo fails the boot rather than
// resolving to either posture.
type InvalidAuditFailurePolicyError = audit.InvalidFailurePolicyError
