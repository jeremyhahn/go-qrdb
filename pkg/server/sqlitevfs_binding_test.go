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

// A stock SQLite application (database/sql over modernc.org/sqlite) on the
// tier 1 binding, VFS qrdb, over the page stores the server's services back
// (row #926): CRUD, a second holder's writer refused with SQLITE_BUSY until
// the first commits, a VACUUM published through the stage prefix move, a
// commit past the tenant's quota refused with SQLITE_FULL, and two tenants'
// databases of the same name kept apart, on the prefix, volume and raft
// isolation types.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs/moderncvfs"
)

// bindingStoreParam is the URI parameter naming the prepared page store a
// connection opens.
const bindingStoreParam = "qrdb_test_store"

// bindingStores holds the page stores the test's Opener hands out, by the
// bindingStoreParam value.
type bindingStores struct{ m sync.Map }

func (s *bindingStores) open(_ context.Context, _ string, params url.Values) (*sqlitevfs.Store, error) {
	key := params.Get(bindingStoreParam)
	v, ok := s.m.Load(key)
	if !ok {
		return nil, fmt.Errorf("no page store %q", key)
	}
	return v.(*sqlitevfs.Store), nil
}

// bindingOpen opens a database/sql handle with one connection on VFS qrdb
// over store, registered under key.
func bindingOpen(t *testing.T, stores *bindingStores, key string, store *sqlitevfs.Store) *sql.DB {
	t.Helper()
	stores.m.Store(key, store)
	db, err := sql.Open("sqlite", "file:"+wiringDB+"?vfs="+moderncvfs.VFSName+"&"+bindingStoreParam+"="+url.QueryEscape(key))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func bindingExec(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, q string,
) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// bindingState returns the row count and sum of t's ids, and checks the
// database's integrity.
func bindingState(t *testing.T, db *sql.DB) (n, sum int64) {
	t.Helper()
	if err := db.QueryRow("select count(*), coalesce(sum(id), 0) from t").Scan(&n, &sum); err != nil {
		t.Fatalf("select from t: %v", err)
	}
	var check string
	if err := db.QueryRow("pragma integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	return n, sum
}

// bindingCode is the SQLite result code of err (the primary code in its
// low byte), -1 if err is not a *sqlite.Error.
func bindingCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() & 0xff
	}
	return -1
}

