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
// second sink. Emit is non-blocking and best-effort by contract, which
// preserves the middleware's requirement that audit failures never block
// or fail the decision being recorded.
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

// Log converts entry to an AuditEvent and emits it. It never returns an
// error: Emit is best-effort by contract, and a decision must never fail
// because its audit record could not be written.
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
	l.sink.Emit(ctx, ev)
	return nil
}

// Close is a no-op: the underlying AuditLogger's lifecycle belongs to its
// owner (the node or daemon that constructed it), not to the middleware.
func (l *decisionLogger) Close() error { return nil }
