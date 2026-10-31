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

package quic

import (
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestErrorPayloadOf_LockOutcomesCarryTheRefusal pins that each typed lock
// outcome leaves the QUIC server with its status, its reason and metadata
// that rebuild the error -- key, holder and tokens included.
func TestErrorPayloadOf_LockOutcomesCarryTheRefusal(t *testing.T) {
	tests := []struct {
		name    string
		err     interface{ Metadata() map[string]string }
		status  uint16
		reason  string
		rebuild func(map[string]string) any
	}{
		{
			name: "held", err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			status: wire.StatusAborted, reason: lock.ReasonLockHeld,
			rebuild: func(md map[string]string) any { return lock.LockHeldFromMetadata(md) },
		},
		{
			name: "not holder", err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			status: wire.StatusPermissionDenied, reason: lock.ReasonNotLockHolder,
			rebuild: func(md map[string]string) any { return lock.NotLockHolderFromMetadata(md) },
		},
		{
			name: "not found", err: &lock.LockNotFoundError{Key: "k"},
			status: wire.StatusNotFound, reason: lock.ReasonLockNotFound,
			rebuild: func(md map[string]string) any { return lock.LockNotFoundFromMetadata(md) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err.(error)
			if got := ServiceErrorStatus(err); got != tt.status {
				t.Fatalf("ServiceErrorStatus = %s, want %s", wire.StatusName(got), wire.StatusName(tt.status))
			}
			info := errorPayloadOf(err).Info
			if info == nil || info.Domain != transport.RefusalDomain || info.Reason != tt.reason {
				t.Fatalf("error frame info = %+v, want the %s refusal", info, tt.reason)
			}
			if got := tt.rebuild(info.Metadata); !reflect.DeepEqual(got, tt.err) {
				t.Fatalf("metadata rebuilds %#v, want %#v", got, tt.err)
			}
		})
	}
}
