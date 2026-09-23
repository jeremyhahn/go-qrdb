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

// transport_audit.go wires the Unix (gRPC-over-UDS) transport to the
// qrdb.v1.AuditService (a service distinct from qrdb.v1.QRDBService,
// registered separately on the same *grpc.Server — see
// pkg/api/unix/server.go and pkg/api/grpc/audit_server.go). The client is
// constructed lazily from the transport's existing *grpc.ClientConn on each
// call rather than cached at Connect time, since AuditService is optional
// (only registered server-side when an AuditLogger is configured) and this
// avoids adding connection-lifecycle state for a rarely-called RPC. Mirrors
// sdk/go/transport/grpc/transport_audit.go, the gRPC transport's identical
// wiring over TCP.
package unix

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.AuditClient   = (*Transport)(nil)
	_ transport.AuditProvider = (*Transport)(nil)
)

// AuditClient returns the Unix transport's own AuditClient projection.
// Implements transport.AuditProvider.
func (t *Transport) AuditClient() transport.AuditClient {
	return t
}

// EmitAudit is not supported over the Unix transport: audit events are
// emitted server-side by the request handlers themselves, and
// qrdb.v1.AuditService exposes no emit RPC. Remote callers query audit via
// QueryAudit; emission belongs to the in-process (embedded) transport.
func (t *Transport) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "EmitAudit"}
}

// QueryAudit queries the audit log via the qrdb.v1.AuditService.Query RPC.
// Authorization: requires "audit:query" permission.
func (t *Transport) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := pb.NewAuditServiceClient(t.conn).Query(ctx, auditFilterToPB(f))
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
			return nil, &transport.NotImplementedError{Method: "QueryAudit"}
		}
		return nil, mapGRPCError("QueryAudit", err)
	}
	return auditQueryResponseToTransport(resp), nil
}

// VerifyChain verifies the audit log's tamper-evidence hash chain via the
// qrdb.v1.AuditService.VerifyChain RPC. A non-nil error means verification
// could not be PERFORMED; a successful response with Verified=false is a
// TAMPER VERDICT, not an error -- see pkg/audit.Verify.
// Authorization: requires "audit:verify" permission.
func (t *Transport) VerifyChain(ctx context.Context) (*transport.AuditVerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := pb.NewAuditServiceClient(t.conn).VerifyChain(ctx, &pb.AuditVerifyChainRequest{})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
			return nil, &transport.NotImplementedError{Method: "VerifyChain"}
		}
		return nil, mapGRPCError("VerifyChain", err)
	}
	return auditVerifyChainResponseToTransport(resp), nil
}

// auditVerifyChainResponseToTransport converts a generated
// AuditVerifyChainResponse to the SDK's *transport.AuditVerifyResult.
func auditVerifyChainResponseToTransport(resp *pb.AuditVerifyChainResponse) *transport.AuditVerifyResult {
	return &transport.AuditVerifyResult{
		Verified: resp.GetVerified(),
		Reason:   resp.GetReason(),
		Files:    int(resp.GetFiles()),
		Records:  resp.GetRecords(),
		Order:    resp.GetOrder(),
		Genesis:  resp.GetGenesis(),
		Seed:     resp.GetSeed(),
		Handoff:  resp.GetHandoff(),
	}
}

// auditFilterToPB converts an SDK transport.AuditFilter to the generated
// AuditQueryRequest.
func auditFilterToPB(f transport.AuditFilter) *pb.AuditQueryRequest {
	req := &pb.AuditQueryRequest{
		Actor:    f.Actor,
		Resource: f.Resource,
		Action:   f.Action,
		Outcome:  f.Outcome,
		TenantId: f.TenantID,
		Limit:    int32(f.Limit), //nolint:gosec // CLI-supplied page size, bounded server-side
		Cursor:   f.Cursor,
	}
	if !f.From.IsZero() {
		req.FromRfc3339 = f.From.UTC().Format(auditTimeLayout)
	}
	if !f.To.IsZero() {
		req.ToRfc3339 = f.To.UTC().Format(auditTimeLayout)
	}
	return req
}

// auditTimeLayout is the RFC3339 layout used to encode AuditFilter time
// bounds on the wire, matching pkg/api/grpc/audit_server.go's parser.
const auditTimeLayout = "2006-01-02T15:04:05Z07:00"

// auditQueryResponseToTransport converts a generated AuditQueryResponse to
// the SDK's *transport.AuditPage.
func auditQueryResponseToTransport(resp *pb.AuditQueryResponse) *transport.AuditPage {
	events := make([]transport.AuditEvent, len(resp.GetEvents()))
	for i, ev := range resp.GetEvents() {
		events[i] = auditEventFromPB(ev)
	}
	return &transport.AuditPage{
		Events: events,
		Next:   resp.GetNext(),
		Total:  int(resp.GetTotal()),
	}
}

// auditEventFromPB converts a generated AuditEvent to the SDK's
// transport.AuditEvent. Timestamp is parsed from RFC3339; a malformed
// timestamp (should not happen against a well-behaved server) degrades to
// the zero time rather than failing the whole query.
func auditEventFromPB(ev *pb.AuditEvent) transport.AuditEvent {
	var attrs map[string]any
	if len(ev.GetAttributes()) > 0 {
		attrs = make(map[string]any, len(ev.GetAttributes()))
		for k, v := range ev.GetAttributes() {
			attrs[k] = v
		}
	}
	return transport.AuditEvent{
		Timestamp:  parseAuditTimestamp(ev.GetTimestampRfc3339()),
		Actor:      ev.GetActor(),
		TenantID:   ev.GetTenantId(),
		Role:       ev.GetRole(),
		Resource:   ev.GetResource(),
		Action:     ev.GetAction(),
		Outcome:    ev.GetOutcome(),
		Attributes: attrs,
		ClientIP:   ev.GetClientIp(),
		RequestID:  ev.GetRequestId(),
		PrevHash:   ev.GetPrevHash(),
	}
}

// parseAuditTimestamp parses an RFC3339 audit event timestamp. An empty or
// malformed value yields the zero time rather than an error, since a single
// unparseable timestamp should not fail an entire page of results.
func parseAuditTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(auditTimeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
