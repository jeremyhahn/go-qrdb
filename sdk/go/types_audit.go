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
