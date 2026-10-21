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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A user update that lost a race is an ABORTED frame carrying the
// USER_CONCURRENT_UPDATE refusal, so a client rebuilds the retryable conflict
// (go-qrdb#938).
func TestErrorPayloadOf_UserConcurrentUpdateCarriesTheRefusal(t *testing.T) {
	err := &user.ConcurrentUpdateError{ID: 7}
	if got := ServiceErrorStatus(err); got != wire.StatusAborted {
		t.Fatalf("ServiceErrorStatus = %s, want StatusAborted", wire.StatusName(got))
	}
	info := errorPayloadOf(err).Info
	if info == nil || info.Domain != replicaforward.ErrorDomain || info.Reason != transport.ReasonUserConcurrentUpdate {
		t.Fatalf("error frame info = %+v, want the %s refusal", info, transport.ReasonUserConcurrentUpdate)
	}
	if got := transport.UserConcurrentUpdateFromMetadata(info.Metadata); got.ID != 7 {
		t.Fatalf("metadata decodes to %+v, want the lost race of user 7", got)
	}
}

// A failed rollback over a lost race carries no refusal: it is DataLoss, not a
// conflict for the client to re-read and retry.
func TestErrorPayloadOf_CompensationOverConcurrentUpdateCarriesNoRefusal(t *testing.T) {
	err := &user.IndexCompensationError{
		Op: "update", UserID: 7,
		Cause:    &user.ConcurrentUpdateError{ID: 7},
		Failures: []error{errors.New("restore failed")},
	}
	if info := errorPayloadOf(err).Info; info != nil {
		t.Fatalf("error frame info = %+v, want none", info)
	}
}
