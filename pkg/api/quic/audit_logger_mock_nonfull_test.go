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

//go:build !qrdb_ts

// audit_logger_mock_nonfull_test.go provides mockAuditLogger for the
// non-full-tag builds (no-tags `go vet ./...` and codec_json+raft). Under the
// full tag set, audit_frame_test.go (gated on the full set) provides the same
// type; the `!qrdb_ts` constraint here makes the two definitions mutually
// exclusive so there is no duplicate-symbol error in any tag context.

package quic

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

type mockAuditLogger struct {
	queryFn func(ctx context.Context, f audit.Filter) (*audit.Page, error)
}

func (m *mockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) {}

func (m *mockAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, f)
	}
	return &audit.Page{
		Events: []audit.AuditEvent{
			{Actor: "alice", Resource: "barrier", Action: "initialize", Outcome: "success", Timestamp: time.Now()},
		},
		Total: 1,
	}, nil
}

func (m *mockAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*mockAuditLogger)(nil)
