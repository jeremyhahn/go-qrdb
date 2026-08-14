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
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newTxContext builds a *sqle.Context carrying a fresh *QRDBSession, with
// GetIgnoreAutoCommit optionally set to simulate being inside an explicit
// BEGIN.
func newTxContext(ignoreAutoCommit bool) (*sqle.Context, *QRDBSession) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetIgnoreAutoCommit(ignoreAutoCommit)
	return ctx, sess
}

func TestQRDBSession_ImplementsTransactionSession(t *testing.T) {
	var _ sqle.TransactionSession = (*QRDBSession)(nil)
}

func TestQRDBSession_StartTransaction_CreatesUnboundWriteSet(t *testing.T) {
	ctx, sess := newTxContext(true)

	tx, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.False(t, tx.IsReadOnly())
	assert.NotEmpty(t, tx.String())

	ws := sess.writeSet()
	require.NotNil(t, ws)
	assert.False(t, ws.Bound())
}

func TestQRDBSession_StartTransaction_ReadOnly(t *testing.T) {
	ctx, sess := newTxContext(true)

	tx, err := sess.StartTransaction(ctx, sqle.ReadOnly)
	require.NoError(t, err)
	assert.True(t, tx.IsReadOnly())
	assert.True(t, sess.writeSet().ReadOnly())
}

func TestQRDBSession_CommitTransaction_NilWriteSet_IsNoop(t *testing.T) {
	ctx, sess := newTxContext(true)
	require.NoError(t, sess.CommitTransaction(ctx, nil))
}

func TestQRDBSession_CommitTransaction_UnboundWriteSet_IsNoop(t *testing.T) {
	ctx, sess := newTxContext(true)
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	require.NoError(t, sess.CommitTransaction(ctx, nil))
	assert.Nil(t, sess.writeSet())
}

func TestQRDBSession_CommitTransaction_Success_ClearsWriteSet(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &memTxn{store: store}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	require.NoError(t, sess.CommitTransaction(ctx, nil))
	assert.Nil(t, sess.writeSet())

	got, err := store.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), got)
}

// conflictTxn wraps memTxn but always reports a failed compare-and-swap,
// simulating a concurrent commit that won the race.
type conflictTxn struct {
	service.TransactionServicer
}

func (c *conflictTxn) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{Succeeded: false}, nil
}

func TestQRDBSession_CommitTransaction_Conflict_MapsToLockDeadlock(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &conflictTxn{}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	err = sess.CommitTransaction(ctx, nil)
	require.Error(t, err)
	assert.True(t, sqle.ErrLockDeadlock.Is(err), "conflict must map to sql.ErrLockDeadlock (MySQL 1213 / SQLSTATE 40001)")

	// The write-set is cleared: a conflict is unrecoverable for this
	// transaction, matching MySQL/InnoDB auto-rolling-back on deadlock.
	assert.Nil(t, sess.writeSet())
}

// TestQRDBSession_CommitTransaction_Conflict_MessageIsRenderable guards the
// text a client actually reads, not just the error code. sql.ErrLockDeadlock
// is a go-errors Kind whose message carries one %s, and Kind.Wrap appends a
// further ": %s" for the cause; wrapping without supplying a value left both
// verbs unfilled, so the wire carried
//
//	serialization failure: %!!(string=...)s(MISSING), try restarting transaction.: %!s(MISSING)
//
// The MySQL error number was right throughout, which is exactly why this went
// unnoticed -- retry logic keyed on 1213/40001 worked while every human and
// every log line saw formatting garbage.
// CommitTransaction's empty-key fallback is deliberately not exercised here:
// it is unreachable through this path, because a conflict can only be
// reported for a write-set that buffered at least one write, and that write
// is what puts an entry in ws.order. The fallback exists so the message stays
// renderable if that ever stops holding.
func TestQRDBSession_CommitTransaction_Conflict_MessageIsRenderable(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", &conflictTxn{}, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	err = sess.CommitTransaction(ctx, nil)
	require.Error(t, err)

	msg := err.Error()
	assert.NotContains(t, msg, "%!", "conflict message must not carry fmt verb artifacts: %s", msg)
	assert.NotContains(t, msg, "MISSING", "conflict message must not carry fmt verb artifacts: %s", msg)
	assert.Contains(t, msg, "serialization failure")
	assert.Contains(t, msg, "try restarting transaction")
	assert.Contains(t, msg, "k1", "the conflicting key must be named: %s", msg)
}

