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

package provider

import (
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// QRDBSession extends sql.BaseSession with QRDB identity and tenant context.
// It carries the authenticated identity through the SQL query pipeline so that
// table implementations can scope operations to the correct tenant namespace.
//
// The base session is embedded by pointer rather than by value because
// sql.BaseSession owns mutable per-connection state — most importantly the
// active transaction (sql.BaseSession.tx, read and written through the
// sql.TransactionSession interface), but also the current database, warning
// list, and session system variables. Embedding it by value would copy that
// state at construction, so a transaction started through the QRDBSession
// would be invisible to any holder of the original base session, and vice
// versa. Sharing one session object per connection is what allows session
// state to survive across the statements of a single connection.
type QRDBSession struct {
	*sqle.BaseSession
	identity *auth.Identity

	// ws holds the WriteSet backing the session's current interactive
	// transaction. It is non-nil for the whole lifetime of every
	// statement -- go-mysql-server's engine calls StartTransaction before
	// every statement, not only after an explicit BEGIN (see
	// engine.beginTransaction) -- but only ever bound (see WriteSet.bind)
	// when a relational table actually participates while
	// ctx.GetIgnoreAutoCommit() is true, i.e. inside an explicit
	// transaction; see RelationalTable.activeWriteSet. An atomic.Pointer
	// is used, rather than a plain field guarded by a mutex, because a
	// session's transaction methods and its table read/write methods can
	// be invoked from goroutines the session itself does not schedule
	// (e.g. a connection pool's concurrent ResetSession/Close alongside an
	// in-flight statement); the pointer swap itself needs no additional
	// locking since WriteSet's own contract already confines all mutation
	// of *contents* to the single goroutine driving the current statement
	// (see WriteSet's doc comment).
	ws atomic.Pointer[WriteSet]
}

// Compile-time interface assertion: QRDBSession implements
// sql.TransactionSession, so go-mysql-server's BEGIN/COMMIT/ROLLBACK/
// SAVEPOINT plan nodes (sql/rowexec/transaction.go) route through
// StartTransaction/CommitTransaction/Rollback/CreateSavepoint/
// RollbackToSavepoint/ReleaseSavepoint below instead of silently returning
// an empty iterator (see sql/rowexec/transaction.go's own type assertion).
var _ sqle.TransactionSession = (*QRDBSession)(nil)

// NewSession creates a new QRDBSession from a base session and auth identity.
// The identity determines tenant scoping for all SQL operations executed
// within this session. base must be non-nil; the returned session shares it
// rather than copying it, so callers must not reuse a base session across
// connections.
func NewSession(base *sqle.BaseSession, identity *auth.Identity) *QRDBSession {
	return &QRDBSession{
		BaseSession: base,
		identity:    identity,
	}
}

// Identity returns the authenticated identity associated with this session.
func (s *QRDBSession) Identity() *auth.Identity {
	return s.identity
}

// TenantID returns the tenant namespace for this session. Returns
// tenant.SystemTenantID if the identity is nil or has an empty TenantID —
// callers without explicit tenant binding execute against the system tenant.
func (s *QRDBSession) TenantID() string {
	if s.identity == nil || s.identity.TenantID == "" {
		return tenant.SystemTenantID
	}
	return s.identity.TenantID
}

// ---------------------------------------------------------------------------
// sql.TransactionSession
// ---------------------------------------------------------------------------

// transactionToken is the opaque sql.Transaction StartTransaction returns
// and go-mysql-server hands back to CommitTransaction/Rollback/
// CreateSavepoint/RollbackToSavepoint/ReleaseSavepoint. It carries no state
// of its own -- the session's ws field is the single source of truth for
// the active write-set -- but a distinct type (rather than reusing an
// existing one) keeps its String() output identifiably ours in logs and
// diagnostics.
type transactionToken struct {
	readOnly bool
}

// String implements fmt.Stringer, part of sql.Transaction.
func (t *transactionToken) String() string {
	if t.readOnly {
		return "qrdb read-only transaction"
	}
	return "qrdb transaction"
}

// IsReadOnly implements sql.Transaction.
func (t *transactionToken) IsReadOnly() bool { return t.readOnly }

// writeSet returns the WriteSet backing the session's current transaction,
// or nil if none has been started (which, in practice, is only true before
// the very first statement on a freshly created session -- see the ws field
// doc comment).
func (s *QRDBSession) writeSet() *WriteSet {
	return s.ws.Load()
}

// StartTransaction begins a new transaction, discarding any write-set left
// over from a prior one. go-mysql-server calls this before every
// statement, not only after an explicit BEGIN/START TRANSACTION (see
// engine.beginTransaction) -- the returned WriteSet stays unbound, and
// therefore free of any snapshot-read cost, until a relational table
// actually participates (see WriteSet.bind, called from
// RelationalTable.activeWriteSet).
func (s *QRDBSession) StartTransaction(_ *sqle.Context, tCharacteristic sqle.TransactionCharacteristic) (sqle.Transaction, error) {
	readOnly := tCharacteristic == sqle.ReadOnly
	s.ws.Store(newWriteSet(readOnly))
	return &transactionToken{readOnly: readOnly}, nil
}

// CommitTransaction applies the session's buffered write-set as a single
// compare-and-swap (see WriteSet.commit). A write-set that never bound to a
// relational table, or bound but never buffered a write, commits trivially.
//
// A genuine infrastructure error (e.g. the shard's leader is unreachable)
// leaves the write-set in place, uncommitted, so a caller may safely retry
// CommitTransaction without losing buffered work. A compare-and-swap
// conflict, by contrast, is unrecoverable for this transaction -- another
// transaction already committed over one of the same keys -- so the
// write-set is cleared (matching MySQL/InnoDB automatically rolling back a
// transaction that hits ER_LOCK_DEADLOCK) and a *TransactionConflictError,
// wrapped in sql.ErrLockDeadlock, is returned; see that error type's doc
// comment for exactly how this becomes MySQL error 1213 / SQLSTATE 40001
// over the wire.
func (s *QRDBSession) CommitTransaction(ctx *sqle.Context, _ sqle.Transaction) error {
	ws := s.ws.Load()
	if ws == nil {
		return nil
	}

	resp, err := ws.commit(ctx)
	if err != nil {
		return err
	}
	s.ws.CompareAndSwap(ws, nil)

	if !resp.Succeeded {
		key := ""
		if len(ws.order) > 0 {
			key = ws.order[0]
		}
		conflict := &TransactionConflictError{Key: key, Err: &service.TransactionConflictError{Key: key}}

		// ErrLockDeadlock's kind message carries one %s naming what
		// conflicted, and go-errors' Wrap appends a further ": %s" for the
		// cause (error.go:35). Passing no value left the first verb unfilled,
		// so clients saw a mangled "serialization failure: %!s(MISSING), try
		// restarting transaction.: %!s(MISSING)" -- the MySQL error code was
		// always correct, but the text was unreadable.
		detail := key
		if detail == "" {
			detail = "a key written by this transaction"
		}
		return sqle.ErrLockDeadlock.Wrap(conflict, detail)
	}
	return nil
}

// Rollback discards the session's buffered write-set unconditionally. There
// is nothing to undo against the store itself: nothing in the write-set was
// ever proposed, so discarding the buffer is the entire rollback.
func (s *QRDBSession) Rollback(_ *sqle.Context, _ sqle.Transaction) error {
	s.ws.Store(nil)
	return nil
}

// CreateSavepoint marks the current position of the session's write-set
// under name. A savepoint created outside a bound write-set (nothing has
// been read or written yet) is accepted but inert: there is nothing yet to
// roll back to.
func (s *QRDBSession) CreateSavepoint(_ *sqle.Context, _ sqle.Transaction, name string) error {
	ws := s.ws.Load()
	if ws == nil {
		return nil
	}
	ws.Mark(name)
	return nil
}

// RollbackToSavepoint undoes every buffered mutation made since name was
// marked (see WriteSet.RollbackTo). The savepoint remains valid for a
// subsequent RollbackToSavepoint or ReleaseSavepoint.
func (s *QRDBSession) RollbackToSavepoint(_ *sqle.Context, _ sqle.Transaction, name string) error {
	ws := s.ws.Load()
	if ws == nil {
		return &SavepointNotFoundError{Name: name}
	}
	return ws.RollbackTo(name)
}

// ReleaseSavepoint discards name from the write-set's set of active
// savepoints without affecting any buffered data.
func (s *QRDBSession) ReleaseSavepoint(_ *sqle.Context, _ sqle.Transaction, name string) error {
	ws := s.ws.Load()
	if ws == nil {
		return &SavepointNotFoundError{Name: name}
	}
	return ws.Release(name)
}

// requireAutocommit returns *NonTransactionalTableError when ctx's session
// has an explicit interactive transaction open (BEGIN/START TRANSACTION was
// issued and not yet committed or rolled back). KV, entity, and collection
// tables (kv_table.go, entity_table.go, collection_table.go) always apply
// their writes immediately -- they have no WriteSet of their own to buffer
// into -- so every one of their write paths calls this first: silently
// applying such a write while an explicit transaction is open would let it
// escape that transaction's atomicity and visibility boundary, and survive
// a later ROLLBACK. An ordinary autocommit statement against these table
// kinds (ctx.GetIgnoreAutoCommit() false, the overwhelming common case) is
// entirely unaffected.
func requireAutocommit(ctx *sqle.Context, table string) error {
	if !ctx.GetIgnoreAutoCommit() {
		return nil
	}
	if _, ok := ctx.Session.(*QRDBSession); !ok {
		return nil
	}
	return &NonTransactionalTableError{Table: table}
}

// flushPendingWriteSet commits any writes buffered by the session's current
// interactive transaction, matching MySQL's rule that DDL (and other
// catalog-mutating statements such as TRUNCATE and
// ALTER TABLE ... AUTO_INCREMENT = N) implicitly commits any pending
// transaction before it runs. Every DDL entry point in this package
// (Database.CreateTable/DropTable/RenameTable/SetCollation,
// RelationalTable.AddColumn/CreateIndex/DropIndex/RenameIndex/
// RewriteInserter/Truncate, and relationalAutoIncSetter.
// SetAutoIncrementValue) calls this before mutating the catalog.
//
// A transaction flushed this way is therefore never undone by a later
// ROLLBACK -- exactly like real MySQL, where DDL is not transactional and a
// COMMIT it implicitly triggers cannot be reversed. It is a no-op for a
// session with no pending write-set (autocommit, or an explicit transaction
// that has not yet buffered anything).
func flushPendingWriteSet(ctx *sqle.Context) error {
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil
	}
	return sess.CommitTransaction(ctx, ctx.GetTransaction())
}
