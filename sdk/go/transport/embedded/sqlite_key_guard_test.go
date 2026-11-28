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
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sqliteKVGuardTransport builds an embedded transport over real KV, batch
// and transaction services on one in-memory host, the way the standalone
// server and the node wire the embedded default database.
func sqliteKVGuardTransport(t *testing.T) (*Transport, service.KVServicer) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	kv := service.NewKVService(host, 1)
	tr, err := New(kv, &mockAdminService{},
		WithBatchService(service.NewBatchService(host, 1)),
		WithTransactionService(service.NewTransactionService(host, 1)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr, kv
}

func sqlitePageKey(t *testing.T) string {
	t.Helper()
	key, err := sqlitevfs.PageKey("app", 1)
	if err != nil {
		t.Fatalf("PageKey: %v", err)
	}
	return string(key)
}

func assertSQLiteKeyRefused(t *testing.T, op string, err error, key string) {
	t.Helper()
	want := (&service.SQLiteKeyError{Key: key}).Error()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s = %v, want a refusal carrying %q", op, err, want)
	}
}

// TestEmbeddedKVWriteOfASQLiteKeyIsRefused: the embedded default database
// calls the KV and batch services directly, outside the server registry
// that guards the network transports, so a plain Put, Delete or batch put
// of a SQLite page key is refused and writes nothing, while other keys and
// the gated transaction path still write.
func TestEmbeddedKVWriteOfASQLiteKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	page := sqlitePageKey(t)

	t.Run("put", func(t *testing.T) {
		tr, kv := sqliteKVGuardTransport(t)
		assertSQLiteKeyRefused(t, "Put", tr.Put(ctx, page, []byte("x")), page)
		assertEmbeddedKeyAbsent(t, kv, page)
	})

	t.Run("delete", func(t *testing.T) {
		tr, kv := sqliteKVGuardTransport(t)
		if err := kv.Put(ctx, page, []byte("x")); err != nil {
			t.Fatalf("seed Put: %v", err)
		}
		assertSQLiteKeyRefused(t, "Delete", tr.Delete(ctx, page), page)
		if _, err := kv.Get(ctx, page); err != nil {
			t.Fatalf("Get(%q) = %v; the refused Delete removed the page", page, err)
		}
	})

	t.Run("batch put", func(t *testing.T) {
		tr, kv := sqliteKVGuardTransport(t)
		_, err := tr.ExecuteBatch(ctx, []transport.Op{
			{Type: transport.OpPut, Key: []byte("plain"), Value: []byte("y")},
			{Type: transport.OpPut, Key: []byte(page), Value: []byte("x")},
		})
		assertSQLiteKeyRefused(t, "ExecuteBatch", err, page)
		assertEmbeddedKeyAbsent(t, kv, page)
		assertEmbeddedKeyAbsent(t, kv, "plain")
	})

	t.Run("other key written", func(t *testing.T) {
		tr, kv := sqliteKVGuardTransport(t)
		if err := tr.Put(ctx, "sqlite-not-a-db", []byte("y")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if _, err := tr.ExecuteBatch(ctx, []transport.Op{{Type: transport.OpPut, Key: []byte("plain"), Value: []byte("y")}}); err != nil {
			t.Fatalf("ExecuteBatch: %v", err)
		}
		for _, key := range []string{"sqlite-not-a-db", "plain"} {
			if _, err := kv.Get(ctx, key); err != nil {
				t.Fatalf("Get(%q) = %v; the guard refused an ordinary key", key, err)
			}
		}
	})

	t.Run("transaction writes", func(t *testing.T) {
		tr, kv := sqliteKVGuardTransport(t)
		resp, err := tr.ExecuteTransaction(ctx, &transport.TransactionRequest{
			SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte(page), Value: []byte("x")}},
		})
		if err != nil || !resp.Succeeded {
			t.Fatalf("ExecuteTransaction = %+v, %v; want the gated path to write", resp, err)
		}
		if _, err := kv.Get(ctx, page); err != nil {
			t.Fatalf("Get(%q) = %v after the transaction", page, err)
		}
	})
}
