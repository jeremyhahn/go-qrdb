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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func unencodableKeyError() error {
	return &service.EncodeRequestError{
		Err: &protocol.FieldTooLongError{Field: "key", Length: 70000, Max: protocol.MaxUint16Field},
	}
}

// A request the protocol cannot encode never reached the cluster, so it is 400
// and not 500. Reported as 500 it fires an operational alert for a healthy
// cluster, and a client that retries on 5xx retries an input that can never
// succeed (go-qrdb#52).
func TestMapHTTPError_EncodeRequestErrorIsBadRequest(t *testing.T) {
	if got := mapHTTPError(unencodableKeyError()); got != http.StatusBadRequest {
		t.Fatalf("mapHTTPError = %d, want %d", got, http.StatusBadRequest)
	}
}

// The write path chooses its status through mapHTTPError rather than hardcoding
// 500, and the body must agree with the status: a 400 must NOT claim a proposal
// failed, because none was made.
func TestWriteProposeFailure_ClassifiesTheStatusAndTheMessage(t *testing.T) {
	t.Run("an unencodable request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeProposeFailure(rec, unencodableKeyError())

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the error body: %v", err)
		}
		if strings.Contains(body.Error, "propose failed") {
			t.Fatalf("body = %q; a 400 must not report a proposal failure, because "+
				"no proposal was made", body.Error)
		}
	})

	t.Run("a genuine proposal failure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeProposeFailure(rec, errors.New("raft is down"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d — an unrecognised failure on the write "+
				"path is still the cluster's", rec.Code, http.StatusInternalServerError)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the error body: %v", err)
		}
		if !strings.Contains(body.Error, "propose failed") {
			t.Fatalf("body = %q, want it to report the proposal failure", body.Error)
		}
	})
}
