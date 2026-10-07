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

// An explicit _id reaching a collection over SQL (go-qrdb#467), the surface
// go-qrdb#447 could not reach.
//
// Every test here drives real SQL text through pkg/sql/engine into a REAL
// service.CollectionService over the package's memStore, and reads the result
// back through the SERVICE surface -- the one REST, gRPC, QUIC, MCP and the
// SDK all sit on. That is deliberate: the defect is that the two surfaces
// disagreed about which document an insert produced, and a test that asks
// only one of them cannot see a disagreement. The mock-driven tests in
// collection_table_test.go cover the writer's own branches; these cover the
// contract.

package provider

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// collectionHarness wires a real service.CollectionService -- with a real
// SequenceService behind it, so the `increment` counter is the replicated one
// and not the process-local fallback -- to a Provider serving the same
// database over SQL.
type collectionHarness struct {
	store *memStore
	svc   *service.CollectionService
	prov  *Provider
	db    string
}

// newCollectionHarness creates database db with a collection of the given ID
// strategy, seeded with one document so the collection's auto-discovered
// schema has a "kind" column for SQL to name.
func newCollectionHarness(t *testing.T, db, collection, strategy string) *collectionHarness {
	t.Helper()

	store := newMemStore()
	txn := &memTxn{store: store}
	seq := service.NewSequenceService(store, txn)
	svc := service.NewCollectionService(store).WithSequenceService(seq)

	_, err := svc.CreateCollection(context.Background(), &service.CreateCollectionRequest{
		Name:       collection,
		Database:   db,
		IDStrategy: strategy,
		FieldHash:  "kind",
	})
	require.NoError(t, err)

	h := &collectionHarness{store: store, svc: svc, db: db}
	h.insertViaService(t, collection, map[string]interface{}{"kind": "seed"})

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
	h.prov = NewProvider(db, store, WithRegistry(reg))
	return h
}

// insertViaService inserts through the service surface -- what REST, gRPC,
// QUIC, MCP and the SDK all reach.
func (h *collectionHarness) insertViaService(t *testing.T, collection string, doc map[string]interface{}) uint64 {
	t.Helper()
	resp, err := h.svc.InsertDocument(context.Background(), &service.InsertDocumentRequest{
		Collection: collection, Database: h.db, Document: doc,
	})
	require.NoError(t, err)
	return resp.ID
}

// documentAt reads a document back through the service surface.
func (h *collectionHarness) documentAt(collection string, id uint64) (*service.GetDocumentResponse, error) {
	return h.svc.GetDocument(context.Background(), &service.GetDocumentRequest{
		Collection: collection, Database: h.db, ID: id,
	})
}

// documentIDs lists the collection's document IDs through the service
// surface.
func (h *collectionHarness) documentIDs(t *testing.T, collection string) []uint64 {
	t.Helper()
	list, err := h.svc.ListDocuments(context.Background(), &service.ListDocumentsRequest{
		Collection: collection, Database: h.db, PageSize: 1000,
	})
	require.NoError(t, err)
	ids := make([]uint64, 0, len(list.Documents))
	for _, d := range list.Documents {
		ids = append(ids, d.ID)
	}
	return ids
}

// sql runs a statement against the harness's database over the SQL surface.
func (h *collectionHarness) sql(query string) error {
	_, _, err := execQuery(h.prov, h.db, query)
	return err
}

// ---------------------------------------------------------------------------
// The defect
// ---------------------------------------------------------------------------

