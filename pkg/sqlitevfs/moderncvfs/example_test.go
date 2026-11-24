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

package moderncvfs_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs/moderncvfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ExampleRegister runs a stock SQLite application on the tier 1 binding:
// VFS "qrdb" keeps the database file app.db as pages in a local MVCC store.
func ExampleRegister() {
	// VFS qrdb is registered once per process; this package's own tests
	// register it before the example runs, so clear that first.
	if err := moderncvfs.Unregister(); err != nil {
		log.Fatal(err)
	}

	// The page store: database app.db over an MVCC store. Without
	// WithWriterLock, writers are optimistic and a losing commit is
	// SQLITE_BUSY_SNAPSHOT.
	ms, err := mvcc.New(memory.NewStorage())
	if err != nil {
		log.Fatal(err)
	}
	backend, err := sqlitevfs.NewMVCCBackend(ms)
	if err != nil {
		log.Fatal(err)
	}
	store, err := sqlitevfs.NewStore(backend, "app.db")
	if err != nil {
		log.Fatal(err)
	}

	// The Opener maps the URI's file name to its page store.
	open := func(_ context.Context, name string, _ url.Values) (*sqlitevfs.Store, error) {
		if name != "app.db" {
			return nil, fmt.Errorf("no database %q", name)
		}
		return store, nil
	}
	if err := moderncvfs.Register(open); err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := moderncvfs.Unregister(); err != nil {
			log.Fatal(err)
		}
	}()

	db, err := sql.Open(moderncvfs.DriverName, "file:app.db?vfs=qrdb")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	for _, q := range []string{
		"create table fruit(id integer primary key, name text not null)",
		"insert into fruit(name) values('apple'), ('pear'), ('plum')",
	} {
		if _, err := db.Exec(q); err != nil {
			log.Fatal(err)
		}
	}
	rows, err := db.Query("select id, name from fruit order by id")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		fmt.Println(id, name)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	// Output:
	// 1 apple
	// 2 pear
	// 3 plum
}
