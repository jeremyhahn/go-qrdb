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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newSQLConfigServer serves the database API over a REAL DatabaseService on
// a temporary catalog, so validation and persistence are the production
// code paths.
func newSQLConfigServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(service.NewDatabaseService(mgr)))
}

func decodeDatabaseInfo(t *testing.T, rec *httptest.ResponseRecorder) databaseInfoResponse {
	t.Helper()
	var info databaseInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return info
}

func TestDatabaseSQL_CreateAndUpdate(t *testing.T) {
	s := newSQLConfigServer(t)

	create := httptest.NewRequest(http.MethodPost, "/v1/databases",
		strings.NewReader(`{"name":"shop","engine":"kv","backend":"memory","sql":{"concurrency":"pessimistic"}}`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, create)
	assertStatus(t, rec, http.StatusCreated)
	if info := decodeDatabaseInfo(t, rec); info.SQL == nil || info.SQL.Concurrency != "pessimistic" {
		t.Fatalf("created sql = %+v, want pessimistic", info.SQL)
	}

	update := httptest.NewRequest(http.MethodPatch, "/v1/databases/shop/sql",
		strings.NewReader(`{"sql":{"concurrency":"optimistic"}}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, update)
	assertStatus(t, rec, http.StatusOK)
	if info := decodeDatabaseInfo(t, rec); info.SQL == nil || info.SQL.Concurrency != "optimistic" {
		t.Fatalf("updated sql = %+v, want optimistic", info.SQL)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/databases/shop", nil)
	rec = httptest.NewRecorder()
	s.dispatch(rec, get)
	assertStatus(t, rec, http.StatusOK)
	if info := decodeDatabaseInfo(t, rec); info.SQL == nil || info.SQL.Concurrency != "optimistic" {
		t.Fatalf("read sql = %+v, want optimistic", info.SQL)
	}
}

func TestDatabaseSQL_Refusals(t *testing.T) {
	s := newSQLConfigServer(t)
	create := httptest.NewRequest(http.MethodPost, "/v1/databases",
		strings.NewReader(`{"name":"shop","engine":"kv","backend":"memory"}`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, create)
	assertStatus(t, rec, http.StatusCreated)

	cases := []struct {
		path, body string
		want       int
	}{
		{"/v1/databases/shop/sql", `{"sql":{"concurrency":"eager"}}`, http.StatusBadRequest},
		{"/v1/databases/shop/sql", `not json`, http.StatusBadRequest},
		{"/v1/databases/missing/sql", `{"sql":{"concurrency":"pessimistic"}}`, http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPatch, c.path, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		s.dispatch(rec, req)
		assertStatus(t, rec, c.want)
	}

	bad := httptest.NewRequest(http.MethodPost, "/v1/databases",
		strings.NewReader(`{"name":"bad","engine":"kv","backend":"memory","sql":{"concurrency":"eager"}}`))
	rec = httptest.NewRecorder()
	s.dispatch(rec, bad)
	assertStatus(t, rec, http.StatusBadRequest)
}