// TestCollectionSQL_ExplicitIDIsHonoured is the failing-first test for
// go-qrdb#467.
//
// Measured on the commit before this change, with exactly this wiring:
//
//	INSERT INTO events (_id, kind) VALUES (42, 'deploy')  -> no error
//	GetDocument(42)                                       -> not found
//	ListDocuments                                         -> ids [1 2]
//
// The statement succeeded and the document landed under the next generated
// ID. A caller who declared an identity over SQL got no indication it was
// ignored, and the same logical insert produced a different document
// depending on which wire carried it.
func TestCollectionSQL_ExplicitIDIsHonoured(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (42, 'deploy')"))

	got, err := h.documentAt("events", 42)
	require.NoError(t, err, "the document must be readable at the id the SQL statement claimed")
	assert.Equal(t, uint64(42), got.ID)
	assert.Equal(t, "deploy", got.Document["kind"])
	assert.Equal(t, []uint64{1, 42}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_ExplicitIDReservesTheCounter pins the second half of
// go-qrdb#447's contract reaching this surface: a claim made over SQL raises
// the collection's counter, so an allocation made afterwards over ANY
// transport cannot reissue it.
func TestCollectionSQL_ExplicitIDReservesTheCounter(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (42, 'deploy')"))

	assert.Equal(t, uint64(43),
		h.insertViaService(t, "events", map[string]interface{}{"kind": "after"}),
		"the counter must have been advanced past the id SQL claimed")
}

// TestCollectionSQL_CollidingClaimIsRefused pins the third: a claim on a
// taken identity is refused rather than overwriting, and is refused as MySQL
// error 1062 -- the SQL layer's equivalent of REST's 409 and gRPC's
// AlreadyExists.
func TestCollectionSQL_CollidingClaimIsRefused(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	err := h.sql("INSERT INTO events (_id, kind) VALUES (1, 'usurper')")
	require.Error(t, err)

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.EqualValues(t, mysql.ERDupEntry, sqlErr.Num)
	assert.EqualValues(t, 1062, sqlErr.Num)

	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	assert.Equal(t, "seed", got.Document["kind"], "the refused insert must not have overwritten the occupant")
	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_ExplicitIDIsHonouredOnEveryStrategy runs the claim
// against all three ID strategies.
//
// go-qrdb#447's lane found the overwrite was not specific to `increment`, and
// the discard here was not either: the projection that dropped column 0 ran
// before the strategy was ever consulted, so all three surfaced the same way.
// Only `increment` has a counter to reserve; all three must honour the claim.
func TestCollectionSQL_ExplicitIDIsHonouredOnEveryStrategy(t *testing.T) {
	for _, strategy := range []string{
		service.IDStrategyIncrement,
		service.IDStrategyUnixNano,
		service.IDStrategyFieldHash,
	} {
		t.Run(strategy, func(t *testing.T) {
			h := newCollectionHarness(t, "shop", "events", strategy)

			require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (42, 'deploy')"))

			got, err := h.documentAt("events", 42)
			require.NoError(t, err)
			assert.Equal(t, "deploy", got.Document["kind"])
		})
	}
}

// ---------------------------------------------------------------------------
// The sibling write paths
// ---------------------------------------------------------------------------

// TestCollectionSQL_BulkInsertPreservesEveryID covers a multi-row VALUES
// list. It reaches the same Insert, once per row, and every row's claim was
// discarded before this change.
func TestCollectionSQL_BulkInsertPreservesEveryID(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (10, 'a'), (11, 'b'), (12, 'c')"))

	assert.Equal(t, []uint64{1, 10, 11, 12}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_InsertSelectPreservesIDs covers the copy the issue names:
// INSERT ... SELECT between two collection tables, which is what a
// dump/restore through the SQL surface is made of. Before this change it
// produced a copy of every document under a fresh ID, so IDs could not be
// preserved across a restore at all.
func TestCollectionSQL_InsertSelectPreservesIDs(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	h.insertViaService(t, "events", map[string]interface{}{"kind": "second"})

	_, err := h.svc.CreateCollection(context.Background(), &service.CreateCollectionRequest{
		Name: "archive", Database: "shop", IDStrategy: service.IDStrategyIncrement,
	})
	require.NoError(t, err)
	h.insertViaService(t, "archive", map[string]interface{}{"kind": "placeholder"})
	require.NoError(t, h.sql("DELETE FROM archive"))

	require.NoError(t, h.sql("INSERT INTO archive (_id, kind) SELECT _id, kind FROM events"))

	assert.Equal(t, h.documentIDs(t, "events"), h.documentIDs(t, "archive"),
		"a copy through the SQL surface must preserve document ids")
}

// TestCollectionSQL_ReplaceIntoReplaces pins REPLACE INTO. go-mysql-server
// implements it as "insert, and on a primary key violation delete the
// existing row and insert again", so without the refusal it never replaced
// anything -- it inserted a second document under a generated ID and left the
// original untouched.
func TestCollectionSQL_ReplaceIntoReplaces(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("REPLACE INTO events (_id, kind) VALUES (1, 'replaced')"))

	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"), "REPLACE must not leave two documents")
	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	assert.Equal(t, "replaced", got.Document["kind"])
}

// TestCollectionSQL_OnDuplicateKeyUpdateUpdates pins the other statement
// built on the same refusal.
func TestCollectionSQL_OnDuplicateKeyUpdateUpdates(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql(
		"INSERT INTO events (_id, kind) VALUES (1, 'dup') ON DUPLICATE KEY UPDATE kind = 'updated'"))

	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"))
	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Document["kind"])
}

// TestCollectionSQL_UpdateOfTheIDReIdentifies covers UPDATE ... SET _id.
//
// Before this change the writer deleted the old ID and then called
// UpdateDocument for the new one, which requires the document to already
// exist: the statement returned "document not found" AFTER destroying the
// document it was moving, leaving the collection empty.
func TestCollectionSQL_UpdateOfTheIDReIdentifies(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("UPDATE events SET _id = 99 WHERE _id = 1"))

	assert.Equal(t, []uint64{99}, h.documentIDs(t, "events"))
	got, err := h.documentAt("events", 99)
	require.NoError(t, err)
	assert.Equal(t, "seed", got.Document["kind"])
}

// TestCollectionSQL_UpdateOntoATakenIDIsRefused is the same statement where
// the destination is occupied: the move is refused and BOTH documents
// survive.
func TestCollectionSQL_UpdateOntoATakenIDIsRefused(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	h.insertViaService(t, "events", map[string]interface{}{"kind": "second"})

	err := h.sql("UPDATE events SET _id = 2 WHERE _id = 1")
	require.Error(t, err)

	assert.Equal(t, []uint64{1, 2}, h.documentIDs(t, "events"),
		"a refused re-identification must destroy nothing")
}

// TestCollectionSQL_UpdateWithoutChangingTheIDIsStillAnUpdate guards the
// ordinary path from the re-identification branch.
func TestCollectionSQL_UpdateWithoutChangingTheIDIsStillAnUpdate(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("UPDATE events SET kind = 'changed' WHERE _id = 1"))

	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"))
	got, err := h.documentAt("events", 1)
	require.NoError(t, err)
	assert.Equal(t, "changed", got.Document["kind"])
}