func TestSQLiteVFSBinding_StockSQLiteOverServices(t *testing.T) {
	stores := &bindingStores{}
	if err := moderncvfs.Register(stores.open); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		if err := moderncvfs.Unregister(); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	})
	for name, scene := range lockScenesWith {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			q := newQuotaScene(t, scene)
			a := q.store(t, lockTenantA, lockHolderA)
			dbA := bindingOpen(t, stores, name+"-a-1", a.store)
			dbA2 := bindingOpen(t, stores, name+"-a-2", q.store(t, lockTenantA, lockHolderB).store)
			dbB := bindingOpen(t, stores, name+"-b-1", q.store(t, lockTenantB, lockHolderB).store)

			// CRUD.
			bindingExec(t, dbA, "create table t(id integer primary key, v blob)")
			bindingExec(t, dbA, "insert into t(id, v) values(1, randomblob(300)), (2, randomblob(300)), (3, randomblob(300))")
			bindingExec(t, dbA, "update t set v = randomblob(200) where id = 2")
			bindingExec(t, dbA, "delete from t where id = 3")
			if n, sum := bindingState(t, dbA); n != 2 || sum != 3 {
				t.Fatalf("tenant A rows = %d (ids sum %d), want 2 (3)", n, sum)
			}

			// A second holder's writer is SQLITE_BUSY until the first commits.
			conn, err := dbA.Conn(ctx)
			if err != nil {
				t.Fatalf("Conn: %v", err)
			}
			bindingExec(t, conn2db{ctx, conn}, "begin immediate")
			bindingExec(t, conn2db{ctx, conn}, "insert into t(id, v) values(10, randomblob(300))")
			if _, err := dbA2.Exec("insert into t(id, v) values(20, randomblob(300))"); bindingCode(err) != sqlite3.SQLITE_BUSY {
				t.Fatalf("second holder's writer err = %v (code %d), want SQLITE_BUSY", err, bindingCode(err))
			}
			bindingExec(t, conn2db{ctx, conn}, "commit")
			if err := conn.Close(); err != nil {
				t.Fatalf("Conn.Close: %v", err)
			}
			bindingExec(t, dbA2, "insert into t(id, v) values(20, randomblob(300))")
			if n, sum := bindingState(t, dbA); n != 4 || sum != 33 {
				t.Fatalf("tenant A rows after both writers = %d (ids sum %d), want 4 (33)", n, sum)
			}

			// VACUUM rewrites every page, above the stage threshold: one
			// staged publish through the prefix move, no stage key left.
			bindingExec(t, dbA, "vacuum")
			if n, sum := bindingState(t, dbA); n != 4 || sum != 33 {
				t.Fatalf("tenant A rows after vacuum = %d (ids sum %d), want 4 (33)", n, sum)
			}
			now, err := a.backend.Revision(ctx)
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			stageRoot, err := sqlitevfs.StageRoot(wiringDB)
			if err != nil {
				t.Fatalf("StageRoot: %v", err)
			}
			if live, err := a.backend.Keys(ctx, stageRoot, now); err != nil || len(live) != 0 {
				t.Fatalf("live stage keys after vacuum = %q, %v, want none", live, err)
			}

			// Tenant B's database of the same name is its own.
			if n, err := dbB.Exec("select count(*) from t"); err == nil {
				t.Fatalf("tenant B sees tenant A's table t (%v)", n)
			}
			bindingExec(t, dbB, "create table t(id integer primary key, v blob)")
			bindingExec(t, dbB, "insert into t(id, v) values(100, randomblob(50))")
			if n, sum := bindingState(t, dbB); n != 1 || sum != 100 {
				t.Fatalf("tenant B rows = %d (ids sum %d), want 1 (100)", n, sum)
			}
			if n, sum := bindingState(t, dbA); n != 4 || sum != 33 {
				t.Fatalf("tenant A rows beside tenant B = %d (ids sum %d), want 4 (33)", n, sum)
			}

			// A commit past tenant A's quota is SQLITE_FULL; the database
			// stays readable and unchanged. The services read the limit
			// when they are resolved, so the connection opens after it.
			q.ts.limit.Store(q.acct.Usage(lockTenantA) + wiringPageSize)
			dbFull := bindingOpen(t, stores, name+"-a-3", q.store(t, lockTenantA, lockHolderA).store)
			if _, err := dbFull.Exec("insert into t(id, v) values(30, randomblob(20000))"); bindingCode(err) != sqlite3.SQLITE_FULL {
				t.Fatalf("commit past the quota err = %v (code %d), want SQLITE_FULL", err, bindingCode(err))
			}
			for _, db := range []*sql.DB{dbFull, dbA} {
				if n, sum := bindingState(t, db); n != 4 || sum != 33 {
					t.Fatalf("tenant A rows after SQLITE_FULL = %d (ids sum %d), want 4 (33)", n, sum)
				}
			}
			// The writer lock is free again: the first holder commits a
			// row small enough for its own (unlimited) services.
			bindingExec(t, dbA, "insert into t(id, v) values(30, randomblob(100))")
			if n, sum := bindingState(t, dbFull); n != 5 || sum != 63 {
				t.Fatalf("tenant A rows after the refused commit = %d (ids sum %d), want 5 (63)", n, sum)
			}
		})
	}
}

// conn2db runs Exec on one pinned connection.
type conn2db struct {
	ctx  context.Context
	conn *sql.Conn
}

func (c conn2db) Exec(q string, args ...any) (sql.Result, error) {
	return c.conn.ExecContext(c.ctx, q, args...)
}
