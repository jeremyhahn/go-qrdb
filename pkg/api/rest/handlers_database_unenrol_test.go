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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// POST /v1/databases/{name}/barrier/officers/unenrol removes a holder before
// the database's custody is initialized and answers 204; its slot is free
// for another holder. It refuses a holder never enrolled (404), one of an
// initialized database (409, revoke instead), and a database's N+1th holder
// is refused before it (409).
func TestDatabaseBarrierOfficerUnenrol_REST(t *testing.T) {
	srv := newDBHolderServer(t)
	register := func(h *restDBHolder) int {
		return postJSON(t, srv, "/v1/databases/vault/barrier/officers", transport.OfficerRegisterRequest{Username: h.name, CSRPEM: h.csr}, nil).Code
	}
	alice, bob, carol := newRESTDBHolder(t, "alice"), newRESTDBHolder(t, "bob"), newRESTDBHolder(t, "carol")
	for _, h := range []*restDBHolder{alice, bob} {
		if code := register(h); code != http.StatusOK {
			t.Fatalf("register %s = %d", h.name, code)
		}
	}
	if code := register(carol); code != http.StatusConflict {
		t.Fatalf("a third holder of a 2-of-2 database = %d, want 409", code)
	}

	unenrol := func(username string) int {
		return postJSON(t, srv, "/v1/databases/vault/barrier/officers/unenrol", map[string]string{"username": username}, nil).Code
	}
	if code := unenrol("bob"); code != http.StatusNoContent {
		t.Fatalf("unenrol bob = %d, want 204", code)
	}
	if code := unenrol("bob"); code != http.StatusNotFound {
		t.Fatalf("unenrol bob again = %d, want 404", code)
	}
	if code := register(carol); code != http.StatusOK {
		t.Fatalf("carol takes bob's slot = %d", code)
	}
	if rec := postJSON(t, srv, "/v1/databases/vault/barrier/shamir/initialize", map[string]string{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("initialize = %d: %s", rec.Code, rec.Body.String())
	}
	if code := unenrol("carol"); code != http.StatusConflict {
		t.Fatalf("unenrol after initialization = %d, want 409", code)
	}
	if code := unenrol(""); code != http.StatusBadRequest {
		t.Fatalf("unenrol without a username = %d, want 400", code)
	}
}