// ---------------------------------------------------------------------------
// Allocation: the other half of the contract
// ---------------------------------------------------------------------------

// TestCollectionSQL_OmittedIDAllocatesFromTheStrategy covers the insert that
// makes no claim.
//
// Before this change it was not merely mis-handled, it was impossible:
// go-mysql-server rejected the statement with "Field '_id' doesn't have a
// default value", so the collection's own ID strategy was unreachable over
// SQL. Every SQL insert had to name an ID and every ID it named was thrown
// away.
func TestCollectionSQL_OmittedIDAllocatesFromTheStrategy(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (kind) VALUES ('allocated')"))

	assert.Equal(t, []uint64{1, 2}, h.documentIDs(t, "events"))
}

// TestCollectionSQL_ZeroIDAllocatesFromTheStrategy pins the spelling of "let
// the collection choose" -- MySQL's convention for an AUTO_INCREMENT column,
// and already what the service layer reads a zero "_id" as.
func TestCollectionSQL_ZeroIDAllocatesFromTheStrategy(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (0, 'allocated')"))

	assert.Equal(t, []uint64{1, 2}, h.documentIDs(t, "events"))
}

// ---------------------------------------------------------------------------
// The identity is not a field
// ---------------------------------------------------------------------------

// TestCollectionSQL_ClaimedIDDoesNotBecomeAColumn is why honouring the claim
// needed the service layer to stop storing "_id" as a document field.
//
// The claim travels to the service inside the document, as it does on every
// other transport. updateSchema records every key it is given, so a stored
// "_id" became an entry in the collection's schema, and buildCollectionSchema
// -- which already declares "_id" as column 0 -- then emitted a SECOND column
// with the same name: `SELECT *` returned two `_id` columns, one holding the
// document's real ID and one holding the echo.
func TestCollectionSQL_ClaimedIDDoesNotBecomeAColumn(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)
	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (42, 'deploy')"))

	schema, rows, err := execQuery(h.prov, "shop", "SELECT * FROM events ORDER BY _id")
	require.NoError(t, err)

	names := make([]string, 0, len(schema))
	for _, col := range schema {
		names = append(names, col.Name)
	}
	assert.Equal(t, []string{"_id", "kind"}, names, "the identity is declared exactly once")
	require.Len(t, rows, 2)
	assert.Equal(t, sqle.Row{uint64(42), "deploy"}, rows[1])
}

