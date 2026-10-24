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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A user update that lost a race answers 409 with the USER_CONCURRENT_UPDATE
// refusal, so a client rebuilds the retryable conflict (go-qrdb#938).
func TestWriteErrorJSON_UserConcurrentUpdateCarriesTheRefusal(t *testing.T) {
	err := &user.ConcurrentUpdateError{ID: 7}
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, mapHTTPError(err), err)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Code != transport.ReasonUserConcurrentUpdate || body.Error != err.Error() {
		t.Fatalf("body = %+v; want code %s and the message", body, transport.ReasonUserConcurrentUpdate)
	}
	if got := transport.UserConcurrentUpdateFromMetadata(body.Metadata); got.ID != 7 {
		t.Fatalf("metadata decodes to %+v, want the lost race of user 7", got)
	}
}

// A failed rollback over a lost race stays a 500 with no refusal: telling the
// client to re-read and retry would hide a possibly inconsistent index.
func TestWriteErrorJSON_CompensationOverConcurrentUpdateCarriesNoRefusal(t *testing.T) {
	err := &user.IndexCompensationError{
		Op: "update", UserID: 7,
		Cause:    &user.ConcurrentUpdateError{ID: 7},
		Failures: []error{errors.New("restore failed")},
	}
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, mapHTTPError(err), err)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := decodeErrorBody(t, rec); body.Code != "" || body.Metadata != nil {
		t.Fatalf("body = %+v; want no refusal", body)
	}
}
