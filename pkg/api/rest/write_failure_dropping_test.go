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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// A KV write the state machine refused because the database's drop fence
// stood at its log position -- a write through a node that still resolved a
// database another node dropped -- is 409 with the drop named, like every
// other refusal of a database being dropped, not a 500 "propose failed".
func TestWriteProposeFailure_DroppingIsConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	writeProposeFailure(rec, &database.DroppingError{Name: "orders", TenantID: "acme"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Error != (&database.DroppingError{Name: "orders", TenantID: "acme"}).Error() {
		t.Fatalf("error = %q, want the drop refusal itself", body.Error)
	}
}
