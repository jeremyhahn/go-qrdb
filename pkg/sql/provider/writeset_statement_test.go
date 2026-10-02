// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lookupFound reports whether ws's own view holds key.
func lookupFound(t *testing.T, ws *WriteSet, key string) bool {
	t.Helper()
	_, found, err := ws.Lookup(context.Background(), key)
	require.NoError(t, err)
	return found
}

func TestWriteSetStatement_DiscardUndoesOnlyTheStatement(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()
	require.NoError(t, ws.Put(ctx, "before", []byte("v")))

	ws.beginStatement()
	ws.beginStatement() // a second editor of the same statement
	require.NoError(t, ws.Put(ctx, "a", []byte("1")))
	require.NoError(t, ws.Put(ctx, "before", []byte("changed")))
	ws.discardStatement()
	ws.discardStatement()

	assert.False(t, lookupFound(t, ws, "a"))
	v, found, err := ws.Lookup(ctx, "before")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v"), v)
	assert.Empty(t, ws.statements)
	assert.Len(t, ws.Ops(), 1)
}

func TestWriteSetStatement_CompleteKeepsTheStatement(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()

	ws.beginStatement()
	require.NoError(t, ws.Put(ctx, "a", []byte("1")))
	ws.completeStatement()

	// A later failed statement does not reach back past a completed one.
	ws.beginStatement()
	require.NoError(t, ws.Put(ctx, "b", []byte("2")))
	ws.discardStatement()

	assert.True(t, lookupFound(t, ws, "a"))
	assert.False(t, lookupFound(t, ws, "b"))
}

func TestWriteSetStatement_PerRowCheckpointsInsideAStatement(t *testing.T) {
	// INSERT IGNORE: each row is bracketed on its own.
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()
	for i, key := range []string{"r1", "r2", "r3"} {
		ws.beginStatement()
		require.NoError(t, ws.Put(ctx, key, []byte("x")))
		if i == 1 {
			ws.discardStatement()
			continue
		}
		ws.completeStatement()
	}
	assert.True(t, lookupFound(t, ws, "r1"))
	assert.False(t, lookupFound(t, ws, "r2"))
	assert.True(t, lookupFound(t, ws, "r3"))
}

func TestWriteSetStatement_UnbalancedCallsAreHarmless(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()
	require.NoError(t, ws.Put(ctx, "a", []byte("1")))

	ws.discardStatement()
	ws.completeStatement()
	assert.True(t, lookupFound(t, ws, "a"), "a discard with no open statement undoes nothing")
}

func TestWriteSetStatement_MarkPastAnEmptiedLog(t *testing.T) {
	// A deadlock victim's write-set is emptied mid-statement; the statement's
	// later discard has nothing to undo and must not slice past the log.
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()
	require.NoError(t, ws.Put(ctx, "a", []byte("1")))
	ws.beginStatement()
	require.NoError(t, ws.Put(ctx, "b", []byte("2")))
	ws.abortWith(ctx, &TransactionAbortedError{})

	assert.NotPanics(t, ws.discardStatement)
	assert.Empty(t, ws.Ops())
}

func TestWriteSetStatement_ResetDropsLeftoverMarks(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ctx := context.Background()
	ws.beginStatement() // never closed
	require.NoError(t, ws.Put(ctx, "a", []byte("1")))
	ws.resetStatements()

	ws.discardStatement()
	assert.True(t, lookupFound(t, ws, "a"), "a leftover mark must not roll back a later command's statement")
}

func TestStatementWriteSet_OnlyInsideATransaction(t *testing.T) {
	assert.Nil(t, statementWriteSet(nil))

	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	assert.Nil(t, statementWriteSet(ctx), "autocommit statement")

	ctx.SetIgnoreAutoCommit(true)
	ws := statementWriteSet(ctx)
	require.NotNil(t, ws)
	assert.Same(t, sess.writeSet(), ws)

	other := sqle.NewContext(context.Background(), sqle.WithSession(sqle.NewBaseSession()))
	other.SetIgnoreAutoCommit(true)
	assert.Nil(t, statementWriteSet(other), "not a QRDB session")
}

func TestSessionCommandBegin_ResetsStatementMarks(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	require.NoError(t, sess.CommandBegin(), "no transaction yet")
	sess.CommandEnd()

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NotNil(t, ws)
	ws.beginStatement()
	require.NoError(t, sess.CommandBegin())
	assert.Empty(t, ws.statements)
}

// TestSessionCommandBegin_RefusesWhileACommandIsOpen: a command cannot begin
// inside another -- it would drop the open statement's savepoint and serve
// the open command's resolutions -- and the session accepts the next command
// once the open one ends.
func TestSessionCommandBegin_RefusesWhileACommandIsOpen(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	require.NoError(t, sess.CommandBegin())
	ws := sess.writeSet()
	ws.beginStatement()
	scope := sess.stmt.Load()

	var inProgress *CommandInProgressError
	require.ErrorAs(t, sess.CommandBegin(), &inProgress)
	assert.NotEmpty(t, ws.statements, "the open statement keeps its savepoint")
	assert.Same(t, scope, sess.stmt.Load(), "the open command keeps its resolution scope")
	assert.NotEmpty(t, inProgress.Error())

	sess.CommandEnd()
	require.NoError(t, sess.CommandBegin(), "the next command begins once the open one ends")
	sess.CommandEnd()
}
