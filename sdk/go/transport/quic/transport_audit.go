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

// transport_audit.go wires the QUIC transport to the server's "audit.Query"
// wire-frame method (registered in pkg/api/quic/dispatch.go's handler table
// only when an AuditLogger is configured — see
// pkg/api/quic/audit_frame.go's handleAuditQuery). transport.AuditFilter,
// transport.AuditEvent, and transport.AuditPage carry the same JSON tags as
// their pkg/audit counterparts (audit.Filter / audit.AuditEvent / audit.Page),
// so the wire payload is the SDK type marshaled directly -- no intermediate
// wire-request/response structs are needed, unlike the other auth.go-style
// QUIC methods in this package.
package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.AuditClient   = (*Transport)(nil)
	_ transport.AuditProvider = (*Transport)(nil)
)

// AuditClient returns the QUIC transport's own AuditClient projection.
// Implements transport.AuditProvider.
func (t *Transport) AuditClient() transport.AuditClient {
	return t
}

// EmitAudit is not supported over the QUIC transport: audit events are
// emitted server-side by the request handlers themselves, and the QUIC
// wire protocol exposes no audit-emit method. Remote callers query audit
// via QueryAudit; emission belongs to the in-process (embedded) transport.
func (t *Transport) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "EmitAudit"}
}

// QueryAudit queries the audit log via the QUIC "audit.Query" wire method.
// Authorization: requires "audit:query" permission.
func (t *Transport) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(f)
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "audit.Query", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: err}
	}

	var page transport.AuditPage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: &UnmarshalError{Err: err}}
	}
	return &page, nil
}
