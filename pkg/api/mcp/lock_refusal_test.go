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

package mcp

import (
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// TestClassifiedToolError_LockOutcomesCarryTheRefusal pins that each typed
// lock outcome leaves the MCP server with its code, its reason and metadata
// that rebuild the error -- key, holder and tokens included. The tool
// handlers wrap the outcome in a ToolError, so the test does too.
func TestClassifiedToolError_LockOutcomesCarryTheRefusal(t *testing.T) {
	tests := []struct {
		name    string
		err     interface{ Metadata() map[string]string }
		code    wire.Code
		reason  string
		rebuild func(map[string]string) any
	}{
		{
			name: "held", err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			code: wire.CodeAborted, reason: lock.ReasonLockHeld,
			rebuild: func(md map[string]string) any { return lock.LockHeldFromMetadata(md) },
		},
		{
			name: "not holder", err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			code: wire.CodePermissionDenied, reason: lock.ReasonNotLockHolder,
			rebuild: func(md map[string]string) any { return lock.NotLockHolderFromMetadata(md) },
		},
		{
			name: "not found", err: &lock.LockNotFoundError{Key: "k"},
			code: wire.CodeNotFound, reason: lock.ReasonLockNotFound,
			rebuild: func(md map[string]string) any { return lock.LockNotFoundFromMetadata(md) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ToolError{Tool: "qrdb_lock_release", Err: tt.err.(error)}
			payload, ok := wire.DecodeErrorPayload(classifiedToolError(err.Tool, err).StructuredContent)
			if !ok || payload.Code != tt.code || payload.Reason != tt.reason {
				t.Fatalf("payload = %+v (ok %v), want code %d with the %s refusal", payload, ok, tt.code, tt.reason)
			}
			if got := tt.rebuild(payload.Metadata); !reflect.DeepEqual(got, tt.err) {
				t.Fatalf("metadata rebuilds %#v, want %#v", got, tt.err)
			}
		})
	}
}
