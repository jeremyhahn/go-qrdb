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

// NoopLogger silently discards all audit events. It is returned by New when
// auditing is disabled or when no backends are configured. Embedded consumers
// that do not require audit functionality can use NoopLogger directly.
type NoopLogger struct{}

// NewNoopLogger returns a NoopLogger.
func NewNoopLogger() *NoopLogger { return &NoopLogger{} }

// Emit discards the event and reports success.
//
// Nil is the truthful answer, not a stub: discarding IS this sink's
// durability boundary. An operator who configures no audit backend has
// chosen to keep no trail, and a middleware that fails closed on audit write
// failure must not refuse every request because of that choice.
func (n *NoopLogger) Emit(_ context.Context, _ AuditEvent) error { return nil }

// Query returns an empty page with Total -1.
func (n *NoopLogger) Query(_ context.Context, _ Filter) (*Page, error) {
	return &Page{Total: -1}, nil
}

// Close is a no-op.
func (n *NoopLogger) Close() error { return nil }

// Compile-time interface assertion.
var _ AuditLogger = (*NoopLogger)(nil)
