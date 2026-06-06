// Copyright (c) 2026 Jeremy Hahn
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

package audit

import "context"

// AuditLogger is the compliance-grade audit logging interface. All QRDB
// subsystems (barrier, ceremony, RBAC, keys, tenants) use this interface to
// record structured audit events. Implementations must be safe for concurrent
// use from multiple goroutines.
//
// Example emission in a barrier handler:
//
//	s.auditLogger.Emit(ctx, audit.AuditEvent{
//	    Actor:    identity.Subject,
//	    TenantID: tenantID,
//	    Role:     identity.Role,
//	    Resource: "barrier",
//	    Action:   "initialize",
//	    Outcome:  audit.OutcomeSuccess,
//	    ClientIP: r.RemoteAddr,
//	    RequestID: requestID,
//	})
type AuditLogger interface {
	// Emit records an audit event. It is non-blocking and best-effort:
	// implementations must not block the caller, and transient write failures
	// must be silently absorbed (logged via slog at Error level internally).
	Emit(ctx context.Context, ev AuditEvent)

	// Query returns a page of audit events matching the provided filter.
	// Implementations should support time range, actor, resource, action,
	// outcome, tenant, and cursor-based pagination.
	Query(ctx context.Context, f Filter) (*Page, error)

	// Close releases all resources held by the logger (file handles, buffers).
	Close() error
}

// aggregateLogger fans out Emit calls to all child AuditLoggers in parallel
// and collects Query results from the first non-empty child that responds.
type aggregateLogger struct {
	loggers []AuditLogger
}

// NewAggregateLogger returns an AuditLogger that fans out to all provided
// loggers. Each Emit call is dispatched to every child. Query is served by
// the first child logger in the list that returns a non-nil page.
func NewAggregateLogger(loggers ...AuditLogger) AuditLogger {
	return &aggregateLogger{loggers: loggers}
}

// Emit dispatches the event to all child loggers. Each child is called in
// sequence; failures are silently absorbed per the best-effort contract.
func (a *aggregateLogger) Emit(ctx context.Context, ev AuditEvent) {
	for _, l := range a.loggers {
		l.Emit(ctx, ev)
	}
}

// Query returns the first non-empty page from any child logger. If all
// children return empty pages, it returns the last non-nil result.
func (a *aggregateLogger) Query(ctx context.Context, f Filter) (*Page, error) {
	var lastPage *Page
	for _, l := range a.loggers {
		page, err := l.Query(ctx, f)
		if err != nil {
			return nil, err
		}
		if page != nil && len(page.Events) > 0 {
			return page, nil
		}
		if page != nil {
			lastPage = page
		}
	}
	if lastPage != nil {
		return lastPage, nil
	}
	return &Page{Total: -1}, nil
}

// Close closes all child loggers. The first error encountered is returned;
// remaining children are still closed.
func (a *aggregateLogger) Close() error {
	var firstErr error
	for _, l := range a.loggers {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
