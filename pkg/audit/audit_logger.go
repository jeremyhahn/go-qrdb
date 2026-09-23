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

import (
	"context"
	"log/slog"
)

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
	// Emit records an audit event and reports whether it was recorded.
	//
	// A nil return means the event REACHED THE SINK'S DURABILITY BOUNDARY:
	// for ChainFileLogger, appended and fsynced; for RingBuffer, held in
	// memory; for SlogSink, handed to the slog handler; for NoopLogger,
	// deliberately discarded. A non-nil return means it did not, and is a
	// *WriteError naming the backend that failed.
	//
	// Callers that treat the audit record as part of the operation they are
	// performing -- the authorization middleware is the canonical one -- must
	// act on this error. Callers for whom the record is genuinely incidental
	// may discard it, but the decision is now THEIRS: before go-qrdb#335 the
	// signature made it for them, and the compliance property "no
	// authorization decision without a persisted record" was decided by a log
	// line no caller could see.
	//
	// Implementations must not block the caller beyond the cost of their own
	// durability boundary, and must be safe for concurrent use.
	Emit(ctx context.Context, ev AuditEvent) error

	// Query returns a page of audit events matching the provided filter.
	// Implementations should support time range, actor, resource, action,
	// outcome, tenant, and cursor-based pagination.
	Query(ctx context.Context, f Filter) (*Page, error)

	// Close releases all resources held by the logger (file handles, buffers).
	Close() error
}

// aggregateLogger fans out Emit calls to every child AuditLogger IN ORDER and
// collects Query results from the first non-empty child that responds.
//
// "in parallel" is what this said until go-qrdb#335; the loop has always been
// sequential, and the ordering is load-bearing -- Emit returns the error of the
// sink that failed EARLIEST in the configured order, which is only a stable
// answer because the fan-out is ordered.
type aggregateLogger struct {
	loggers []AuditLogger
}

// NewAggregateLogger returns an AuditLogger that fans out to all provided
// loggers. Each Emit call is dispatched to every child. Query is served by
// the first child logger in the list that returns a non-nil page.
func NewAggregateLogger(loggers ...AuditLogger) AuditLogger {
	return &aggregateLogger{loggers: loggers}
}

// Emit dispatches the event to EVERY child logger and returns the first
// error any of them reported.
//
// Every child is attempted even after one fails. Stopping at the first
// failure would let a broken file sink blank the trail in the ring buffer and
// the operational log stream that were both perfectly healthy -- the fan-out
// exists precisely so that one destination's failure is not every
// destination's failure. The first error is the one returned because it names
// the sink that failed earliest in the configured order; the rest are logged.
func (a *aggregateLogger) Emit(ctx context.Context, ev AuditEvent) error {
	var firstErr error
	for _, l := range a.loggers {
		if err := l.Emit(ctx, ev); err != nil {
			if firstErr == nil {
				firstErr = err
			} else {
				slog.Error("audit: aggregate sink write failed", "error", err)
			}
		}
	}
	return firstErr
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

// VerifyChainSet delegates to the first child logger that actually HAS a
// verifying sink -- in production that is the *ChainFileLogger a compliance
// aggregate always fans out to first (cmd/qrdbd wires chainFile before the
// ring buffer and slog sink). Returns *VerificationUnsupportedError when no
// child has one, so aggregateLogger itself always satisfies the Verifier
// interface and Verify's type assertion sees the real answer rather than
// treating an all-non-chain aggregate as unsupported at that layer.
//
// "Implements Verifier" is not "has a verifying sink": aggregateLogger itself
// implements Verifier unconditionally, once the method exists on the type, so
// a NESTED aggregate with no chain-backed child among ITS OWN children still
// satisfies the interface check. Delegating to it on that basis alone stops
// the walk there and reports VerificationUnsupportedError even when a sibling
// at THIS level -- or a cousin nested somewhere else -- does have one.
// hasVerifyingSink resolves that recursively before a child is chosen, so the
// child actually delegated to is guaranteed to find a real sink when its own
// VerifyChainSet runs.
func (a *aggregateLogger) VerifyChainSet(ctx context.Context) (*ChainVerification, error) {
	for _, l := range a.loggers {
		if v, ok := l.(Verifier); ok && hasVerifyingSink(l) {
			return v.VerifyChainSet(ctx)
		}
	}
	return nil, &VerificationUnsupportedError{}
}

// hasVerifyingSink reports whether l resolves, recursively through any
// nesting of aggregateLogger, to at least one backend that implements
// Verifier. It is the recursive counterpart to the interface check alone,
// which aggregateLogger always passes regardless of what its children are.
func hasVerifyingSink(l AuditLogger) bool {
	agg, ok := l.(*aggregateLogger)
	if !ok {
		_, ok := l.(Verifier)
		return ok
	}
	for _, child := range agg.loggers {
		if hasVerifyingSink(child) {
			return true
		}
	}
	return false
}

// Compile-time interface check.
var _ Verifier = (*aggregateLogger)(nil)

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
