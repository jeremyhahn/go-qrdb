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

package rest

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// TestWriteErrorJSON_LockOutcomesCarryTheRefusal pins that each typed lock
// outcome leaves the REST server with its status, its reason and metadata
// that rebuild the error -- key, holder and tokens included.
func TestWriteErrorJSON_LockOutcomesCarryTheRefusal(t *testing.T) {
	tests := []struct {
		name    string
		err     interface{ Metadata() map[string]string }
		status  int
		reason  string
		rebuild func(map[string]string) any
	}{
		{
			name: "held", err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			status: http.StatusConflict, reason: lock.ReasonLockHeld,
			rebuild: func(md map[string]string) any { return lock.LockHeldFromMetadata(md) },
		},
		{
			name: "not holder", err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			status: http.StatusForbidden, reason: lock.ReasonNotLockHolder,
			rebuild: func(md map[string]string) any { return lock.NotLockHolderFromMetadata(md) },
		},
		{
			name: "not found", err: &lock.LockNotFoundError{Key: "k"},
			status: http.StatusNotFound, reason: lock.ReasonLockNotFound,
			rebuild: func(md map[string]string) any { return lock.LockNotFoundFromMetadata(md) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err.(error)
			rec := httptest.NewRecorder()
			writeErrorJSON(rec, mapHTTPError(err), err)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			body := decodeErrorBody(t, rec)
			if body.Code != tt.reason || body.Error != err.Error() {
				t.Fatalf("body = %+v; want code %s and the message", body, tt.reason)
			}
			if got := tt.rebuild(body.Metadata); !reflect.DeepEqual(got, tt.err) {
				t.Fatalf("metadata rebuilds %#v, want %#v", got, tt.err)
			}
		})
	}
}
