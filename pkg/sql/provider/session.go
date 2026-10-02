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
	"context"
	"strings"
	"sync/atomic"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/types"

	// Imported for its initialization side effect: sql.SystemVariables, the
	// registry this file's init registers qrdb_isolation with, is populated
	// by this package's own init. Go guarantees an imported package is
	// initialized first, so the blank import is what makes that ordering a
	// fact rather than a hope about who else happens to import it.
	_ "github.com/dolthub/go-mysql-server/sql/variables"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
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
	// when a relational table actually participates in an explicit
	// transaction (explicitTxn), or writes in an autocommit statement (see
	// writeset_autocommit.go); see RelationalTable.writeSetFor. An atomic.Pointer
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

	// stmt is the resolution scope of the command in flight, opened by
	// CommandBegin and closed by CommandEnd (see statementScope).
	stmt atomic.Pointer[statementScope]

	// locks is the connection's row-lock lease, shared by the lock owners
	// of its successive transactions -- every autocommit statement is one
	// -- so a locking statement does not grant and revoke a lease of its
	// own (see rowlock.Session). SessionEnd revokes it.
	locks *rowlock.Session
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
		locks:       rowlock.NewSession(),
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
	ws := newWriteSet(readOnly, s.isolationLevel(ctx))
	ws.concurrency = s.concurrencyChoice(ctx)
	if prev := s.ws.Swap(ws); prev != nil {
		// A transaction is only ever replaced after it was committed or
		// rolled back, which already released its row locks; this is the
		// backstop for a statement that failed before either ran.
		_ = prev.releaseLocks(ctx)
	}
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
// transaction already committed over one of the same keys, or (go-qrdb#547)
// a TRUNCATE advanced a table's generation out from under it -- so the
// write-set is cleared (matching MySQL/InnoDB automatically rolling back a
// transaction that hits ER_LOCK_DEADLOCK) and a typed conflict error,
// wrapped in sql.ErrLockDeadlock, is returned; see that error type's doc
// comment for exactly how this becomes MySQL error 1213 / SQLSTATE 40001
// over the wire. WriteSet.staleResolution distinguishes the causes so a
// TRUNCATE or DDL race is reported as its own typed error rather
// than the generic *TransactionConflictError -- both are wrapped the same
// way, so the wire mapping is identical either way, but a caller inspecting
// the cause (errors.As through Cause()) sees which one actually happened.
func (s *QRDBSession) CommitTransaction(ctx *sqle.Context, _ sqle.Transaction) error {
	ws := s.ws.Load()
	if ws == nil {
		return nil
	}
	if ws.aborted != nil {
		// The transaction was already rolled back as a deadlock victim.
		// Ending it is what the client must do next; an explicit COMMIT
		// is told once more that nothing was committed.
		s.ws.CompareAndSwap(ws, nil)
		explicit, err := explicitTxn(ctx)
		if err != nil {
			return err
		}
		s.endTransaction()
		if explicit {
			return abortedError(ws.aborted)
		}
		return nil
	}

	// The commit and the release of the transaction's row locks are one
	// proposal: the locks end with the transaction, whether its commit wins
	// or loses (see WriteSet.commitAndRelease).
	resp, err := ws.commitAndRelease(ctx)
	if err != nil {
		if ws.implicit {
			// An autocommit statement has no COMMIT to retry: the
			// statement failed, and nothing of it outlives it.
			s.ws.CompareAndSwap(ws, nil)
			s.endTransaction()
			_ = ws.releaseLocks(ctx)
			return err
		}
		// The write set stays for a retry, and so do the locks that
		// protect it.
		return err
	}
	s.ws.CompareAndSwap(ws, nil)

	if !resp.Succeeded {
		// The transaction is over -- rolled back, as InnoDB rolls back a
		// transaction that hits ER_LOCK_DEADLOCK -- so the session leaves
		// it too. go-mysql-server ends its own transaction only when COMMIT
		// succeeds; left to it, the session would stay "in" a transaction
		// that no longer exists, and the next statement would run in
		// neither autocommit nor a transaction anyone can commit.
		s.endTransaction()
		if stale, serr := ws.staleResolution(ctx); serr == nil && stale != nil {
			return sqle.ErrLockDeadlock.Wrap(stale, stale.Error())
		}
		// An autocommit statement that lost its commit to a concurrent
		// statement taking one of the keys it occupies is a duplicate key,
		// exactly as when the row was written on its own.
		if dup := ws.duplicateAfterConflict(ctx); dup != nil {
			return dup
		}

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

// endTransaction ends go-mysql-server's view of the session's transaction,
// exactly as its own COMMIT and ROLLBACK do on success, so the next
// statement starts a new one.
func (s *QRDBSession) endTransaction() {
	s.SetIgnoreAutoCommit(false)
	s.SetTransaction(nil)
}

// Rollback discards the session's buffered write-set unconditionally. There
// is nothing to undo against the store itself: nothing in the write-set was
// ever proposed, so discarding the buffer is the entire rollback.
func (s *QRDBSession) Rollback(ctx *sqle.Context, _ sqle.Transaction) error {
	if ws := s.ws.Swap(nil); ws != nil {
		_ = ws.releaseLocks(ctx)
	}
	return nil
}

// Compile-time interface assertion: go-mysql-server tells a
// LifecycleAwareSession when its connection ends, which is when an open
// transaction's row locks must be released rather than left to expire.
var _ sqle.LifecycleAwareSession = (*QRDBSession)(nil)

// CommandBegin implements sql.LifecycleAwareSession. No statement is in
// flight at a command boundary, so any statement savepoint still open is a
// leftover and is dropped (see WriteSet.resetStatements), and the command
// gets a fresh resolution scope (see statementScope).
func (s *QRDBSession) CommandBegin() error {
	if ws := s.ws.Load(); ws != nil {
		ws.resetStatements()
		if ws.implicit {
			// An autocommit statement that failed before go-mysql-server
			// committed it left its implicit transaction open. Nothing of a
			// statement may outlive it: the next statement gets a fresh
			// write-set, and the leftover's locks are released.
			fresh := newWriteSet(ws.readOnly, ws.level)
			fresh.concurrency = ws.concurrency
			if s.ws.CompareAndSwap(ws, fresh) {
				_ = ws.releaseLocks(context.Background())
			}
		}
	}
	s.stmt.Store(&statementScope{})
	return nil
}

// CommandEnd implements sql.LifecycleAwareSession. It closes the command's
// resolution scope; a command's row locks belong to its transaction, not to
// the command.
func (s *QRDBSession) CommandEnd() {
	s.stmt.Store(nil)
}

// SessionEnd implements sql.LifecycleAwareSession: the connection is gone,
// so its open transaction is abandoned, every row lock it holds is released
// now, and its lock lease is revoked. A connection that dies without this
// call is covered by the lease expiring.
//
// The lease is revoked first: that one proposal deletes every lock record
// the session holds, so the abandoned transaction's release that follows
// finds its lease ended and has nothing left to delete. A per-request
// session -- the ad-hoc query service's -- pays one proposal to end, not
// two.
func (s *QRDBSession) SessionEnd() {
	_ = s.locks.Close(context.Background())
	if ws := s.ws.Swap(nil); ws != nil {
		_ = ws.releaseLocks(context.Background())
	}
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

// explicitTxn reports whether the statement ctx runs belongs to an explicit,
// multi-statement transaction: one opened by BEGIN / START TRANSACTION, or
// -- as in InnoDB -- any statement of a session with autocommit off, whose
// transaction stays open until COMMIT or ROLLBACK. Every "explicit or
// autocommit" decision in this package is made here, and nowhere else.
func explicitTxn(ctx *sqle.Context) (bool, error) {
	if ctx.GetIgnoreAutoCommit() {
		return true, nil
	}
	autocommit, err := plan.IsSessionAutocommit(ctx)
	if err != nil {
		return false, err
	}
	return !autocommit, nil
}

// requireAutocommit returns *NonTransactionalTableError when ctx's
// statement belongs to an explicit transaction (see explicitTxn). KV, entity, and collection
// tables (kv_table.go, entity_table.go, collection_table.go) always apply
// their writes immediately -- they have no WriteSet of their own to buffer
// into -- so every one of their write paths calls this first: silently
// applying such a write while an explicit transaction is open would let it
// escape that transaction's atomicity and visibility boundary, and survive
// a later ROLLBACK. An ordinary autocommit statement against these table
// kinds is entirely unaffected.
func requireAutocommit(ctx *sqle.Context, table string) error {
	if _, ok := ctx.Session.(*QRDBSession); !ok {
		return nil
	}
	explicit, err := explicitTxn(ctx)
	if err != nil {
		return err
	}
	if !explicit {
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
	if ws := sess.writeSet(); ws != nil && ws.aborted != nil {
		// A transaction already rolled back as a deadlock victim has
		// nothing to commit; the DDL simply ends it, as MySQL's implicit
		// commit ends any transaction.
		sess.ws.CompareAndSwap(ws, nil)
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
