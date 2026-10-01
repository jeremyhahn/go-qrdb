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
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/lockclause"
)

// ---------------------------------------------------------------------------
// Row locks on relational tables
//
// This file is where a relational table decides which of its rows a
// statement locks, takes those locks through the transaction's
// rowlock.Owner, and reads the locked rows at their latest committed
// version. See docs/row-locking/ for the contract.
// ---------------------------------------------------------------------------

// SQLCodeSkipLockedUnsupported is MySQL's ER_NOT_SUPPORTED_YET, the number a
// SKIP LOCKED clause is refused with.
const SQLCodeSkipLockedUnsupported = 1235

// lockRequest is the row lock one access takes: mode zero takes none.
type lockRequest struct {
	mode rowlock.Mode
	wait time.Duration
}

// strengths maps a lock clause's strength to the lock mode it takes.
var strengths = map[lockclause.Strength]rowlock.Mode{
	lockclause.Shared:    rowlock.ModeShared,
	lockclause.Exclusive: rowlock.ModeExclusive,
}

// WithRowLocks supplies how the lock table this table's rows are locked in
// is resolved, and the database's configured concurrency mode. resolve runs
// at most once, the first time a statement needs a lock; its error is
// reported to that statement.
func WithRowLocks(resolve func() (*rowlock.Table, error), dbDefault database.Concurrency) RelationalTableOption {
	return func(t *RelationalTable) {
		t.rowLocks = sync.OnceValues(resolve)
		t.defaultConcurrency = dbDefault
	}
}

// lockState returns the session and the go-mysql-server transaction's write
// set that own any lock this statement takes, or nils for a context that
// is not a QRDB session (which then takes no locks).
func lockState(ctx *sqle.Context) (*QRDBSession, *WriteSet) {
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil, nil
	}
	ws := sess.writeSet()
	return sess, ws
}

// pessimistic reports whether the transaction runs in pessimistic mode.
func (t *RelationalTable) pessimistic(ws *WriteSet) bool {
	return effectiveConcurrency(ws.concurrency, t.defaultConcurrency) == database.ConcurrencyPessimistic
}

// readLock returns the lock this statement takes on the rows it reads from
// t: the lock clause of a query block reading t, or, in pessimistic mode, an
// exclusive lock when t is the target of the statement's UPDATE or DELETE.
func (t *RelationalTable) readLock(ctx *sqle.Context) (lockRequest, error) {
	sess, ws := lockState(ctx)
	if sess == nil {
		return lockRequest{}, nil
	}
	intent := lockclause.Analyze(ctx, ctx.Query(), sqle.LoadSqlMode(ctx).ParserOptions())
	if intent.Empty() {
		return lockRequest{}, nil
	}
	if ws == nil {
		// go-mysql-server starts a transaction before every statement, so
		// a statement with no write set is a composition fault; taking no
		// lock would silently break the lock clause's promise.
		return lockRequest{}, &RowLockingUnavailableError{Table: t.def.Name}
	}
	clause := intent.Locking(t.def.Name)
	if clause != lockclause.None && intent.SkipLocked {
		return lockRequest{}, mysql.NewSQLError(SQLCodeSkipLockedUnsupported, "42000", "%s", (&SkipLockedUnsupportedError{}).Error())
	}
	mode := strengths[clause]
	if mode < rowlock.ModeExclusive && intent.Targets(t.def.Name) && t.pessimistic(ws) {
		mode = rowlock.ModeExclusive
	}
	if mode == 0 {
		return lockRequest{}, nil
	}
	wait := sess.lockWaitTimeout(ctx)
	if intent.NoWait {
		wait = 0
	}
	return lockRequest{mode: mode, wait: wait}, nil
}

// writeLock returns the lock pessimistic-mode DML takes on the keys it
// writes: exclusive, waiting the session's lock-wait timeout. ok is false in
// optimistic mode, where DML takes no locks.
func (t *RelationalTable) writeLock(ctx *sqle.Context) (req lockRequest, ok bool) {
	sess, ws := lockState(ctx)
	if sess == nil || ws == nil || !t.pessimistic(ws) {
		return lockRequest{}, false
	}
	return lockRequest{mode: rowlock.ModeExclusive, wait: sess.lockWaitTimeout(ctx)}, true
}

