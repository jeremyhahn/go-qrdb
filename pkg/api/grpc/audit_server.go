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

package grpc

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// auditServer implements the generated pb.AuditServiceServer.
type auditServer struct {
	pb.UnimplementedAuditServiceServer

	auditLogger audit.AuditLogger

	// replicatedAudit is the cluster-wide, per-tenant-opt-in replicated
	// audit sink (docs/audit/architecture.md#cluster-wide-replicated-trail).
	// Nil means this server exposes no replicated-chain capability.
	replicatedAudit *service.OptInAuditLogger
}

// RegisterAuditService registers the AuditService on the given gRPC server.
// When both auditLogger and replicatedAudit are nil the service is not
// registered, matching the other optional-service registrars in this
// package. replicatedAudit may be nil even when auditLogger is not: a
// server with only the node-local trail configured still gets Query/
// VerifyChain, with GetReplicated()/VerifyReplicatedChain answering
// Unimplemented.
func RegisterAuditService(srv *grpc.Server, auditLogger audit.AuditLogger, replicatedAudit *service.OptInAuditLogger) {
	if auditLogger == nil && replicatedAudit == nil {
		return
	}
	pb.RegisterAuditServiceServer(srv, &auditServer{auditLogger: auditLogger, replicatedAudit: replicatedAudit})
}

// Query handles the AuditService.Query RPC. The query is confined to the
// caller's own tenant (tenant.ConfineAuditFilter); a request naming another
// tenant is refused with PERMISSION_DENIED.
func (s *auditServer) Query(ctx context.Context, req *pb.AuditQueryRequest) (*pb.AuditQueryResponse, error) {
	logger := s.auditLogger
	if req.GetReplicated() {
		if s.replicatedAudit == nil {
			return nil, status.Error(codes.Unimplemented, "replicated audit not configured")
		}
		logger = s.replicatedAudit
	} else if logger == nil {
		return nil, status.Error(codes.Unimplemented, "audit service not configured")
	}

	f := audit.Filter{
		Actor:    req.GetActor(),
		Resource: req.GetResource(),
		Action:   req.GetAction(),
		Outcome:  req.GetOutcome(),
		TenantID: req.GetTenantId(),
		Limit:    int(req.GetLimit()),
		Cursor:   req.GetCursor(),
	}

	if raw := req.GetFromRfc3339(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, mapServiceError(&TimestampParseError{Field: "from_rfc3339", Value: raw, Err: err})
		}
		f.From = t
	}
	if raw := req.GetToRfc3339(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, mapServiceError(&TimestampParseError{Field: "to_rfc3339", Value: raw, Err: err})
		}
		f.To = t
	}

	f, err := tenant.ConfineAuditFilter(ctx, f)
	if err != nil {
		return nil, mapServiceError(err)
	}

	page, err := logger.Query(ctx, f)
	if err != nil {
		return nil, mapServiceError(err)
	}

	resp := &pb.AuditQueryResponse{
		Next:  page.Next,
		Total: int32(page.Total), //nolint:gosec // audit event count is bounded
	}
	for _, ev := range page.Events {
		pbEv := &pb.AuditEvent{
			TimestampRfc3339: ev.Timestamp.UTC().Format(time.RFC3339),
			Actor:            ev.Actor,
			TenantId:         ev.TenantID,
			Role:             ev.Role,
			Resource:         ev.Resource,
			Action:           ev.Action,
			Outcome:          ev.Outcome,
			ClientIp:         ev.ClientIP,
			RequestId:        ev.RequestID,
			PrevHash:         ev.PrevHash,
		}
		if len(ev.Attributes) > 0 {
			pbEv.Attributes = make(map[string]string, len(ev.Attributes))
			for k, v := range ev.Attributes {
				pbEv.Attributes[k] = auditAttrString(v)
			}
		}
		resp.Events = append(resp.Events, pbEv)
	}

	return resp, nil
}

// VerifyChain handles the AuditService.VerifyChain RPC.
//
// A non-nil gRPC error means verification could not be PERFORMED (backend
// does not support it, or the trail file is missing/racing a rotation); a
// successful response with Verified=false is a TAMPER VERDICT, not a
// failure -- see pkg/audit.Verify.
func (s *auditServer) VerifyChain(ctx context.Context, _ *pb.AuditVerifyChainRequest) (*pb.AuditVerifyChainResponse, error) {
	if s.auditLogger == nil {
		return nil, status.Error(codes.Unimplemented, "audit service not configured")
	}

	report, err := audit.Verify(ctx, s.auditLogger)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.AuditVerifyChainResponse{
		Verified: report.Verified,
		Reason:   report.Reason,
		Files:    int32(report.Files), //nolint:gosec // rotation-set file count is bounded
		Records:  report.Records,      // int64 in both the report and the proto; no truncation
		Order:    report.Order,
		Genesis:  report.Genesis,
		Seed:     report.Seed,
		Handoff:  report.Handoff,
	}, nil
}

// VerifyReplicatedChain handles the AuditService.VerifyReplicatedChain RPC:
// hash-chain continuity for the cluster-wide replicated audit chain, for the
// caller's own tenant (tenant.Confine; a tenant_id naming another tenant is
// refused with PERMISSION_DENIED). A tenant whose replicated-audit
// entitlement is off is refused with PERMISSION_DENIED rather than
// answering a trivially-true report for a chain that was never written to.
func (s *auditServer) VerifyReplicatedChain(ctx context.Context, req *pb.AuditVerifyReplicatedChainRequest) (*pb.AuditVerifyReplicatedChainResponse, error) {
	if s.replicatedAudit == nil {
		return nil, status.Error(codes.Unimplemented, "replicated audit not configured")
	}

	tenantID, err := tenant.Confine(ctx, req.GetTenantId(), "verify replicated audit chain")
	if err != nil {
		return nil, mapServiceError(err)
	}

	report, err := s.replicatedAudit.VerifyTenantChain(ctx, tenantID)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.AuditVerifyReplicatedChainResponse{
		Verified: report.Verified,
		Reason:   report.Reason,
		Records:  report.Records,
		HeadSeq:  report.Head,
		LowSeq:   report.Low,
		Genesis:  report.Genesis,
	}, nil
}

// auditAttrString converts an audit attribute value to a string for the
// protobuf map<string,string>. Strings are returned verbatim; other types
// are JSON-encoded.
func auditAttrString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
