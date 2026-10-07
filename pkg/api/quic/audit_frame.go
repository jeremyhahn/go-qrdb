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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// auditQueryPayload is "audit.Query"'s wire payload: audit.Filter's own
// fields, flattened (embedding), plus one QUIC-specific addition.
// Replicated is never part of audit.Filter itself -- that type is shared
// with the node-local sinks' own matching logic, which has no concept of a
// replicated chain.
type auditQueryPayload struct {
	audit.Filter
	Replicated bool `json:"replicated,omitempty"`
}

// handleAuditQuery handles the "audit.Query" QUIC frame method.
//
// The payload is a JSON-encoded auditQueryPayload (audit.Filter's fields
// plus "replicated"). The response is a JSON-encoded audit.Page.
//
// Authorization: requires "audit:query" (permission.AuditQuery), and the query
// is confined to the caller's own tenant (tenant.ConfineAuditFilter)
// whichever backend answers it; a filter naming another tenant is refused
// with StatusPermissionDenied. replicated=true reads the cluster-wide
// replicated chain (docs/audit/architecture.md#cluster-wide-replicated-trail)
// instead of the node-local trail.
func (s *Server) handleAuditQuery(ctx context.Context, payload []byte) ([]byte, error) {
	var req auditQueryPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &UnmarshalError{Err: err}
		}
	}

	var logger audit.AuditLogger
	switch {
	case req.Replicated && s.replicatedAudit == nil:
		return nil, &ServiceNotConfiguredError{Service: "replicated audit"}
	case req.Replicated:
		logger = s.replicatedAudit
	case s.auditLogger == nil:
		return nil, &ServiceNotConfiguredError{Service: "audit"}
	default:
		logger = s.auditLogger
	}

	f, err := tenant.ConfineAuditFilter(ctx, req.Filter)
	if err != nil {
		return nil, err
	}

	page, err := logger.Query(ctx, f)
	if err != nil {
		return nil, err
	}

	b, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// handleAuditVerifyChain handles the "audit.VerifyChain" QUIC frame method.
//
// The payload is ignored (no parameters today). The response is a
// JSON-encoded audit.VerificationReport.
//
// A non-nil error means verification could not be PERFORMED (unsupported
// backend, missing file, or a rotation race); a successful response with
// Verified=false is a TAMPER VERDICT, not a failure -- see pkg/audit.Verify.
//
// Authorization: requires "audit:verify" permission.
func (s *Server) handleAuditVerifyChain(ctx context.Context, _ []byte) ([]byte, error) {
	if s.auditLogger == nil {
		return nil, &ServiceNotConfiguredError{Service: "audit"}
	}

	report, err := audit.Verify(ctx, s.auditLogger)
	if err != nil {
		return nil, err
	}

	b, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// auditVerifyReplicatedChainPayload is "audit.VerifyReplicatedChain"'s wire
// payload: the tenant to verify. Empty means the caller's own tenant.
type auditVerifyReplicatedChainPayload struct {
	TenantID string `json:"tenant_id,omitempty"`
}

// handleAuditVerifyReplicatedChain handles the
// "audit.VerifyReplicatedChain" QUIC frame method: hash-chain continuity
// for the cluster-wide replicated audit chain
// (docs/audit/architecture.md#cluster-wide-replicated-trail), for the
// caller's own tenant (tenant.Confine; a tenant_id naming another tenant is
// refused with StatusPermissionDenied). The response is a JSON-encoded
// audit.ChainContinuityReport.
//
// Authorization: requires "audit:verify" permission.
func (s *Server) handleAuditVerifyReplicatedChain(ctx context.Context, payload []byte) ([]byte, error) {
	if s.replicatedAudit == nil {
		return nil, &ServiceNotConfiguredError{Service: "replicated audit"}
	}

	var req auditVerifyReplicatedChainPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &UnmarshalError{Err: err}
		}
	}

	tenantID, err := tenant.Confine(ctx, req.TenantID, "verify replicated audit chain")
	if err != nil {
		return nil, err
	}

	report, err := s.replicatedAudit.VerifyTenantChain(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	b, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return b, nil
}