// lockKeys takes req on keys for the statement's transaction.
func (t *RelationalTable) lockKeys(ctx *sqle.Context, keys []string, req lockRequest) error {
	sess, ws := lockState(ctx)
	if ws == nil {
		return nil
	}
	if ws.aborted != nil {
		return abortedError(ws.aborted)
	}
	if t.rowLocks == nil {
		return &RowLockingUnavailableError{Table: t.def.Name}
	}
	tbl, err := t.rowLocks()
	if err != nil {
		return err
	}
	err = ws.lockOwner(sess.locks).Acquire(ctx, tbl, keys, req.mode, req.wait)
	return lockFailure(ctx, ws, err)
}

// lockFailure renders a lock-table error as the error a client classifies:
// MySQL's own numbers for a timeout and NOWAIT (in the one shape the MySQL
// wire forwards verbatim), and go-mysql-server's ER_LOCK_DEADLOCK -- after
// rolling the transaction back -- for a deadlock or a lost lock.
func lockFailure(ctx *sqle.Context, ws *WriteSet, err error) error {
	if err == nil {
		return nil
	}
	var (
		timeout  *rowlock.LockWaitTimeoutError
		nowait   *rowlock.LockNotAvailableError
		deadlock *rowlock.DeadlockError
		lost     *rowlock.LockLostError
		scope    *rowlock.ScopeMismatchError
		released *rowlock.OwnerReleasedError
	)
	switch {
	case errors.As(err, &timeout):
		return mysql.NewSQLError(rowlock.SQLCodeLockWaitTimeout, "HY000", "%s",
			"Lock wait timeout exceeded; try restarting transaction")
	case errors.As(err, &nowait):
		return mysql.NewSQLError(rowlock.SQLCodeLockNowait, "HY000", "%s",
			"Statement aborted because lock(s) could not be acquired immediately and NOWAIT is set.")
	case errors.As(err, &deadlock):
		ws.abortWith(ctx, err)
		return sqle.ErrLockDeadlock.Wrap(err, "deadlock found when trying to get lock")
	case errors.As(err, &lost):
		ws.abortWith(ctx, err)
		return sqle.ErrLockDeadlock.Wrap(err, "this transaction's row locks were lost")
	case errors.As(err, &scope):
		return &CrossDatabaseTransactionError{Bound: scopeDatabase(scope.Bound), Requested: scopeDatabase(scope.Requested)}
	case errors.As(err, &released):
		if ws.aborted != nil {
			return abortedError(ws.aborted)
		}
	}
	return err
}

// scopeDatabase returns the database half of a "<tenant>/<database>" lock
// scope.
func scopeDatabase(scope string) string {
	if i := strings.LastIndexByte(scope, '/'); i >= 0 {
		return scope[i+1:]
	}
	return scope
}

// abortedError is what a statement in a transaction rolled back as a
// deadlock victim fails with.
func abortedError(cause error) error {
	return sqle.ErrLockDeadlock.Wrap(&TransactionAbortedError{Cause: cause}, "current transaction is aborted")
}

// currentRead returns key's latest committed state, recording it on ws as
// the state the transaction locked it in (see rowLocks.recordCurrent). It
// must only be called once key's lock is granted.
func (t *RelationalTable) currentRead(ctx *sqle.Context, ws *WriteSet, key string) (currentRead, error) {
	if c, ok := ws.currentState(key); ok {
		return c, nil
	}
	if t.mvccSvc == nil {
		return currentRead{}, &RelationalMVCCUnavailableError{Table: t.def.Name}
	}
	var c currentRead
	kv, err := t.mvccSvc.GetRevision(ctx, key)
	switch {
	case err != nil && isNotFound(err):
	case err != nil:
		return currentRead{}, err
	default:
		c = currentRead{value: kv.Value, found: true, modRevision: kv.ModRevision}
	}
	ws.recordCurrent(key, c)
	return c, nil
}

