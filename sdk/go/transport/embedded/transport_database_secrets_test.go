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

package embedded

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A database's discovery secrets are write-only in-process too: what the
// embedded transport returns -- and what the desktop bindings serialise to
// the frontend, since they return these values unchanged -- carries none of
// them, only that each one is set.
func TestEmbeddedDatabase_SecretsNeverReadBack(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	dbwiretest.ClaimShards(t, mgr)
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	tr := newExtTransport(t, WithDatabaseService(svc))
	ctx := context.Background()

	encode := func(v any) []byte {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	info, err := tr.GetDatabase(ctx, dbwiretest.Name)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "embedded GetDatabase", encode(info))

	infos, err := tr.ListDatabases(ctx)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "embedded ListDatabases", encode(infos))

	updated, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{
		Name: dbwiretest.Name, SQL: &transport.DatabaseSQLConfig{Concurrency: "optimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "embedded UpdateDatabaseSQL", encode(updated))
}
