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

// Package auditconv converts between the SDK's transport audit types and
// pkg/audit's, which are also the audit query's wire shape on the QUIC frame,
// the REST response and the MCP tool result. The two differ in their JSON
// field names (tenantID against tenant_id), so a transport that encodes or
// decodes one as the other silently drops those fields; converting through
// here is what keeps a filter's tenant and an event's tenant on the wire.
package auditconv

import (
	"maps"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// EventToDomain converts an SDK transport.AuditEvent to an audit.AuditEvent.
// Attributes are copied, so the result shares no map with ev.
func EventToDomain(ev transport.AuditEvent) audit.AuditEvent {
	return audit.AuditEvent{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: cloneAttributes(ev.Attributes),
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
		PrevHash:   ev.PrevHash,
	}
}

// EventToTransport converts an audit.AuditEvent to an SDK
// transport.AuditEvent. Attributes are copied, so the result shares no map
// with ev.
func EventToTransport(ev audit.AuditEvent) transport.AuditEvent {
	return transport.AuditEvent{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: cloneAttributes(ev.Attributes),
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
		PrevHash:   ev.PrevHash,
	}
}

// FilterToDomain converts an SDK transport.AuditFilter to an audit.Filter.
func FilterToDomain(f transport.AuditFilter) audit.Filter {
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

// PageToTransport converts an *audit.Page to an SDK *transport.AuditPage. A
// nil page yields nil.
func PageToTransport(p *audit.Page) *transport.AuditPage {
	if p == nil {
		return nil
	}
	events := make([]transport.AuditEvent, len(p.Events))
	for i, ev := range p.Events {
		events[i] = EventToTransport(ev)
	}
	return &transport.AuditPage{
		Events: events,
		Next:   p.Next,
		Total:  p.Total,
	}
}

// cloneAttributes copies attrs, returning nil for an empty map.
func cloneAttributes(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	return maps.Clone(attrs)
}
