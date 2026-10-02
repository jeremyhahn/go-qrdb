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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

func newSQLConfigQUICServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return NewServer(nil, nil, "localhost:0", WithDatabaseService(servicetest.NewDatabaseService(mgr)))
}

func TestHandleDatabaseSQL_CreateAndUpdate(t *testing.T) {
	srv := newSQLConfigQUICServer(t)
	ctx := context.Background()

	create, _ := json.Marshal(map[string]any{
		"name": "shop", "engine": "kv", "backend": "memory",
		"sql": map[string]string{"concurrency": "pessimistic"},
	})
	resp, err := srv.handleDatabaseCreate(ctx, create)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.SQL.EffectiveConcurrency() != database.ConcurrencyPessimistic {
		t.Fatalf("created sql = %+v, want pessimistic", info.SQL)
	}

	update, _ := json.Marshal(map[string]any{"name": "shop", "sql": map[string]string{"concurrency": "optimistic"}})
	resp, err = srv.handleDatabaseUpdateSQL(ctx, update)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateSQL: %v", err)
	}
	info = database.DatabaseInfo{}
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != database.ConcurrencyOptimistic {
		t.Fatalf("updated sql = %+v, want optimistic", info.SQL)
	}
}

func TestHandleDatabaseSQL_Refusals(t *testing.T) {
	srv := newSQLConfigQUICServer(t)
	ctx := context.Background()
	create, _ := json.Marshal(map[string]any{"name": "shop", "engine": "kv", "backend": "memory"})
	if _, err := srv.handleDatabaseCreate(ctx, create); err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	bad, _ := json.Marshal(map[string]any{"name": "shop", "sql": map[string]string{"concurrency": "eager"}})
	var invalid *database.InvalidConcurrencyError
	if _, err := srv.handleDatabaseUpdateSQL(ctx, bad); !errors.As(err, &invalid) {
		t.Fatalf("eager = %v, want *InvalidConcurrencyError", err)
	}

	noName, _ := json.Marshal(map[string]any{"sql": map[string]string{"concurrency": "pessimistic"}})
	var arg *InvalidArgumentError
	if _, err := srv.handleDatabaseUpdateSQL(ctx, noName); !errors.As(err, &arg) {
		t.Fatalf("no name = %v, want *InvalidArgumentError", err)
	}

	var unmarshal *UnmarshalError
	if _, err := srv.handleDatabaseUpdateSQL(ctx, []byte("not json")); !errors.As(err, &unmarshal) {
		t.Fatalf("bad payload = %v, want *UnmarshalError", err)
	}

	var notConfigured *ServiceNotConfiguredError
	plain := NewServer(nil, nil, "localhost:0")
	if _, err := plain.handleDatabaseUpdateSQL(ctx, bad); !errors.As(err, &notConfigured) {
		t.Fatalf("no service = %v, want *ServiceNotConfiguredError", err)
	}
}
