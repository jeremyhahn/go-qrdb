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
	"strings"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"

	// Imported for its initialization side effect: sql.SystemVariables, the
	// registry this file's init registers qrdb_isolation with, is populated
	// by this package's own init. Go guarantees an imported package is
	// initialized first, so the blank import is what makes that ordering a
	// fact rather than a hope about who else happens to import it.
	_ "github.com/dolthub/go-mysql-server/sql/variables"

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

	// statementIndexOrigin -- see SetStatementIndexOrigin.
	statementIndexOrigin atomic.Bool
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

// ---------------------------------------------------------------------------
// qrdb_isolation session system variable
// ---------------------------------------------------------------------------

// IsolationVariable is the name of the session system variable selecting an
// interactive transaction's conflict-detection scope:
//
//	SET qrdb_isolation = 'none';           -- last-writer-wins
//	SET qrdb_isolation = 'written';        -- the default
//	SET qrdb_isolation = 'written+reads';  -- also compare point reads
//	SELECT @@qrdb_isolation;
//
// See IsolationLevel for exactly what each value does and does not
// guarantee. The variable is SESSION-scoped only: SET GLOBAL qrdb_isolation
// is refused (ErrSystemVariableSessionOnly), because the setting is a
// property of one client's workload, not of the cluster, and a global
// default would silently change the isolation of every other connection.
//
// # Why a qrdb-specific variable rather than SET TRANSACTION ISOLATION LEVEL
//
// MySQL's isolation levels name the behavior of a LOCK-based engine, and
// QRDB's transactions are optimistic; the two do not correspond, so no
// MySQL level is accepted as a spelling of a qrdb_isolation value and none
// of the values below claims to be equivalent to one. Concretely:
//
//   - InnoDB's READ COMMITTED still prevents a lost update, because a row
//     an UPDATE touches is locked until commit. IsolationNone does not.
//     Aliasing the two would be a straightforward lie.
//   - InnoDB's REPEATABLE READ, the closest fit to IsolationWritten,
//     BLOCKS a second writer of the same row and then applies it; QRDB
//     ABORTS it with 1213. Two concurrent "UPDATE t SET n = n + 1" both
//     succeed under InnoDB RR and one is rejected here. Same guarantee
//     against lost updates, materially different behavior.
//   - InnoDB's SERIALIZABLE covers ranges with next-key locks, which is
//     exactly what IsolationWrittenReads does not do (see
//     IsolationRangeReadError).
//
// MySQL's own transaction_isolation variable, and the SET TRANSACTION
// ISOLATION LEVEL statement that writes it, are therefore left exactly as
// go-mysql-server already treats them -- accepted, recorded, and not
// interpreted by this package -- rather than being wired to this setting.
// A client that issues one gets the behavior it got before this variable
// existed, which for the default is also the behavior it gets now.
const IsolationVariable = "qrdb_isolation"

// init registers qrdb_isolation with go-mysql-server's global system
// variable registry, which is what gives every session created afterwards a
// copy of it (sql.BaseSession takes SystemVariables.NewSessionMap() at
// construction) and what makes SET/SELECT @@qrdb_isolation resolve at all.
//
// Registration is global; the VALUE is per session. AddSystemVariables also
// records the definition in the registry's own backing collection, so a
// later variables.InitSystemVariables() -- which rebuilds the registry from
// that collection -- keeps qrdb_isolation rather than dropping it.
func init() {
	sqle.SystemVariables.AddSystemVariables([]sqle.SystemVariable{isolationSystemVariable()})
}

// isolationSystemVariable is the definition init registers: a session-scoped,
// dynamic ENUM over every level in isolationLevelOrder, defaulting to
// IsolationWritten. The enum type is what rejects a misspelled level at the
// SET statement itself, rather than accepting it and quietly running at some
// other level.
func isolationSystemVariable() sqle.SystemVariable {
	values := make([]string, 0, len(isolationLevelOrder))
	for _, level := range isolationLevelOrder {
		values = append(values, level.String())
	}
	return &sqle.MysqlSystemVariable{
		Name:              IsolationVariable,
		Scope:             sqle.GetMysqlScope(sqle.SystemVariableScope_Session),
		Dynamic:           true,
		SetVarHintApplies: false,
		Type:              types.NewSystemEnumType(IsolationVariable, values...),
		Default:           IsolationWritten.String(),
	}
}

