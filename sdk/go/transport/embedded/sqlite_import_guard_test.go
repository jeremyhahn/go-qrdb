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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sqliteGuardTransport builds an embedded transport over a real
// BatchService and KVService on one in-memory host, the way the standalone
// server and the node wire the embedded default database.
func sqliteGuardTransport(t *testing.T) (*Transport, service.KVServicer) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	kv := service.NewKVService(host, 1)
	tr, err := New(kv, &mockAdminService{}, WithBatchService(service.NewBatchService(host, 1)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr, kv
}

func runEmbeddedImport(t *testing.T, tr *Transport, transactional bool, data string) (*transport.ImportResult, error) {
	t.Helper()
	ctx := context.Background()
	sid, err := tr.ImportBegin(ctx, transport.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row", SkipHeader: true, Transactional: transactional})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := tr.ImportAppend(ctx, sid, 0, []byte(data)); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	return tr.ImportCommit(ctx, sid)
}

func assertEmbeddedKeyAbsent(t *testing.T, kv service.KVServicer, key string) {
	t.Helper()
	if _, err := kv.Get(context.Background(), key); err == nil {
		t.Fatalf("Get(%q) found a value; the embedded import wrote it", key)
	}
}

// TestEmbeddedImportOfASQLiteKeyIsRefused: the embedded default database
// calls the batch service directly, outside the server registry that
// guards the network transports, so ImportCommit itself must refuse a row
// keyed into a SQLite VFS database and write nothing for it.
func TestEmbeddedImportOfASQLiteKeyIsRefused(t *testing.T) {
	const data = "id,v\nsqlite/app/meta,x\nplain,y\n"
	sqliteErr := (&service.SQLiteKeyError{Key: "sqlite/app/meta"}).Error()

	t.Run("transactional", func(t *testing.T) {
		tr, kv := sqliteGuardTransport(t)
		_, err := runEmbeddedImport(t, tr, true, data)
		if err == nil || !strings.Contains(err.Error(), sqliteErr) {
			t.Fatalf("ImportCommit = %v, want an abort carrying %q", err, sqliteErr)
		}
		assertEmbeddedKeyAbsent(t, kv, "sqlite/app/meta")
		assertEmbeddedKeyAbsent(t, kv, "plain")
	})

	t.Run("per-row", func(t *testing.T) {
		tr, kv := sqliteGuardTransport(t)
		res, err := runEmbeddedImport(t, tr, false, data)
		if err != nil {
			t.Fatalf("ImportCommit: %v", err)
		}
		if res.Failed != 1 || len(res.Errors) != 1 || res.Errors[0].Key != "sqlite/app/meta" {
			t.Fatalf("result = %+v, want exactly the sqlite/app/meta row failed", res)
		}
		assertEmbeddedKeyAbsent(t, kv, "sqlite/app/meta")
		if _, err := kv.Get(context.Background(), "plain"); err != nil {
			t.Fatalf("Get(plain) = %v; the guard refused an ordinary row", err)
		}
	})
}
