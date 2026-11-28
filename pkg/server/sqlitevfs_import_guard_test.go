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

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// importGuardServices builds real data-plane services behind the SQLite
// key guard, so an import runs the production BatchService end to end.
func importGuardServices(t *testing.T) (*DatabaseServices, context.Context) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "acme", Subject: "alice"})
	svcs := buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{}, qrdbconfig.IndexConfig{}, qrdbconfig.TimeSeriesConfig{})
	return withSQLiteKeyGuard(svcs), ctx
}

func runGuardedImport(t *testing.T, svcs *DatabaseServices, ctx context.Context, transactional bool, data string) (*service.ImportResult, error) {
	t.Helper()
	sid, err := svcs.Batch.ImportBegin(ctx, service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row", SkipHeader: true, Transactional: transactional})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svcs.Batch.ImportAppend(ctx, sid, 0, []byte(data)); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	return svcs.Batch.ImportCommit(ctx, sid)
}

func assertKeyAbsent(t *testing.T, svcs *DatabaseServices, ctx context.Context, key string) {
	t.Helper()
	if _, err := svcs.KV.Get(ctx, key); err == nil {
		t.Fatalf("Get(%q) found a value; the guarded import wrote it", key)
	}
}

// An import naming a SQLite database's key is refused with the typed
// SQLiteKeyError, row by row, and writes nothing to the database.
func TestSQLiteKeyGuard_ImportOfASQLiteKeyIsRefused(t *testing.T) {
	const data = "id,v\nsqlite/app/meta,x\nplain,y\n"

	t.Run("transactional", func(t *testing.T) {
		svcs, ctx := importGuardServices(t)
		_, err := runGuardedImport(t, svcs, ctx, true, data)
		var aborted *service.ImportTransactionAbortedError
		if !errors.As(err, &aborted) || aborted.Key != "sqlite/app/meta" {
			t.Fatalf("ImportCommit = %v, want an ImportTransactionAbortedError on sqlite/app/meta", err)
		}
		if !strings.Contains(aborted.Message, (&service.SQLiteKeyError{Key: "sqlite/app/meta"}).Error()) {
			t.Fatalf("abort message %q does not carry the SQLiteKeyError", aborted.Message)
		}
		assertKeyAbsent(t, svcs, ctx, "sqlite/app/meta")
		assertKeyAbsent(t, svcs, ctx, "plain")
	})

	t.Run("per-row", func(t *testing.T) {
		svcs, ctx := importGuardServices(t)
		res, err := runGuardedImport(t, svcs, ctx, false, data)
		if err != nil {
			t.Fatalf("ImportCommit: %v", err)
		}
		if res.Failed != 1 || len(res.Errors) != 1 || res.Errors[0].Key != "sqlite/app/meta" {
			t.Fatalf("result = %+v, want exactly the sqlite/app/meta row failed", res)
		}
		assertKeyAbsent(t, svcs, ctx, "sqlite/app/meta")
		if _, err := svcs.KV.Get(ctx, "plain"); err != nil {
			t.Fatalf("Get(plain) = %v; the guard refused an ordinary row", err)
		}
	})
}

// Guards on a context compose: an earlier guard still refuses what it
// refused, and the later one adds its own refusals.
func TestWithImportRowGuard_Composes(t *testing.T) {
	errA := errors.New("a refused")
	svcs, ctx := importGuardServices(t)
	ctx = service.WithImportRowGuard(ctx, func(k string) error {
		if k == "a" {
			return errA
		}
		return nil
	})
	res, err := runGuardedImport(t, svcs, ctx, false, "id,v\na,1\nsqlite/app/p/1,2\nc,3\n")
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if res.Failed != 2 || res.Errors[0].Key != "a" || res.Errors[1].Key != "sqlite/app/p/1" {
		t.Fatalf("result = %+v, want rows a (caller's guard) and sqlite/app/p/1 (SQLite guard) refused", res)
	}
	assertKeyAbsent(t, svcs, ctx, "a")
	if _, err := svcs.KV.Get(ctx, "c"); err != nil {
		t.Fatalf("Get(c) = %v; an ordinary row was refused", err)
	}
}
