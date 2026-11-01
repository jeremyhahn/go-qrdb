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

package grpc

import (
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// TestMapServiceError_LockOutcomesCarryTheRefusal pins that each typed lock
// outcome leaves the gRPC server -- and the unix server, which registers the
// same service -- with its code, its reason and metadata that rebuild the
// error, key, holder and tokens included.
func TestMapServiceError_LockOutcomesCarryTheRefusal(t *testing.T) {
	tests := []struct {
		name    string
		err     interface{ Metadata() map[string]string }
		code    codes.Code
		reason  string
		rebuild func(map[string]string) any
	}{
		{
			name: "held", err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			code: codes.Aborted, reason: lock.ReasonLockHeld,
			rebuild: func(md map[string]string) any { return lock.LockHeldFromMetadata(md) },
		},
		{
			name: "not holder", err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			code: codes.PermissionDenied, reason: lock.ReasonNotLockHolder,
			rebuild: func(md map[string]string) any { return lock.NotLockHolderFromMetadata(md) },
		},
		{
			name: "not found", err: &lock.LockNotFoundError{Key: "k"},
			code: codes.NotFound, reason: lock.ReasonLockNotFound,
			rebuild: func(md map[string]string) any { return lock.LockNotFoundFromMetadata(md) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapServiceError(tt.err.(error))
			if status.Code(err) != tt.code {
				t.Fatalf("code = %v, want %v", status.Code(err), tt.code)
			}
			reason, md, ok := refusalFromStatus(status.Convert(err))
			if !ok || reason != tt.reason {
				t.Fatalf("refusal = %q, %v; want %q", reason, ok, tt.reason)
			}
			if got := tt.rebuild(md); !reflect.DeepEqual(got, tt.err) {
				t.Fatalf("metadata rebuilds %#v, want %#v", got, tt.err)
			}
		})
	}
}
