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

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// A database's discovery secrets are write-only on the desktop too: what the
// bindings hand the frontend -- serialised as JSON, as Wails does -- carries
// none of them, only that each one is set.
func TestDatabaseBinding_SecretsNeverReadBack(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(svc)))

	encode := func(v any) []byte {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	info, err := b.Get(dbwiretest.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "desktop DatabaseBinding.Get", encode(info))

	infos, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "desktop DatabaseBinding.List", encode(infos))

	updated, err := b.UpdateSQL(dbwiretest.Name, &transport.UpdateDatabaseSQLRequest{SQL: &transport.DatabaseSQLConfig{Concurrency: "optimistic"}})
	if err != nil {
		t.Fatalf("UpdateSQL: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "desktop DatabaseBinding.UpdateSQL", encode(updated))
}
