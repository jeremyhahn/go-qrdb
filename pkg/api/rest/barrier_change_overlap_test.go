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

package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestBarrierHandlers_ChangePassphrase_OverlappingChangeIsConflict drives a
// passphrase change into a barrier on which another change is in flight:
// the real refusal, not an injected one, is a 409 carrying the reason that
// names seal.ErrRootKeyWriteInProgress, so a client can tell it apart and
// retry.
func TestBarrierHandlers_ChangePassphrase_OverlappingChangeIsConflict(t *testing.T) {
	barrier, release := sealtest.PassphraseChangeInFlight(t, "first", "second")
	srv := newBarrierPINTestServer(t, service.NewBarrierService(barrier), newStubPINBackend())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/change-passphrase",
		strings.NewReader(`{"current":"Zmlyc3Q=","next":"dGhpcmQ="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Code != transport.ReasonBarrierRootKeyWriteInProgress {
		t.Fatalf("code = %q, want %q", body.Code, transport.ReasonBarrierRootKeyWriteInProgress)
	}
	if err := transport.RefusalError(body.Code, body.Metadata); !errors.Is(err, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("RefusalError(%q) = %v, want ErrRootKeyWriteInProgress", body.Code, err)
	}
	release()
}
