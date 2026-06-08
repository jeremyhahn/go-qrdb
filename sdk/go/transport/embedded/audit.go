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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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

// auditEventToDomain converts an SDK transport.AuditEvent to the internal
// audit.AuditEvent. The Attributes map is copied so the caller's map is
// independent of the logger's internal state.
func auditEventToDomain(ev transport.AuditEvent) audit.AuditEvent {
	var attrs map[string]any
	if len(ev.Attributes) > 0 {
		attrs = make(map[string]any, len(ev.Attributes))
		for k, v := range ev.Attributes {
			attrs[k] = v
		}
	}
	return audit.AuditEvent{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: attrs,
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
		PrevHash:   ev.PrevHash,
	}
}

// auditEventToTransport converts an internal audit.AuditEvent to the SDK
// transport.AuditEvent. The Attributes map is copied for the same isolation
// reason as auditEventToDomain.
func auditEventToTransport(ev audit.AuditEvent) transport.AuditEvent {
	var attrs map[string]any
	if len(ev.Attributes) > 0 {
		attrs = make(map[string]any, len(ev.Attributes))
		for k, v := range ev.Attributes {
			attrs[k] = v
		}
	}
	return transport.AuditEvent{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: attrs,
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
		PrevHash:   ev.PrevHash,
	}
}

// auditFilterToDomain converts an SDK transport.AuditFilter to the internal
// audit.Filter.
func auditFilterToDomain(f transport.AuditFilter) audit.Filter {
	return audit.Filter{
		From:     f.From,
		To:       f.To,
		Actor:    f.Actor,
		Resource: f.Resource,
		Action:   f.Action,
		Outcome:  f.Outcome,
		TenantID: f.TenantID,
		Limit:    f.Limit,
		Cursor:   f.Cursor,
	}
}

// auditPageToTransport converts an internal *audit.Page to an SDK
// *transport.AuditPage. A nil page yields a nil result.
func auditPageToTransport(p *audit.Page) *transport.AuditPage {
	if p == nil {
		return nil
	}
	events := make([]transport.AuditEvent, len(p.Events))
	for i, ev := range p.Events {
		events[i] = auditEventToTransport(ev)
	}
	return &transport.AuditPage{
		Events: events,
		Next:   p.Next,
		Total:  p.Total,
	}
}

// EmitAudit records a compliance-grade audit event through the wrapped
// AuditLogger. Emit is best-effort and non-blocking per the logger contract,
// so this returns nil once the event has been handed off.
func (c *auditClient) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.auditLogger == nil {
		return &MissingAuditLoggerError{}
	}
	c.transport.auditLogger.Emit(ctx, auditEventToDomain(ev))
	return nil
}

// QueryAudit returns a paginated page of audit events matching the filter
// through the wrapped AuditLogger.
func (c *auditClient) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.auditLogger == nil {
		return nil, &MissingAuditLoggerError{}
	}
	page, err := c.transport.auditLogger.Query(ctx, auditFilterToDomain(f))
	if err != nil {
		return nil, translateError(err)
	}
	return auditPageToTransport(page), nil
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