// TestCollectionSQL_SameDocumentWhicheverWireCarriedIt is the ruling's
// verification stated directly: a document inserted over SQL under an
// explicit id and one inserted over the service surface under an explicit id
// are the same document.
func TestCollectionSQL_SameDocumentWhicheverWireCarriedIt(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	require.NoError(t, h.sql("INSERT INTO events (_id, kind) VALUES (42, 'deploy')"))
	h.insertViaService(t, "events", map[string]interface{}{"_id": uint64(43), "kind": "deploy"})

	overSQL, err := h.documentAt("events", 42)
	require.NoError(t, err)
	overService, err := h.documentAt("events", 43)
	require.NoError(t, err)

	assert.Equal(t, overService.Document, overSQL.Document,
		"the wire an insert arrived on must not change the document it stored")
	assert.Equal(t, map[string]interface{}{"kind": "deploy"}, overSQL.Document,
		"the identity is the document's id, never one of its fields")
}

// ---------------------------------------------------------------------------
// Unit coverage for the pieces the statements above exercise indirectly
// ---------------------------------------------------------------------------

// TestClaimedDocID covers the reading of column 0 on the INSERT path, where
// an absent or zero ID is a legitimate "allocate one" rather than the error
// it is on UPDATE and DELETE.
func TestClaimedDocID(t *testing.T) {
	tests := []struct {
		name    string
		row     sqle.Row
		want    uint64
		wantErr bool
	}{
		{"empty row claims nothing", sqle.Row{}, 0, false},
		{"nil column 0 claims nothing", sqle.Row{nil, "x"}, 0, false},
		{"zero claims nothing", sqle.Row{uint64(0), "x"}, 0, false},
		{"uint64", sqle.Row{uint64(42), "x"}, 42, false},
		{"int64", sqle.Row{int64(42), "x"}, 42, false},
		{"float64", sqle.Row{float64(42), "x"}, 42, false},
		{"decimal string", sqle.Row{"42", "x"}, 42, false},
		{"unparseable string", sqle.Row{"nope", "x"}, 0, true},
		{"unsupported type", sqle.Row{[]byte("42"), "x"}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claimedDocID(tc.row)
			if tc.wantErr {
				require.Error(t, err)
				assert.IsType(t, &InvalidKeyError{}, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBuildCollectionSchema_IdentityDeclaredOnce pins that a collection whose
// stored metadata still carries an "_id" field -- written before the service
// stopped recording it -- yields ONE identity column, not two.
func TestBuildCollectionSchema_IdentityDeclaredOnce(t *testing.T) {
	schema, fieldOrder := buildCollectionSchema("events", map[string]string{
		"_id":  "VARCHAR",
		"kind": "VARCHAR",
		"who":  "VARCHAR",
	})

	names := make([]string, 0, len(schema))
	for _, col := range schema {
		names = append(names, col.Name)
	}
	assert.Equal(t, []string{"_id", "kind", "who"}, names)
	assert.Equal(t, []string{"kind", "who"}, fieldOrder,
		"the identity is not one of the document's fields")
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
}

// TestBuildCollectionSchema_IdentityDefaultsToAllocate pins the DEFAULT that
// makes an insert naming no _id legal.
func TestBuildCollectionSchema_IdentityDefaultsToAllocate(t *testing.T) {
	schema, _ := buildCollectionSchema("events", map[string]string{"kind": "VARCHAR"})

	require.NotNil(t, schema[0].Default, "without a default the column cannot be omitted")
	val, err := schema[0].Default.Eval(newTestContext(), nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), val, "zero is the spelling of 'let the collection allocate'")
}

// TestCollectionWriter_DuplicateKeyErrorIsTransientWhenTheOccupantIsGone
// covers the retry branch: the occupying document was deleted between the
// refusal and the read that would have described it, so there is no duplicate
// to report.
func TestCollectionWriter_DuplicateKeyErrorIsTransientWhenTheOccupantIsGone(t *testing.T) {
	w := &collectionWriter{
		collectionSvc: &mockCollectionService{
			getFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
				return nil, &service.CollectionDocumentNotFoundError{ID: req.ID}
			},
		},
		database: "shop", collection: "events", fieldOrder: []string{"kind"},
	}

	assert.Nil(t, w.duplicateKeyError(newTestContext(), 7),
		"an occupant that is gone is not a duplicate")
}

// TestCollectionWriter_DuplicateKeyErrorCarriesTheExistingRow pins the row
// go-mysql-server needs: REPLACE feeds it straight into Delete, and ON
// DUPLICATE KEY UPDATE into Update, so its shape must match the table's.
func TestCollectionWriter_DuplicateKeyErrorCarriesTheExistingRow(t *testing.T) {
	w := &collectionWriter{
		collectionSvc: &mockCollectionService{
			getFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
				return &service.GetDocumentResponse{
					ID: req.ID, Document: map[string]interface{}{"kind": "occupant"},
				}, nil
			},
		},
		database: "shop", collection: "events", fieldOrder: []string{"kind"},
	}

	dupErr := w.duplicateKeyError(newTestContext(), 7)
	require.Error(t, dupErr)
	assert.True(t, sqle.ErrPrimaryKeyViolation.Is(dupErr))

	sqlErr := sqle.CastSQLError(dupErr)
	require.NotNil(t, sqlErr)
	assert.EqualValues(t, mysql.ERDupEntry, sqlErr.Num)
}

// TestCollectionWriter_InsertGivesUpAfterSustainedContention covers the
// bounded retry's exit: a service that refuses every insert while the
// occupant is never readable must not spin.
func TestCollectionWriter_InsertGivesUpAfterSustainedContention(t *testing.T) {
	attempts := 0
	w := &collectionWriter{
		collectionSvc: &mockCollectionService{
			insertFn: func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
				attempts++
				return nil, &service.CollectionDocumentExistsError{
					Database: req.Database, Collection: req.Collection, ID: 7, Explicit: true,
				}
			},
			getFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
				return nil, &service.CollectionDocumentNotFoundError{ID: req.ID}
			},
		},
		database: "shop", collection: "events", fieldOrder: []string{"kind"},
	}

	err := w.Insert(newTestContext(), sqle.Row{uint64(7), "x"})
	assert.IsType(t, &TransactionConflictRetriesExhaustedError{}, err)
	assert.Equal(t, writeMaxAttempts, attempts)
}

