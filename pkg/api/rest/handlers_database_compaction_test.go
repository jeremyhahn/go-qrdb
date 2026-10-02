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
	"strings"
	"testing"
)

// The compaction policy is set by its own verb over a real DatabaseService,
// read back with the database, refused when invalid, and turned off by an
// absent policy. Create does not take one.
func TestDatabaseCompaction_UpdateReadAndClear(t *testing.T) {
	s := newSQLConfigServer(t)

	create := httptest.NewRequest(http.MethodPost, "/v1/databases",
		strings.NewReader(`{"name":"events","engine":"kv","backend":"memory","compaction":{"mode":"revision","retainRevisions":5}}`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, create)
	assertStatus(t, rec, http.StatusCreated)
	if info := decodeDatabaseInfo(t, rec); info.Compaction != nil {
		t.Fatalf("create took a compaction policy: %+v", info.Compaction)
	}

	update := httptest.NewRequest(http.MethodPatch, "/v1/databases/events/compaction",
		strings.NewReader(`{"compaction":{"mode":"period","retainPeriod":"24h"}}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, update)
	assertStatus(t, rec, http.StatusOK)
	if info := decodeDatabaseInfo(t, rec); info.Compaction == nil || info.Compaction.Mode != "period" || info.Compaction.RetainPeriod != "24h" {
		t.Fatalf("updated compaction = %+v", info.Compaction)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/databases/events", nil)
	rec = httptest.NewRecorder()
	s.dispatch(rec, get)
	assertStatus(t, rec, http.StatusOK)
	if info := decodeDatabaseInfo(t, rec); info.Compaction == nil || info.Compaction.Mode != "period" {
		t.Fatalf("read compaction = %+v", info.Compaction)
	}

	invalid := httptest.NewRequest(http.MethodPatch, "/v1/databases/events/compaction",
		strings.NewReader(`{"compaction":{"mode":"revision"}}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, invalid)
	assertStatus(t, rec, http.StatusBadRequest)

	clear := httptest.NewRequest(http.MethodPatch, "/v1/databases/events/compaction", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, clear)
	assertStatus(t, rec, http.StatusOK)
	if info := decodeDatabaseInfo(t, rec); info.Compaction != nil {
		t.Fatalf("an absent policy did not turn it off: %+v", info.Compaction)
	}
}

func TestDatabaseCompaction_RefusesBadRequests(t *testing.T) {
	s := newSQLConfigServer(t)

	malformed := httptest.NewRequest(http.MethodPatch, "/v1/databases/events/compaction", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, malformed)
	assertStatus(t, rec, http.StatusBadRequest)

	missing := httptest.NewRequest(http.MethodPatch, "/v1/databases/nope/compaction",
		strings.NewReader(`{"compaction":{"mode":"revision","retainRevisions":1}}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, missing)
	assertStatus(t, rec, http.StatusNotFound)
}
