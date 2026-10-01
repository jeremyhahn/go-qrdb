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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// rowLocks is the row-lock half of one go-mysql-server transaction's state,
// embedded in its WriteSet so the two share one lifetime: a transaction's
// locks are released exactly when its write set is committed, rolled back or
// discarded. An autocommit statement is its own transaction, so its locks
// last the statement.
//
// Like the rest of WriteSet, everything here is mutated by the one goroutine
// driving the session's current statement -- except releaseLocks, which a
// closing connection may call from another goroutine, and which is why owner
// is behind ownerMu.
type rowLocks struct {
	// concurrency is the session's qrdb_concurrency choice, sampled when the
	// transaction started: "database", "optimistic" or "pessimistic".
	concurrency string

	ownerMu sync.Mutex
	owner   *rowlock.Owner

	// current holds, for every key this transaction locked, the state the
	// key was read in right after its lock was granted (see recordCurrent).
	current map[string]currentRead

	// aborted is the error that rolled this transaction back as a deadlock
	// victim (or for a lost lock): every later statement of the
	// transaction that touches a relational table fails with it until the
	// client ends the transaction.
	aborted error
}

// currentRead is one key's state as read after its lock was granted.
type currentRead struct {
	value       []byte
	found       bool
	modRevision int64
}

// compare is the commit precondition the read establishes: the key must
// still hold the revision it was read at, or still be absent.
func (c currentRead) compare(key string) service.Compare {
	if !c.found {
		return absenceCompare(key)
	}
	return modRevisionCompare(key, c.modRevision)
}

// lockOwner returns the transaction's lock owner, creating it on first use
// as an owner of session's lease: the connection's lease, shared by all of
// its transactions (see rowlock.Session).
func (l *rowLocks) lockOwner(session *rowlock.Session) *rowlock.Owner {
	l.ownerMu.Lock()
	defer l.ownerMu.Unlock()
	if l.owner == nil {
		l.owner = session.NewOwner()
	}
	return l.owner
}

// recordCurrent remembers key's state as read after its lock was granted. A
// key read again later in the transaction is served from here, and its
// commit precondition is derived from here, rather than from the snapshot.
// The first current read wins: the lock has been held since, so nothing a
// lock-respecting writer did can have changed the key, and re-reading could
// only observe a writer that took no lock -- which the commit precondition
// exists to catch.
func (l *rowLocks) recordCurrent(key string, c currentRead) {
	if _, ok := l.current[key]; ok {
		return
	}
	if l.current == nil {
		l.current = make(map[string]currentRead)
	}
	l.current[key] = c
}

// currentState reports the recorded current read of key, if any.
func (l *rowLocks) currentState(key string) (currentRead, bool) {
	c, ok := l.current[key]
	return c, ok
}

// releaseLocks releases every row lock the transaction holds. It is safe
// to call more than once and from any goroutine.
func (l *rowLocks) releaseLocks(ctx context.Context) error {
	l.ownerMu.Lock()
	owner := l.owner
	l.ownerMu.Unlock()
	if owner == nil {
		return nil
	}
	return owner.Release(ctx)
}

// abortWith rolls the transaction back as a deadlock victim or after a lost
// lock: its buffered writes are discarded, its locks released, and cause is
// what every later statement of the transaction fails with.
func (ws *WriteSet) abortWith(ctx context.Context, cause error) {
	ws.entries = make(map[string]*wsEntry)
	ws.order = nil
	ws.undoLog = nil
	ws.reads = nil
	ws.readOrder = nil
	ws.totalBytes = 0
	ws.current = nil
	ws.aborted = cause
	_ = ws.releaseLocks(ctx)
}