// isolationLevel returns the conflict-detection scope this session has
// selected, which StartTransaction samples once per transaction.
//
// It answers IsolationWritten -- the default, and the registered default of
// the variable itself -- for a session whose variable map does not carry
// qrdb_isolation at all. That is not a silent downgrade of a selected
// level: a session can only lack the variable if it was built against a
// system variable registry this package's init never reached, in which case
// no level was ever selected and the answer is the default by definition.
func (s *QRDBSession) isolationLevel(ctx *sqle.Context) IsolationLevel {
	value, err := s.GetSessionVariable(ctx, IsolationVariable)
	if err != nil {
		return IsolationWritten
	}
	name, ok := value.(string)
	if !ok {
		return IsolationWritten
	}
	level, ok := isolationLevels[strings.ToLower(name)]
	if !ok {
		return IsolationWritten
	}
	return level
}

// Identity returns the authenticated identity associated with this session.
func (s *QRDBSession) Identity() *auth.Identity {
	return s.identity
}

// TenantID returns the tenant namespace for this session. Returns
// tenant.SystemTenantID if the identity is nil or has an empty TenantID —
// callers without explicit tenant binding execute against the system tenant.
//
// The fold is tenant.Normalize, not an open-coded comparison: this is one of
// the boundaries tenant.IsSystemTenant's precondition depends on (the SQL
// privilege plane reads it in privilege.principalOf and exempts the system
// tenant from every grant check), so it must use the same single fold every
// other boundary uses. See #188.
func (s *QRDBSession) TenantID() string {
	if s.identity == nil {
		return tenant.SystemTenantID
	}
	return tenant.Normalize(s.identity.TenantID)
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
//
// The session's qrdb_isolation setting is sampled here and fixed for the
// whole transaction, so that changing it mid-transaction cannot retroactively
// alter the conflict detection of work already buffered under the old
// setting. Because engine.beginTransaction is a no-op while a transaction is
// already open, this is once per BEGIN inside an explicit transaction, and
// once per statement in autocommit.
func (s *QRDBSession) StartTransaction(ctx *sqle.Context, tCharacteristic sqle.TransactionCharacteristic) (sqle.Transaction, error) {
	readOnly := tCharacteristic == sqle.ReadOnly
	s.ws.Store(newWriteSet(readOnly, s.isolationLevel(ctx)))
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

// statementIndexOrigin records whether the statement currently executing on
// this session is a standalone CREATE INDEX. The PostgreSQL wire front end
// sets it around each such statement (see pkg/sql/pgsql's HandleParse) so
// RelationalTable.CreateIndex can persist PostgreSQL's index-vs-constraint
// distinction into the catalog -- the plan node reaching CreateIndex is
// identical for CREATE INDEX and ALTER TABLE ADD CONSTRAINT UNIQUE, so the
// statement text is the only place the distinction exists. An atomic is
// used for the same cross-goroutine reasons as ws above.
//
// The zero value (false, "constraint or unknown origin") is deliberately
// the MySQL-faithful default: a surface that never sets the hint gets
// MySQL's behaviour, where every unique index is a constraint.

// SetStatementIndexOrigin marks whether the statement about to execute is a
// standalone CREATE INDEX.
func (s *QRDBSession) SetStatementIndexOrigin(isStatementIndex bool) {
	s.statementIndexOrigin.Store(isStatementIndex)
}

// StatementIndexOrigin reports whether the currently executing statement
// was marked a standalone CREATE INDEX.
func (s *QRDBSession) StatementIndexOrigin() bool {
	return s.statementIndexOrigin.Load()
}
