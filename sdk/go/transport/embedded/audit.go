// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/auditconv"
)

// MissingAuditLoggerError is returned by the embedded AuditClient when it is
// invoked without an injected audit.AuditLogger. Wire one up via
// WithAuditLogger to enable audit emission and queries.
type MissingAuditLoggerError struct{}

// Error returns a human-readable description.
func (e *MissingAuditLoggerError) Error() string {
	return "embedded: audit logger is not configured"
}

// auditClient is the embedded implementation of transport.AuditClient. It
// wraps the node's audit.AuditLogger, converting between the SDK transport
// audit types and the internal pkg/audit types on each call. The logger's
// Emit contract is best-effort and non-blocking, so EmitAudit always returns
// nil once the event is handed off.
type auditClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.AuditClient = (*auditClient)(nil)

// EmitAudit records a compliance-grade audit event through the wrapped
// AuditLogger and returns whether it was recorded.
//
// PROPAGATED, not best-effort: the caller asked for this event to be
// recorded and has nothing else to go on. A nil return means the sink
// reached its durability boundary; a non-nil return is the sink's own
// *audit.WriteError naming the backend that refused. Before go-qrdb#335 this
// returned nil unconditionally, which told an embedder its compliance record
// had landed when it had not.
func (c *auditClient) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.auditLogger == nil {
		return &MissingAuditLoggerError{}
	}
	return c.transport.auditLogger.Emit(ctx, auditconv.EventToDomain(ev))
}

// QueryAudit returns a paginated page of audit events matching the filter
// through the wrapped AuditLogger, confined to the caller's own tenant
// (tenant.ConfineAuditFilter) exactly as on the network transports: the
// identity in ctx when there is one, otherwise the tenant bound to ctx. A
// filter naming another tenant is refused with *tenant.CrossTenantDeniedError.
func (c *auditClient) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.auditLogger == nil {
		return nil, &MissingAuditLoggerError{}
	}
	confined, err := tenant.ConfineAuditFilter(ctx, auditconv.FilterToDomain(f))
	if err != nil {
		return nil, err
	}
	page, err := c.transport.auditLogger.Query(ctx, confined)
	if err != nil {
		return nil, translateError(err)
	}
	return auditconv.PageToTransport(page), nil
}

// auditVerificationReportToTransport converts an internal
// *audit.VerificationReport to the SDK's *transport.AuditVerifyResult.
func auditVerificationReportToTransport(r *audit.VerificationReport) *transport.AuditVerifyResult {
	return &transport.AuditVerifyResult{
		Verified: r.Verified,
		Reason:   r.Reason,
		Files:    r.Files,
		Records:  r.Records,
		Order:    r.Order,
		Genesis:  r.Genesis,
		Seed:     r.Seed,
		Handoff:  r.Handoff,
	}
}

// VerifyChain verifies the wrapped AuditLogger's tamper-evidence hash chain.
// A non-nil error means verification could not be PERFORMED (unsupported
// backend, missing file, or a rotation race); a successful result with
// Verified=false is a TAMPER VERDICT, not an error -- see pkg/audit.Verify.
func (c *auditClient) VerifyChain(ctx context.Context) (*transport.AuditVerifyResult, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.auditLogger == nil {
		return nil, &MissingAuditLoggerError{}
	}
	report, err := audit.Verify(ctx, c.transport.auditLogger)
	if err != nil {
		return nil, translateError(err)
	}
	return auditVerificationReportToTransport(report), nil
}

// AuditClient returns the embedded audit client projection. Implements
// transport.AuditProvider. Returns nil when no AuditLogger has been injected
// so the SDK accessor degrades to nil rather than surfacing a logger-less
// client.
func (t *Transport) AuditClient() transport.AuditClient {
	if t.auditLogger == nil {
		return nil
	}
	return &auditClient{transport: t}
}

// WithAuditLogger injects an audit.AuditLogger into the embedded transport.
// The logger backs the AuditClient's EmitAudit and QueryAudit methods.
func WithAuditLogger(logger audit.AuditLogger) Option {
	return func(t *Transport) {
		t.auditLogger = logger
	}
}