// errTxn always fails Execute with a plain infrastructure error.
type errTxn struct {
	service.TransactionServicer
}

var errBoom = errors.New("boom")

func (e *errTxn) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return nil, errBoom
}

func TestQRDBSession_CommitTransaction_InfraError_PreservesWriteSet(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &errTxn{}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	err = sess.CommitTransaction(ctx, nil)
	require.ErrorIs(t, err, errBoom)

	// The write-set must survive a transient infra error so a retried
	// COMMIT can still succeed without losing buffered work.
	assert.Same(t, ws, sess.writeSet())
}

func TestQRDBSession_Rollback_DiscardsWriteSet(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &memTxn{store: store}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	require.NoError(t, sess.Rollback(ctx, nil))
	assert.Nil(t, sess.writeSet())

	_, err = store.Get(ctx, "k1")
	assert.Error(t, err, "a rolled-back write must never have been proposed")
}

func TestQRDBSession_Savepoints(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &memTxn{store: store}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	require.NoError(t, sess.CreateSavepoint(ctx, nil, "sp1"))
	require.NoError(t, ws.Put(ctx, "k2", []byte("v2")))

	require.NoError(t, sess.RollbackToSavepoint(ctx, nil, "sp1"))
	_, found, err := ws.Lookup(ctx, "k2")
	require.NoError(t, err)
	assert.False(t, found, "k2 was written after the savepoint and must be undone")
	v1, found, err := ws.Lookup(ctx, "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v1"), v1)

	require.NoError(t, sess.ReleaseSavepoint(ctx, nil, "sp1"))
	err = sess.ReleaseSavepoint(ctx, nil, "sp1")
	require.Error(t, err, "releasing an already-released savepoint must fail")
}

func TestQRDBSession_SavepointMethods_NoWriteSet(t *testing.T) {
	ctx, sess := newTxContext(true)

	// CreateSavepoint is inert with no write-set at all.
	require.NoError(t, sess.CreateSavepoint(ctx, nil, "sp1"))

	// RollbackToSavepoint/ReleaseSavepoint report a typed not-found error.
	err := sess.RollbackToSavepoint(ctx, nil, "sp1")
	var notFound *SavepointNotFoundError
	assert.ErrorAs(t, err, &notFound)

	err = sess.ReleaseSavepoint(ctx, nil, "sp1")
	assert.ErrorAs(t, err, &notFound)
}

func TestFlushPendingWriteSet_NonQRDBSession_IsNoop(t *testing.T) {
	ctx := sqle.NewContext(context.Background())
	require.NoError(t, flushPendingWriteSet(ctx))
}

func TestFlushPendingWriteSet_CommitsPendingBuffer(t *testing.T) {
	ctx, sess := newTxContext(true)
	store := newMemStore()
	txn := &memTxn{store: store}

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", txn, store))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))

	require.NoError(t, flushPendingWriteSet(ctx))
	assert.Nil(t, sess.writeSet())

	got, err := store.Get(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), got)
}

func TestRequireAutocommit_AllowsAutocommitStatement(t *testing.T) {
	ctx, _ := newTxContext(false)
	assert.NoError(t, requireAutocommit(ctx, "kv"))
}

func TestRequireAutocommit_BlocksInsideExplicitTransaction(t *testing.T) {
	ctx, _ := newTxContext(true)
	err := requireAutocommit(ctx, "kv")
	var nonTx *NonTransactionalTableError
	require.ErrorAs(t, err, &nonTx)
	assert.Equal(t, "kv", nonTx.Table)
}

func TestRequireAutocommit_NonQRDBSession_IsNoop(t *testing.T) {
	ctx := sqle.NewContext(context.Background())
	ctx.SetIgnoreAutoCommit(true)
	assert.NoError(t, requireAutocommit(ctx, "kv"))
}
