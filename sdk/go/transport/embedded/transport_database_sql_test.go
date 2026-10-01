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
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedDatabase_SQLConfig_RealService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	tr := newExtTransport(t, WithDatabaseService(service.NewDatabaseService(mgr)))
	ctx := context.Background()

	created, err := tr.CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: "shop", Engine: "kv", Backend: "memory",
		SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if created.SQL == nil || created.SQL.Concurrency != "pessimistic" {
		t.Fatalf("created SQL = %+v, want pessimistic", created.SQL)
	}

	updated, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{
		Name: "shop", SQL: &transport.DatabaseSQLConfig{Concurrency: "optimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	if updated.SQL == nil || updated.SQL.Concurrency != "optimistic" {
		t.Fatalf("updated SQL = %+v, want optimistic", updated.SQL)
	}

	var invalid *database.InvalidConcurrencyError
	if _, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{
		Name: "shop", SQL: &transport.DatabaseSQLConfig{Concurrency: "eager"},
	}); !errors.As(err, &invalid) {
		t.Fatalf("eager = %v, want *InvalidConcurrencyError", err)
	}
}

func TestEmbeddedDatabase_UpdateSQL_NilService(t *testing.T) {
	tr := newExtTransport(t)
	var nilSvc *NilServiceError
	if _, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "db"}); !errors.As(err, &nilSvc) {
		t.Fatalf("nil service = %v, want *NilServiceError", err)
	}
}
