//go:build qrdb_sql

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

// REPLACE INTO and INSERT ... ON DUPLICATE KEY UPDATE on a collection table,
// under concurrency (go-qrdb#497).
//
// go-mysql-server drives both statements as several calls -- insert, then on a
// primary key violation a delete or an update -- and a collection table is
// autocommit-only, so nothing held those calls together. These tests drive
// real SQL into a real service.CollectionService and measure what the
// statements report against what the store holds.

package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	gms "github.com/dolthub/go-mysql-server"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlengine "github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
)

// hookedDocStore is the faithful memStore with one hook: it runs once, just
// after the first document delete commits. That is the moment the old
// three-call REPLACE was exposed -- its own delete done, its re-insert not yet
// issued -- so the hook can run a second statement exactly there.
type hookedDocStore struct {
	*memStore
	mu          sync.Mutex
	afterDelete func()
}

func (h *hookedDocStore) Delete(ctx context.Context, key string) error {
	err := h.memStore.Delete(ctx, key)
	if err == nil && strings.HasPrefix(key, "__col:") {
		h.mu.Lock()
		hook := h.afterDelete
		h.afterDelete = nil
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	return err
}

// newHookedCollectionHarness is newCollectionHarness over a hookedDocStore.
func newHookedCollectionHarness(t *testing.T) (*collectionHarness, *hookedDocStore) {
	t.Helper()
	store := newMemStore()
	hooked := &hookedDocStore{memStore: store}
	txn := &memTxn{store: store}
	seq := service.NewSequenceService(store, txn)
	svc := service.NewCollectionService(hooked).WithSequenceService(seq)
	_, err := svc.CreateCollection(context.Background(), &service.CreateCollectionRequest{
		Name: "events", Database: "shop", IDStrategy: service.IDStrategyIncrement,
	})
	require.NoError(t, err)
	h := &collectionHarness{store: store, svc: svc, db: "shop"}
	h.insertViaService(t, "events", map[string]interface{}{"kind": "seed"})
	reg := &fakeRelationalRegistry{
		collectionSvc: svc,
		kvSvc:         store,
		mvccSvc:       store,
		txnSvc:        txn,
		seqSvc:        seq,
		batchSvc:      &memBatch{store: store},
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return service.NewTableService(store, store, txn, seq), nil
		},
	}
	h.prov = NewProviderWithEngine("shop", store, "", WithRegistry(reg))
	return h, hooked
}

// affectedRows runs a DML statement and returns the rows it reports.
func affectedRows(h *collectionHarness, query string) (uint64, error) {
	_, rows, err := execQuery(h.prov, h.db, query)
	if err != nil {
		return 0, err
	}
	return okRows(rows)
}

// affectedRowsOn runs a DML statement on a shared engine, in a session of its
// own -- the shape concurrent client connections have.
func affectedRowsOn(eng *gms.Engine, db, query string) (uint64, error) {
	sqlCtx := sqle.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase(db)
	_, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return 0, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(sqlCtx)
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			_ = iter.Close(sqlCtx)
			return 0, nerr
		}
		rows = append(rows, row)
	}
	if err := iter.Close(sqlCtx); err != nil {
		return 0, err
	}
	return okRows(rows)
}

// isRetryableConflict reports whether err reaches a client as MySQL error
// 1213 (ER_LOCK_DEADLOCK / SQLSTATE 40001), the code a client retries on.
func isRetryableConflict(err error) bool {
	return err != nil && sqle.CastSQLError(err).Num == mysql.ERLockDeadlock
}

// unexpectedDMLResultError reports a DML statement that did not answer with
// a single OK result.
type unexpectedDMLResultError struct{ rows []sqle.Row }

func (e *unexpectedDMLResultError) Error() string {
	return "unexpected DML result: " + fmt.Sprint(e.rows)
}

// okRows extracts the affected-rows count from a DML result.
func okRows(rows []sqle.Row) (uint64, error) {
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, &unexpectedDMLResultError{rows: rows}
	}
	ok, isOK := rows[0][0].(types.OkResult)
	if !isOK {
		return 0, &unexpectedDMLResultError{rows: rows}
	}
	return ok.RowsAffected, nil
}

// TestCollectionSQL_ReplaceIntoReportsReplacementAsTwoRows pins the affected
// rows count MySQL gives REPLACE: two for a replacement, one for an insert.
func TestCollectionSQL_ReplaceIntoReportsReplacementAsTwoRows(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	n, err := affectedRows(h, "REPLACE INTO events (_id, kind) VALUES (1, 'replaced')")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), n, "replacing document 1 affects two rows")
	n, err = affectedRows(h, "REPLACE INTO events (_id, kind) VALUES (50, 'new')")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), n, "inserting document 50 affects one row")
}