// TestCollectionWriter_InsertReportsANonCollisionErrorUnchanged guards
// against the retry loop swallowing anything but a collision.
func TestCollectionWriter_InsertReportsANonCollisionErrorUnchanged(t *testing.T) {
	refusal := &service.CollectionEmptyDocumentError{}
	w := &collectionWriter{
		collectionSvc: &mockCollectionService{
			insertFn: func(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
				return nil, refusal
			},
		},
		database: "shop", collection: "events", fieldOrder: []string{"kind"},
	}

	err := w.Insert(newTestContext(), sqle.Row{uint64(7), "x"})
	require.ErrorIs(t, err, error(refusal))
}

// TestCollectionWriter_InsertRejectsAnUnreadableID pins that a column 0 that
// is present but not an ID fails the statement rather than being treated as
// no claim.
func TestCollectionWriter_InsertRejectsAnUnreadableID(t *testing.T) {
	w := &collectionWriter{
		collectionSvc: &mockCollectionService{
			insertFn: func(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
				t.Fatal("the insert must not reach the service")
				return nil, nil
			},
		},
		database: "shop", collection: "events", fieldOrder: []string{"kind"},
	}

	err := w.Insert(newTestContext(), sqle.Row{"not-an-id", "x"})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// TestDocumentToRow covers the projection the read and write paths share.
func TestDocumentToRow(t *testing.T) {
	row := documentToRow([]string{"kind", "who"}, 42, map[string]interface{}{
		"kind": "deploy",
		"tags": []interface{}{"a"}, // not in fieldOrder: not projected
	})
	assert.Equal(t, sqle.Row{uint64(42), "deploy", nil}, row)
}

// TestDocumentToRow_NoFields covers a collection whose schema has not yet
// discovered anything.
func TestDocumentToRow_NoFields(t *testing.T) {
	assert.Equal(t, sqle.Row{uint64(7)}, documentToRow(nil, 7, map[string]interface{}{"kind": "x"}))
}

// TestCollectionWriter_EveryWriteVerbIsAutocommitOnly pins that the
// autocommit gate covers all three write verbs, not merely the one a test
// happened to reach. A collection table has no write-set to buffer into, so a
// write issued inside an open transaction must fail loudly rather than escape
// the transaction's boundary and survive a ROLLBACK.
func TestCollectionWriter_EveryWriteVerbIsAutocommitOnly(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "shop", "events",
		map[string]string{"kind": "VARCHAR"}, nil)

	verbs := map[string]func(ctx *sqle.Context) error{
		"insert": func(ctx *sqle.Context) error {
			return tbl.Inserter(ctx).Insert(ctx, sqle.Row{uint64(1), "x"})
		},
		"delete": func(ctx *sqle.Context) error {
			return tbl.Deleter(ctx).Delete(ctx, sqle.Row{uint64(1), "x"})
		},
		"update": func(ctx *sqle.Context) error {
			return tbl.Updater(ctx).Update(ctx, sqle.Row{uint64(1), "x"}, sqle.Row{uint64(1), "y"})
		},
	}
	for name, verb := range verbs {
		t.Run(name, func(t *testing.T) {
			ctx, _ := newTxContext(true)
			err := verb(ctx)
			var nonTx *NonTransactionalTableError
			require.ErrorAs(t, err, &nonTx)
			assert.Equal(t, "events", nonTx.Table)
		})
	}
}

// TestCollectionSQL_UpdateToZeroIDIsRefused pins that zero is a claim only on
// INSERT. An UPDATE names the document it writes, and allocating a fresh ID
// for a statement that said `_id = 0` would substitute an identity silently --
// the defect this change removes, one statement over.
func TestCollectionSQL_UpdateToZeroIDIsRefused(t *testing.T) {
	h := newCollectionHarness(t, "shop", "events", service.IDStrategyIncrement)

	err := h.sql("UPDATE events SET _id = 0 WHERE _id = 1")
	require.Error(t, err)
	assert.Equal(t, []uint64{1}, h.documentIDs(t, "events"), "nothing may move or vanish")
}

// TestCollectionWriter_UpdateToZeroIDIsRefused is the same refusal at the
// writer, where the error type is visible.
func TestCollectionWriter_UpdateToZeroIDIsRefused(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "shop", "events",
		map[string]string{"kind": "VARCHAR"}, nil)
	ctx := newTestContext()

	err := tbl.Updater(ctx).Update(ctx, sqle.Row{uint64(1), "x"}, sqle.Row{uint64(0), "x"})
	assert.IsType(t, &InvalidKeyError{}, err)
}
