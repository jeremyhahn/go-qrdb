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
	"time"
)

// SlogSink mirrors every audit event to the default slog logger at Info level.
// Each event is emitted as structured slog attributes so that operators can
// grep for audit events in the operational log stream without needing to query
// the audit file separately.
type SlogSink struct {
	logger *slog.Logger
}

// NewSlogSink returns a SlogSink that writes to the default slog logger.
func NewSlogSink() *SlogSink {
	return &SlogSink{logger: slog.Default()}
}

// NewSlogSinkWith returns a SlogSink that writes to the provided logger.
func NewSlogSinkWith(l *slog.Logger) *SlogSink {
	return &SlogSink{logger: l}
}

// Emit mirrors ev to slog. It is always synchronous and does not block callers
// beyond the slog handler's own latency.
func (s *SlogSink) Emit(_ context.Context, ev AuditEvent) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	attrs := []slog.Attr{
		slog.Time("timestamp", ev.Timestamp),
		slog.String("actor", ev.Actor),
		slog.String("resource", ev.Resource),
		slog.String("action", ev.Action),
		slog.String("outcome", ev.Outcome),
	}
	if ev.TenantID != "" {
		attrs = append(attrs, slog.String("tenant_id", ev.TenantID))
	}
	if ev.Role != "" {
		attrs = append(attrs, slog.String("role", ev.Role))
	}
	if ev.ClientIP != "" {
		attrs = append(attrs, slog.String("client_ip", ev.ClientIP))
	}
	if ev.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", ev.RequestID))
	}
	if len(ev.Attributes) > 0 {
		attrs = append(attrs, slog.Any("attributes", ev.Attributes))
	}

	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)
}

// Query returns an empty page; the slog sink does not store events.
func (s *SlogSink) Query(_ context.Context, _ Filter) (*Page, error) {
	return &Page{Total: -1}, nil
}

// Close is a no-op; the slog sink holds no external resources.
func (s *SlogSink) Close() error { return nil }
