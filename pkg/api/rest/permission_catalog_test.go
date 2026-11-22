// Copyright 2026 Jeremy Hahn
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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestHandleListPermissionCatalog_MatchesRegistry(t *testing.T) {
	want, err := transport.PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	rec := httptest.NewRecorder()
	(&Server{}).handleListPermissionCatalog(rec, httptest.NewRequest(http.MethodGet, "/permissions/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got permissionCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Permissions) != len(want) {
		t.Fatalf("got %d, want %d", len(got.Permissions), len(want))
	}
	for i := range want {
		if got.Permissions[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got.Permissions[i], want[i])
		}
	}
}
