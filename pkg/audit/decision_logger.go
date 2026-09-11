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

package audit

import "context"

// decisionLogger adapts the auth middleware's per-decision Logger contract
// (Log/Close over Entry) onto the canonical chained AuditLogger (Emit over
// AuditEvent), so authorization decisions land in the SAME tamper-evident
// event log as every other audited operation — one audit plane, not a
// second sink.
//
// It is a pure adapter: whatever the sink says about the record, Log says to
// the middleware. Until go-qrdb#335 it discarded the sink's answer and
// returned nil unconditionally, which made the middleware's fail-closed
// branch structurally unreachable and the compliance property "no
// authorization decision without a persisted record" unenforceable by
// construction.
type decisionLogger struct {
	sink AuditLogger
}

// NewDecisionLogger returns a Logger that forwards each middleware decision
// entry into sink as a canonical AuditEvent. sink must not be nil.
func NewDecisionLogger(sink AuditLogger) Logger {
	if sink == nil {
		panic("audit: NewDecisionLogger: sink must not be nil")
	}
	return &decisionLogger{sink: sink}
}

// Log converts entry to an AuditEvent and emits it, returning whatever the
// sink reported — a *WriteError when the record did not reach the sink's
// durability boundary, nil when it did.
//
// A nil entry is not an error: there is nothing to record and nothing failed.
func (l *decisionLogger) Log(ctx context.Context, entry *Entry) error {
	if entry == nil {
		return nil
	}
	ev := AuditEvent{
		Timestamp: entry.Timestamp,
		Actor:     entry.Subject,
		TenantID:  entry.TenantID,
		Resource:  entry.Resource,
		Action:    entry.Action,
		Outcome:   entry.Result,
	}
	if len(entry.Metadata) > 0 {
		ev.Attributes = make(map[string]any, len(entry.Metadata))
		for k, v := range entry.Metadata {
			ev.Attributes[k] = v
		}
	}
	return l.sink.Emit(ctx, ev)
}

// Close is a no-op: the underlying AuditLogger's lifecycle belongs to its
// owner (the node or daemon that constructed it), not to the middleware.
func (l *decisionLogger) Close() error { return nil }
