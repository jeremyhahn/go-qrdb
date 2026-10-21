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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A user update that lost a race is an ABORTED tool error carrying the
// USER_CONCURRENT_UPDATE refusal, so a client rebuilds the retryable conflict
// (go-qrdb#938).
func TestClassifiedToolError_UserConcurrentUpdateCarriesTheRefusal(t *testing.T) {
	err := &user.ConcurrentUpdateError{ID: 7}
	payload, ok := wire.DecodeErrorPayload(classifiedToolError("qrdb_user_update", err).StructuredContent)
	if !ok || payload.Code != wire.CodeAborted || payload.Reason != transport.ReasonUserConcurrentUpdate {
		t.Fatalf("payload = %+v (ok %v), want CodeAborted with the %s refusal", payload, ok, transport.ReasonUserConcurrentUpdate)
	}
	if got := transport.UserConcurrentUpdateFromMetadata(payload.Metadata); got.ID != 7 {
		t.Fatalf("metadata decodes to %+v, want the lost race of user 7", got)
	}
}

// A failed rollback over a lost race carries no refusal: it is DataLoss, not a
// conflict for the client to re-read and retry.
func TestClassifiedToolError_CompensationOverConcurrentUpdateCarriesNoRefusal(t *testing.T) {
	err := &user.IndexCompensationError{
		Op: "update", UserID: 7,
		Cause:    &user.ConcurrentUpdateError{ID: 7},
		Failures: []error{errors.New("restore failed")},
	}
	payload, ok := wire.DecodeErrorPayload(classifiedToolError("qrdb_user_update", err).StructuredContent)
	if !ok || payload.Code == wire.CodeAborted || payload.Reason != "" || payload.Metadata != nil {
		t.Fatalf("payload = %+v (ok %v), want a non-retryable error with no refusal", payload, ok)
	}
}