// lockAndRead takes req on keys and returns each key's value as the
// statement sees it once the locks are granted: the latest committed
// version, overlaid by the transaction's own buffered writes. Keys that do
// not exist are omitted.
func (t *RelationalTable) lockAndRead(ctx *sqle.Context, keys []string, req lockRequest) (map[string][]byte, error) {
	if err := t.lockKeys(ctx, keys, req); err != nil {
		return nil, err
	}
	_, ws := lockState(ctx)
	active, err := t.activeWriteSet(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		c, cerr := t.currentRead(ctx, ws, k)
		if cerr != nil {
			return nil, cerr
		}
		if active == nil {
			if c.found {
				out[k] = c.value
			}
			continue
		}
		v, found, lerr := active.Lookup(ctx, k)
		if lerr != nil {
			return nil, lerr
		}
		if found {
			out[k] = v
		}
	}
	if perr := t.confirmGenerationPin(ctx); perr != nil {
		return nil, perr
	}
	return out, nil
}

// lockingScan is a locking read of every row under prefix: the rows are
// discovered at the latest committed version (plus the transaction's own
// buffered and locked keys), locked, and then read again once granted. A
// row inserted under prefix after the discovery is not locked -- gap locks
// are out of scope (see docs/row-locking/).
func (t *RelationalTable) lockingScan(ctx *sqle.Context, prefix string, req lockRequest) (map[string][]byte, error) {
	latest, err := t.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(latest))
	for k := range latest {
		seen[k] = struct{}{}
	}
	if _, ws := lockState(ctx); ws != nil {
		for k := range ws.current {
			if strings.HasPrefix(k, prefix) {
				seen[k] = struct{}{}
			}
		}
		for _, k := range ws.order {
			if strings.HasPrefix(k, prefix) {
				seen[k] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return t.lockAndRead(ctx, keys, req)
}

// lockForWrite takes pessimistic mode's exclusive lock on the keys a write
// is about to occupy and current-reads them, so the write's duplicate check
// and commit precondition see the latest committed state. It is a no-op in
// optimistic mode.
func (t *RelationalTable) lockForWrite(ctx *sqle.Context, keys []string) error {
	req, ok := t.writeLock(ctx)
	if !ok {
		return nil
	}
	if err := t.lockKeys(ctx, keys, req); err != nil {
		return err
	}
	_, ws := lockState(ctx)
	for _, k := range keys {
		if _, err := t.currentRead(ctx, ws, k); err != nil {
			return err
		}
	}
	return nil
}

// lockMatchedRow is pessimistic mode's guard on the row an UPDATE rewrites
// or a DELETE removes. A row the transaction already read under its lock is
// the row the statement matched. A row that reached the write without that
// -- a foreign-key cascade's own read, or any read that took no lock -- is
// locked here and its latest committed version compared with the row the
// statement matched. A row that changed in between, or was deleted, fails
// the statement with ER_LOCK_DEADLOCK rather than overwrite or delete a
// change the statement never saw: waiting for a writer must not turn into
// losing its update.
func (t *RelationalTable) lockMatchedRow(ctx *sqle.Context, rowKey string, old sqle.Row) error {
	_, ws := lockState(ctx)
	if ws == nil {
		return nil
	}
	if _, ok := ws.currentState(rowKey); ok {
		return nil
	}
	if err := t.lockForWrite(ctx, []string{rowKey}); err != nil {
		return err
	}
	c, ok := ws.currentState(rowKey)
	if !ok {
		return nil
	}
	if c.found {
		cur, err := t.decodeRow(rowKey[len(t.gen().dataPrefix):], c.value)
		if err != nil {
			return err
		}
		same, err := cur.Equals(ctx, old, t.schema)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
	}
	return rowChangedError(rowKey)
}

// rowChangedError reports that a row changed between the read an UPDATE or
// DELETE matched it in and the write, as the same retryable ER_LOCK_DEADLOCK
// a lost commit is.
func rowChangedError(key string) error {
	conflict := &TransactionConflictError{Key: key, Err: &service.TransactionConflictError{Key: key}}
	return sqle.ErrLockDeadlock.Wrap(conflict, "a row this statement writes changed after it was read")
}
