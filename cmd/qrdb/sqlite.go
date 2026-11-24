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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/spf13/cobra"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs/moderncvfs"
	sdksqlitevfs "github.com/jeremyhahn/go-qrdb/sdk/go/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The `qrdb sqlite` concurrency modes and the URI parameter carrying the
// stage threshold to the Opener.
const (
	sqliteConcurrencyLock       = "lock"
	sqliteConcurrencyOptimistic = "optimistic"
	sqliteStageThresholdParam   = "qrdb_stage_threshold"
)

var (
	sqliteRegisterOnce sync.Once
	errSQLiteRegister  error
)

// sqliteBackend builds the page store's Backend the Opener opens databases
// over: the SDK Backend over the connected client.
var sqliteBackend = func() (sqlitevfs.Backend, error) {
	if client == nil {
		return nil, errors.New("sqlite: not connected")
	}
	return sdksqlitevfs.NewBackend(client.MVCC(), client.Transaction())
}

// registerSQLiteVFS registers VFS qrdb once per process, over an Opener
// that opens each database in the connected server's page store.
func registerSQLiteVFS() error {
	sqliteRegisterOnce.Do(func() {
		errSQLiteRegister = moderncvfs.Register(openSQLiteStore)
	})
	return errSQLiteRegister
}

// openSQLiteStore opens database name over the SDK Backend, in the
// concurrency mode and with the stage threshold the URI names.
func openSQLiteStore(_ context.Context, name string, params url.Values) (*sqlitevfs.Store, error) {
	b, err := sqliteBackend()
	if err != nil {
		return nil, err
	}
	var opts []sqlitevfs.Option
	switch mode := params.Get(moderncvfs.ConcurrencyParam); mode {
	case sqliteConcurrencyOptimistic:
		opts = append(opts, sqlitevfs.WithOptimistic())
	case "", sqliteConcurrencyLock:
		w, err := sdksqlitevfs.NewWriterLock(client.Lock(), sqliteHolderID(), 0)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sqlitevfs.WithWriterLock(w))
	default:
		return nil, fmt.Errorf("%w: concurrency %q", sqlitevfs.ErrInvalidOption, mode)
	}
	if v := params.Get(sqliteStageThresholdParam); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("%w: stage threshold %q", sqlitevfs.ErrInvalidOption, v)
		}
		opts = append(opts, sqlitevfs.WithStageThreshold(n))
	}
	return sqlitevfs.NewStore(b, name, opts...)
}

// sqliteHolderID names this process as the holder of a writer lock.
func sqliteHolderID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return "qrdb-cli/" + host + "/" + strconv.Itoa(os.Getpid())
}

// sqliteOpenFlags are the flags exec and query share.
type sqliteOpenFlags struct {
	concurrency    string
	stageThreshold int
}

func (f *sqliteOpenFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.concurrency, "concurrency", sqliteConcurrencyLock, "Write concurrency: lock (one writer holds the database's writer lock) or optimistic (commits check their reads)")
	cmd.Flags().IntVar(&f.stageThreshold, "stage-threshold", 0, "Bytes of dirty pages above which a commit is staged and published in one revision (0: the store's default)")
}

// open opens database db through VFS qrdb.
func (f *sqliteOpenFlags) open(db string) (*sql.DB, error) {
	if err := sqlitevfs.ValidateDatabaseName(db); err != nil {
		return nil, err
	}
	switch f.concurrency {
	case sqliteConcurrencyLock, sqliteConcurrencyOptimistic:
	default:
		return nil, fmt.Errorf("--concurrency must be %s or %s, got %q", sqliteConcurrencyLock, sqliteConcurrencyOptimistic, f.concurrency)
	}
	if f.stageThreshold < 0 {
		return nil, fmt.Errorf("--stage-threshold must not be negative, got %d", f.stageThreshold)
	}
	if err := registerSQLiteVFS(); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("vfs", moderncvfs.VFSName)
	q.Set(moderncvfs.ConcurrencyParam, f.concurrency)
	if f.stageThreshold > 0 {
		q.Set(sqliteStageThresholdParam, strconv.Itoa(f.stageThreshold))
	}
	conn, err := sql.Open(moderncvfs.DriverName, "file:"+db+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection: a statement and the next see one page cache, and a
	// second connection cannot contend with the first for the writer lock.
	conn.SetMaxOpenConns(1)
	return conn, nil
}

// sqliteError maps SQLite's busy and full result codes to the errors whose
// exit status is that code.
func sqliteError(db string, err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_BUSY:
			return &SQLiteBusyError{DB: db, Err: err}
		case sqlite3.SQLITE_FULL:
			return &SQLiteFullError{DB: db, Err: err}
		}
	}
	return err
}

func newSQLiteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sqlite",
		Short: "Run SQL on a SQLite database stored in qrdb's page store",
		Long: `Run SQL on a SQLite database whose pages live in the server's MVCC store
under sqlite/<db>/, opened through the qrdb VFS over the connected transport.

A statement refused because another writer holds the database (or, with
--concurrency optimistic, because another commit changed what it read) exits
with SQLite's SQLITE_BUSY code 5; a commit refused by the tenant's storage
quota exits with SQLITE_FULL code 13.`,
	}
	cmd.AddCommand(newSQLiteExecCmd(), newSQLiteQueryCmd(), newSQLiteRestoreCmd())
	return cmd
}

func newSQLiteExecCmd() *cobra.Command {
	var flags sqliteOpenFlags
	cmd := &cobra.Command{
		Use:   "exec <db> <sql>",
		Short: "Execute a SQL statement and print the rows it affected",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, stmt := args[0], args[1]
			conn, err := flags.open(db)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			res, err := conn.ExecContext(cmd.Context(), stmt)
			if err != nil {
				return sqliteError(db, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return sqliteError(db, err)
			}
			switch outputFormat {
			case "json":
				return printJSON(map[string]int64{"rows_affected": n})
			case "yaml":
				return printYAML(map[string]int64{"rows_affected": n})
			default:
				_, err := fmt.Fprintf(outputWriter, "%d rows affected\n", n)
				return err
			}
		},
	}
	flags.register(cmd)
	return cmd
}

func newSQLiteQueryCmd() *cobra.Command {
	var flags sqliteOpenFlags
	cmd := &cobra.Command{
		Use:   "query <db> <sql>",
		Short: "Run a SQL query and print its rows",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, stmt := args[0], args[1]
			conn, err := flags.open(db)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			columns, rows, err := sqliteQuery(cmd.Context(), conn, stmt)
			if err != nil {
				return sqliteError(db, err)
			}
			switch outputFormat {
			case "json", "yaml":
				out := make([]map[string]any, 0, len(rows))
				for _, row := range rows {
					m := make(map[string]any, len(columns))
					for i, c := range columns {
						m[c] = row[i]
					}
					out = append(out, m)
				}
				if outputFormat == "json" {
					return printJSON(out)
				}
				return printYAML(out)
			default:
				table := make([][]string, 0, len(rows))
				for _, row := range rows {
					cells := make([]string, len(row))
					for i, v := range row {
						if v == nil {
							cells[i] = "NULL"
						} else {
							cells[i] = fmt.Sprint(v)
						}
					}
					table = append(table, cells)
				}
				printTable(columns, table)
				return nil
			}
		},
	}
	flags.register(cmd)
	return cmd
}

// sqliteQuery runs stmt and returns its columns and rows, a text or blob
// value as a string.
func sqliteQuery(ctx context.Context, conn *sql.DB, stmt string) ([]string, [][]any, error) {
	rs, err := conn.QueryContext(ctx, stmt)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rs.Close() }()
	columns, err := rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	var rows [][]any
	for rs.Next() {
		row := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, v := range row {
			if b, ok := v.([]byte); ok {
				row[i] = string(b)
			}
		}
		rows = append(rows, row)
	}
	return columns, rows, rs.Err()
}

func newSQLiteRestoreCmd() *cobra.Command {
	var (
		revision int64
		dryRun   bool
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "restore <db>",
		Short: "Restore a SQLite database to the state it held at an earlier MVCC revision",
		Long: `Restore returns every key of the database (sqlite/<db>/) to the value it
held at an earlier MVCC revision by proposing new writes, as "kv restore
--prefix" does: committed history is never discarded.

Use --dry-run to preview the plan. Without --force, a key modified since the
plan was computed is left untouched and reported as a conflict, and the
command exits non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := args[0]
			if err := sqlitevfs.ValidateDatabaseName(db); err != nil {
				return err
			}
			result, err := client.MVCC().RestoreToRevision(cmd.Context(), &transport.RestoreRevisionRequest{
				Revision: revision,
				Prefix:   []byte("sqlite/" + db + "/"),
				DryRun:   dryRun,
				Force:    force,
			})
			if err != nil {
				return err
			}
			if err := printRestoreRevisionResult(result); err != nil {
				return err
			}
			if !dryRun && !force && result.Conflicted > 0 {
				return &RestoreConflictsError{Revision: result.Revision, Conflicted: result.Conflicted}
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&revision, "revision", 0, "Target MVCC revision to restore the database to")
	_ = cmd.MarkFlagRequired("revision")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Compute and print the restore plan without writing anything")
	cmd.Flags().BoolVar(&force, "force", false, "Apply the plan unconditionally, overwriting keys modified since the plan was computed")
	return cmd
}
