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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A database's discovery secrets are write-only: no read of the database
// returns them, only that each one is set.
func TestDatabaseSecrets_NeverReadBack(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	dbwiretest.ClaimShards(t, mgr)
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithDatabaseService(svc))

	reads := []struct {
		what   string
		method string
		path   string
		body   string
	}{
		{"GET /v1/databases/{name}", http.MethodGet, "/v1/databases/" + dbwiretest.Name, ""},
		{"GET /v1/databases/list", http.MethodGet, "/v1/databases/list", ""},
		{"PATCH /v1/databases/{name}/sql", http.MethodPatch, "/v1/databases/" + dbwiretest.Name + "/sql", `{"sql":{"concurrency":"optimistic"}}`},
		// The update that sets the secrets does not echo them either.
		{"PATCH /v1/databases/{name}/raft", http.MethodPatch, "/v1/databases/" + dbwiretest.Name + "/raft", `{
			"clusterMode": "distributed", "replicaId": 1, "discovery": "static",
			"static": {"peers": ["db.example.com:63001"], "sharedSecret": "` + dbwiretest.StaticSecret + `"},
			"multicast": {"sharedSecret": "` + dbwiretest.MulticastSecret + `", "multicastGroup": "239.255.0.1:63100"},
			"token": {"endpoint": "https://db.example.com/discovery", "token": "` + dbwiretest.DiscoveryToken + `", "clusterSize": 3}
		}`},
	}
	for _, r := range reads {
		t.Run(r.what, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.dispatch(rec, httptest.NewRequest(r.method, r.path, strings.NewReader(r.body)))
			assertStatus(t, rec, http.StatusOK)
			dbwiretest.AssertNoSecrets(t, "REST "+r.what, rec.Body.Bytes())
		})
	}
}
