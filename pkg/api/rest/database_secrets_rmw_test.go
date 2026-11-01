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

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A client cannot read a secret back, so a read-modify-write of the Raft
// settings sends none: the stored secrets are kept. Leaving a discovery block
// out removes it, secret and all.
func TestDatabaseSecrets_ReadModifyWriteKeepsThem(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithDatabaseService(svc))

	rec := httptest.NewRecorder()
	s.dispatch(rec, httptest.NewRequest(http.MethodGet, "/v1/databases/"+dbwiretest.Name, nil))
	assertStatus(t, rec, http.StatusOK)
	var info struct {
		Raft map[string]any `json:"raft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}

	// Write the settings back unchanged except for the discovery method, which
	// now needs the multicast secret the client does not have, and with token
	// discovery disabled.
	raft := info.Raft
	raft["discovery"] = "multicast"
	delete(raft, "token")
	body, err := json.Marshal(raft)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.dispatch(rec, httptest.NewRequest(http.MethodPatch, "/v1/databases/"+dbwiretest.Name+"/raft",
		strings.NewReader(string(body))))
	assertStatus(t, rec, http.StatusOK)

	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raft.Static.SharedSecret != dbwiretest.StaticSecret ||
		stored.Raft.Multicast.SharedSecret != dbwiretest.MulticastSecret {
		t.Fatalf("stored secrets after a read-modify-write = %q/%q, want them kept",
			stored.Raft.Static.SharedSecret, stored.Raft.Multicast.SharedSecret)
	}
	if stored.Raft.Token != nil {
		t.Fatalf("token discovery = %+v, want it removed", stored.Raft.Token)
	}
}
