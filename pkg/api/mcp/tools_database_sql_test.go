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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func newRealDatabaseService(t *testing.T) service.DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return service.NewDatabaseService(mgr)
}

func TestDatabaseSQLTools_CreateAndUpdate(t *testing.T) {
	svc := newRealDatabaseService(t)
	ctx := context.Background()

	result, err := handleDatabaseCreate(svc)(ctx, buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "shop", "engine": "kv", "backend": "memory", "sql_concurrency": "pessimistic",
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.SQL.EffectiveConcurrency() != database.ConcurrencyPessimistic {
		t.Fatalf("created sql = %+v, want pessimistic", info.SQL)
	}

	result, err = handleDatabaseUpdateSQL(svc)(ctx, buildCallToolRequest(t, "qrdb_database_update_sql", map[string]any{
		"name": "shop", "concurrency": "optimistic",
	}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	info = database.DatabaseInfo{}
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != database.ConcurrencyOptimistic {
		t.Fatalf("updated sql = %+v, want optimistic", info.SQL)
	}
}

// An update without a concurrency restores the defaults, as an absent sql
// does on REST, gRPC, QUIC and the Unix socket: there must be a way back to
// "unset" on every transport.
func TestDatabaseSQLTools_UpdateWithoutConcurrencyRestoresDefaults(t *testing.T) {
	svc := newRealDatabaseService(t)
	ctx := context.Background()
	if _, err := handleDatabaseCreate(svc)(ctx, buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "shop", "engine": "kv", "backend": "memory", "sql_concurrency": "pessimistic",
	})); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, required := range toolDatabaseUpdateSQL().InputSchema.Required {
		if required == "concurrency" {
			t.Fatal("concurrency is still a required argument")
		}
	}

	result, err := handleDatabaseUpdateSQL(svc)(ctx, buildCallToolRequest(t, "qrdb_database_update_sql", map[string]any{
		"name": "shop",
	}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.IsError {
		t.Fatalf("update without concurrency refused: %s", resultText(t, result))
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.SQL != nil {
		t.Fatalf("sql = %+v, want the defaults (unset)", info.SQL)
	}
	if got := info.SQL.EffectiveConcurrency(); got != database.ConcurrencyOptimistic {
		t.Fatalf("effective concurrency = %s, want optimistic", got)
	}
}

func TestDatabaseSQLTools_Refusals(t *testing.T) {
	svc := newRealDatabaseService(t)
	ctx := context.Background()
	handler := handleDatabaseUpdateSQL(svc)

	for _, args := range []map[string]any{
		{"concurrency": "pessimistic"},
	} {
		result, err := handler(ctx, buildCallToolRequest(t, "qrdb_database_update_sql", args))
		if err != nil {
			t.Fatalf("handler(%v) error = %v, want a tool-result error", args, err)
		}
		if !result.IsError {
			t.Fatalf("handler(%v) succeeded, want a missing-argument error", args)
		}
	}

	_, err := handler(ctx, buildCallToolRequest(t, "qrdb_database_update_sql", map[string]any{
		"name": "missing", "concurrency": "pessimistic",
	}))
	var notFound *database.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("missing database = %v, want *database.NotFoundError", err)
	}
	if tool := toolDatabaseUpdateSQL(); tool.Name != "qrdb_database_update_sql" {
		t.Fatalf("tool name = %q", tool.Name)
	}
}
