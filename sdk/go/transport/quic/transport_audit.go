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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/auditconv"
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

	// The frame's payload is an audit.Filter: transport.AuditFilter names its
	// tenant "tenantID" where the server reads "tenant_id", so marshaling it
	// directly dropped the tenant from every query.
	payload, err := json.Marshal(auditconv.FilterToDomain(f))
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "audit.Query", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: err}
	}

	var page audit.Page
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: &UnmarshalError{Err: err}}
	}
	return auditconv.PageToTransport(&page), nil
}

// VerifyChain verifies the audit log's tamper-evidence hash chain via the
// QUIC "audit.VerifyChain" wire method. A non-nil error means verification
// could not be PERFORMED; a successful response with Verified=false is a
// TAMPER VERDICT, not an error -- see pkg/audit.Verify.
// Authorization: requires "audit:verify" permission.
func (t *Transport) VerifyChain(ctx context.Context) (*transport.AuditVerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	data, err := t.call(ctx, "audit.VerifyChain", nil)
	if err != nil {
		// Normalize to the same transport.NotImplementedError REST and gRPC
		// report for this condition (their 501 / codes.Unimplemented special
		// cases in transport_audit.go), rather than the generic
		// CapabilityUnsupportedError typedWireErrors reconstitutes for every
		// other StatusUnimplemented verb. The Tier-1 client contract for
		// "this node cannot verify its audit chain" must not depend on which
		// transport reached it.
		var unsupported *transport.CapabilityUnsupportedError
		if errors.As(err, &unsupported) {
			return nil, &transport.NotImplementedError{Method: "VerifyChain"}
		}
		return nil, &transport.RequestError{Method: "VerifyChain", Err: err}
	}

	var result transport.AuditVerifyResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, &transport.RequestError{Method: "VerifyChain", Err: &UnmarshalError{Err: err}}
	}
	return &result, nil
}