// TestCollectionSQL_ReplaceIntoIsOneGuardedWrite forces the interleaving the
// old implementation was open to. Statement A replaces document 1; the moment
// its delete of the existing row committed, statement B replaces the same
// document. Under the old three-call REPLACE, B found the ID free, INSERTED,
// and was told one row -- and then A's re-insert collided with B's document,
// deleted it and wrote over it: B's value gone, and B told it had inserted
// into a table where document 1 had existed all along.
//
// Measured against the tree before this change: B reports 1 affected row and
// the final document is A's. With the guarded upsert there is no delete to
// interleave with, both statements report the replacement they made, and the
// last write is the one that survives.
func TestCollectionSQL_ReplaceIntoIsOneGuardedWrite(t *testing.T) {
	h, hooked := newHookedCollectionHarness(t)

	var bRows uint64
	var bErr error
	hooked.afterDelete = func() {
		bRows, bErr = affectedRows(h, "REPLACE INTO events (_id, kind) VALUES (1, 'from-b')")
	}

	aRows, aErr := affectedRows(h, "REPLACE INTO events (_id, kind) VALUES (1, 'from-a')")
	require.NoError(t, aErr)
	if hooked.afterDelete == nil {
		// The old path ran B inside A.
		require.NoError(t, bErr)
	} else {
		// The guarded path deletes nothing; run B after A.
		hooked.afterDelete = nil
		bRows, bErr = affectedRows(h, "REPLACE INTO events (_id, kind) VALUES (1, 'from-b')")
		require.NoError(t, bErr)
	}

	assert.Equal(t, uint64(2), aRows, "A replaced the seed")
	assert.Equal(t, uint64(2), bRows, "document 1 existed throughout; B replaced, it did not insert")
	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	assert.Equal(t, "from-b", got.Document["kind"], "the statement that finished last must be the survivor")
	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_ConcurrentReplaceInto runs N REPLACE INTO statements on
// one existing _id at once. Every statement must replace (the document exists
// throughout, so none may report an insert) or be refused with 1213, and the
// survivor must be one writer's document in full.
func TestCollectionSQL_ConcurrentReplaceInto(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	const writers = 16
	eng := sqlengine.New(sqlengine.Config{Provider: h.prov})
	defer func() { _ = eng.Close() }()

	rows := make([]uint64, writers)
	errs := make([]error, writers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < writers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			rows[i], errs[i] = affectedRowsOn(eng, h.db, fmt.Sprintf("REPLACE INTO events (_id, kind) VALUES (1, 'w%d')", i))
		}(i)
	}
	start.Done()
	done.Wait()

	written := map[string]bool{}
	for i := 0; i < writers; i++ {
		if errs[i] != nil {
			require.True(t, isRetryableConflict(errs[i]), "writer %d: only a retryable conflict may refuse, got %v", i, errs[i])
			continue
		}
		assert.Equal(t, uint64(2), rows[i], "writer %d reported an insert over an existing document", i)
		written[fmt.Sprintf("w%d", i)] = true
	}
	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	kind, _ := got.Document["kind"].(string)
	assert.True(t, written[kind], "the survivor %q must be a writer that was told it succeeded", kind)
	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_OnDuplicateKeyUpdateLosesLoudly pins the other statement.
// Its update expressions are evaluated over the row the insert reported; if a
// concurrent writer changes the document before the update lands, the update
// is refused with 1213 rather than overwriting a value it never saw.
func TestCollectionSQL_OnDuplicateKeyUpdateLosesLoudly(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	var raced sync.Once

	// Change document 1 between ODKU's duplicate read and its update, by
	// writing through the service the moment the SQL surface reads it.
	reads := 0
	hookedSvc := &racingCollectionService{CollectionServicer: h.svc, onGet: func() {
		reads++
		if reads == 2 { // 1: the duplicate row; 2: the update's guard read
			raced.Do(func() {
				_, err := h.svc.UpdateDocument(context.Background(), &service.UpdateDocumentRequest{
					Database: "shop", Collection: "events", ID: 1, Document: map[string]interface{}{"kind": "concurrent"},
				})
				require.NoError(t, err)
			})
		}
	}}
	h.prov = NewProviderWithEngine("shop", h.store, "", WithRegistry(&fakeRelationalRegistry{
		collectionSvc: hookedSvc,
		kvSvc:         h.store,
		mvccSvc:       h.store,
		txnSvc:        &memTxn{store: h.store},
		batchSvc:      &memBatch{store: h.store},
	}))

	_, err := affectedRows(h, "INSERT INTO events (_id, kind) VALUES (1, 'dup') ON DUPLICATE KEY UPDATE kind = CONCAT(kind, '+1')")
	require.Error(t, err)
	assert.True(t, isRetryableConflict(err), "got %v", err)
	got, gerr := h.documentAt("events", 1)
	require.NoError(t, gerr)
	assert.Equal(t, "concurrent", got.Document["kind"], "the concurrent write must survive")
}

// racingCollectionService runs a hook before every GetDocument, so a test can
// land a concurrent write between a statement's read and its guarded write.
type racingCollectionService struct {
	service.CollectionServicer
	onGet func()
}

func (r *racingCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	resp, err := r.CollectionServicer.GetDocument(ctx, req)
	r.onGet()
	return resp, err
}
