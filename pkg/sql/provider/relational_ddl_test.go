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

package provider

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// CREATE TABLE
// ---------------------------------------------------------------------------

func TestDatabase_CreateTable_RoundTripsThroughCatalogAndIsQueryable(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255) NOT NULL)")

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Equal(t, "users", def.Name)
	require.Len(t, def.Columns, 2)
	assert.Equal(t, "id", def.Columns[0].Name)
	assert.Equal(t, "email", def.Columns[1].Name)
	require.Len(t, def.PrimaryKey, 1)
	assert.Equal(t, "id", def.PrimaryKey[0].Name)

	// Queryable with zero rows.
	schema, rows := mustExecQuery(t, prov, "shop", "SELECT * FROM users")
	require.Len(t, schema, 2)
	assert.Empty(t, rows)

	// Seed a row directly (DML is a later phase) and confirm it becomes
	// visible through the same table -- proving CreateTable produced a
	// genuinely queryable table, not just a catalog entry.
	rowCodec, err := catalog.NewRowCodec(def)
	require.NoError(t, err)
	keyCodec, err := catalog.NewKeyCodec(def)
	require.NoError(t, err)
	encodedRow, err := rowCodec.Encode(sqle.Row{uint64(1), "alice@example.com"})
	require.NoError(t, err)
	encodedKey, err := keyCodec.EncodeValues([]any{uint64(1)})
	require.NoError(t, err)
	require.NoError(t, h.kvSvc.Put(context.Background(), service.TableDataPrefix("shop", def.TableID, 0)+encodedKey, encodedRow))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT * FROM users")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0])
	assert.Equal(t, "alice@example.com", rows[0][1])
}

func TestDatabase_CreateTable_PrefixLengthPrimaryKey(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		"CREATE TABLE docs (title VARCHAR(500) NOT NULL, PRIMARY KEY (title(10)))")

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "docs")
	require.NoError(t, err)
	require.Len(t, def.PrimaryKey, 1)
	assert.Equal(t, "title", def.PrimaryKey[0].Name)
	assert.Equal(t, uint16(10), def.PrimaryKey[0].PrefixLength)
}

func TestDatabase_CreateTable_Duplicate(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY)")

	_, _, err := execQuery(prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY)")
	require.Error(t, err)
	var exists *service.TableAlreadyExistsError
	require.ErrorAs(t, err, &exists)
}

func TestDatabase_CreateTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.CreateTable(newTestContext(), "users", sqle.PrimaryKeySchema{}, sqle.Collation_Default, "")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_CreateTable_RejectsUnmappableSchema(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")

	schema := sqle.NewPrimaryKeySchema(sqle.Schema{
		{Name: "g", Type: types.GeometryType{}, Nullable: true, Source: "shapes"},
	})
	err := db.CreateTable(newTestContext(), "shapes", schema, sqle.Collation_Default, "")
	var e *catalog.SpatialTypeUnsupportedError
	require.ErrorAs(t, err, &e)

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "shapes")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound, "a schema-invalid CREATE TABLE must never reach the catalog")
}

func TestDatabase_CreateTable_RejectsGeneratedColumn(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")

	def, err := sqle.NewColumnDefaultValue(nil, types.Int64, false, false, true)
	require.NoError(t, err)
	schema := sqle.NewPrimaryKeySchema(sqle.Schema{
		{Name: "id", Type: types.Int64, Nullable: false, Source: "t", PrimaryKey: true},
		{Name: "computed", Type: types.Int64, Nullable: true, Source: "t", Generated: def},
	})
	err = db.CreateTable(newTestContext(), "t", schema, sqle.Collation_Default, "")
	var e *GeneratedColumnUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// DROP TABLE
// ---------------------------------------------------------------------------

func TestDatabase_DropTable_ReclaimsRowsAndIndexEntries(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)

	// Seed rows and a physical secondary-index entry directly.
	dataPrefix := service.TableDataPrefix("shop", def.TableID, 0)
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row1", []byte("data1")))
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row2", []byte("data2")))

	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))
	def, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	indexPrefix := service.TableIndexPrefix("shop", def.TableID, 0, incarnationOf(def, "idx_email"))
	require.NoError(t, h.kvSvc.Put(context.Background(), indexPrefix+"entry1", []byte("row1")))

	mustExecQuery(t, prov, "shop", "DROP TABLE users")

	// The drop publishes the reclaim; it does not delete inline. A statement
	// that resolved the table before the drop may still be reading it, so
	// the data stays whole until the sweeper is past ReclaimQuiescence.
	rows, err := h.kvSvc.Scan(context.Background(), dataPrefix)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "no reader may see a partially-deleted table")

	sweepPastQuiescence(t, h)

	rows, err = h.kvSvc.Scan(context.Background(), dataPrefix)
	require.NoError(t, err)
	assert.Empty(t, rows, "DROP TABLE must reclaim every row")

	idxRows, err := h.kvSvc.Scan(context.Background(), indexPrefix)
	require.NoError(t, err)
	assert.Empty(t, idxRows, "DROP TABLE must reclaim every secondary-index entry")

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestDatabase_DropTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.DropTable(newTestContext(), "users")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_DropTable_NotFound(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	_, _, err := execQuery(prov, "shop", "DROP TABLE users")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// RENAME TABLE
// ---------------------------------------------------------------------------

func TestDatabase_RenameTable_DataStaysInPlace(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)

	dataPrefix := service.TableDataPrefix("shop", before.TableID, 0)
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row1", []byte("data1")))

	mustExecQuery(t, prov, "shop", "RENAME TABLE users TO accounts")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "accounts")
	require.NoError(t, err)
	assert.Equal(t, before.TableID, after.TableID, "TableID must not change across a rename")

	// The row is still readable under the exact same physical key: no data
	// moved.
	v, err := h.kvSvc.Get(context.Background(), dataPrefix+"row1")
	require.NoError(t, err)
	assert.Equal(t, []byte("data1"), v)

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestDatabase_RenameTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.RenameTable(newTestContext(), "users", "accounts")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// Collation
// ---------------------------------------------------------------------------

func TestDatabase_Collation_RoundTrip(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx), "no collation set yet")

	require.NoError(t, db.SetCollation(ctx, sqle.Collation_utf8mb4_unicode_ci))
	assert.Equal(t, sqle.Collation_utf8mb4_unicode_ci, db.GetCollation(ctx))
}

func TestDatabase_Collation_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx))

	err := db.SetCollation(ctx, sqle.Collation_utf8mb4_unicode_ci)
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_Collation_UnspecifiedRoundTripsToDefault(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	require.NoError(t, db.SetCollation(ctx, sqle.Collation_Unspecified))
	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx))
}

// ---------------------------------------------------------------------------
// SchemaValidator
// ---------------------------------------------------------------------------

func TestDatabase_ValidateSchema_AcceptsMappableSchema(t *testing.T) {
	db := &Database{name: "shop"}
	err := db.ValidateSchema(sqle.Schema{
		{Name: "id", Type: types.Int64, Source: "t"},
		{Name: "name", Type: types.MustCreateStringWithDefaults(sqltypes.VarChar, 255), Source: "t"},
	})
	assert.NoError(t, err)
}

func TestDatabase_ValidateSchema_RejectsSpatialType(t *testing.T) {
	db := &Database{name: "shop"}
	err := db.ValidateSchema(sqle.Schema{
		{Name: "g", Type: types.PointType{}, Source: "t"},
	})
	var e *catalog.SpatialTypeUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// Structural interface assertions on the concrete *Database -- the positive
// control for the read-only guard tests in readonly_database_test.go: these
// prove the DDL interfaces are real (reachable on the unwrapped type),
// which is exactly what newReadOnlyDatabase must make unreachable.
// ---------------------------------------------------------------------------

func TestDatabase_ImplementsDDLInterfaces(t *testing.T) {
	var d sqle.Database = &Database{}
	_, ok := d.(sqle.TableCreator)
	assert.True(t, ok)
	_, ok = d.(sqle.IndexedTableCreator)
	assert.True(t, ok)
	_, ok = d.(sqle.TableDropper)
	assert.True(t, ok)
	_, ok = d.(sqle.TableRenamer)
	assert.True(t, ok)
	_, ok = d.(sqle.CollatedDatabase)
	assert.True(t, ok)
	_, ok = d.(sqle.SchemaValidator)
	assert.True(t, ok)
}

// TestDatabase_DropTable_ReclaimsSequenceCounters pins go-qrdb#444: the two
// sequence counters keyed by the table's TableID must not outlive the table.
//
// The counters are the one part of a table's storage the data reclamation
// never enumerated, because they live under "__seq:" rather than under the
// table's own data or index prefixes. Left behind they were unreachable as
// well as orphaned: both names are derived from the immutable TableID, and
// dropping the catalog row destroys the only mapping from the table's name to
// that id.
func TestDatabase_DropTable_ReclaimsSequenceCounters(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	ctx := context.Background()

	// A keyless table so BOTH counters exist: the AUTO_INCREMENT column
	// allocates from one, and the synthetic hidden row id from the other.
	mustExecQuery(t, prov, "shop",
		"CREATE TABLE tickets (seq BIGINT UNSIGNED AUTO_INCREMENT UNIQUE KEY, label VARCHAR(64))")
	def, err := h.tableSvc.GetTable(ctx, "shop", "tickets")
	require.NoError(t, err)

	autoinc := service.TableAutoIncrementSequenceName(def.TableID)
	rowid := service.TableRowIDSequenceName(def.TableID)

	// Allocate on both so the counters are genuinely present: Delete on a
	// never-written counter proposes nothing, so a test that skipped this
	// would pass whether or not the fix exists.
	_, _, err = h.seqSvc.Allocate(ctx, "shop", autoinc, 5)
	require.NoError(t, err)
	_, _, err = h.seqSvc.Allocate(ctx, "shop", rowid, 3)
	require.NoError(t, err)

	for _, name := range []string{autoinc, rowid} {
		v, err := h.kvSvc.Get(ctx, "__seq:shop:"+name)
		require.NoError(t, err, "counter %s must exist before the drop, or this test proves nothing", name)
		require.NotEmpty(t, v)
	}

	mustExecQuery(t, prov, "shop", "DROP TABLE tickets")

	for _, name := range []string{autoinc, rowid} {
		_, err := h.kvSvc.Get(ctx, "__seq:shop:"+name)
		var notFound *service.KeyNotFoundError
		require.ErrorAs(t, err, &notFound,
			"DROP TABLE must reclaim the %s counter; it is unreachable once the catalog row is gone", name)
	}
}

// TestDatabase_DropTable_ReclaimsGenerationKey pins the fix for a permanent
// leak: a table's TRUNCATE generation counter (go-qrdb#547) was never
// deleted by DROP TABLE. TableID is never reused, so once the catalog row
// naming this table is gone, nothing can ever again compute this key to
// finish deleting it -- the record would otherwise persist for the life of
// the store, one per table that was ever truncated then dropped.
func TestDatabase_DropTable_ReclaimsGenerationKey(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	ctx := context.Background()

	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")
	mustExecQuery(t, prov, "shop", "TRUNCATE TABLE widgets")

	def, err := h.tableSvc.GetTable(ctx, "shop", "widgets")
	require.NoError(t, err)
	genKey := service.TableGenerationKey("shop", def.TableID)

	_, err = h.kvSvc.Get(ctx, genKey)
	require.NoError(t, err, "the generation key must exist after a TRUNCATE, or this test proves nothing")

	mustExecQuery(t, prov, "shop", "DROP TABLE widgets")

	_, err = h.kvSvc.Get(ctx, genKey)
	var notFound *service.KeyNotFoundError
	require.ErrorAs(t, err, &notFound,
		"DROP TABLE must reclaim the generation key; it is unreachable once the catalog row is gone")
}

// TestDatabase_DropTable_FailedCatalogDropKeepsTheGeneration pins the DROP
// ordering fix: the generation counter is deleted in the catalog's own
// transaction, so a DROP whose catalog step fails leaves a table that still
// resolves at its truncated generation -- never one silently back at
// generation 0, writing into a keyspace an earlier TRUNCATE's reclaim job
// may still be deleting.
func TestDatabase_DropTable_FailedCatalogDropKeepsTheGeneration(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	ctx := context.Background()

	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")
	mustExecQuery(t, prov, "shop", "TRUNCATE TABLE widgets")
	before, err := h.tableSvc.GetTable(ctx, "shop", "widgets")
	require.NoError(t, err)
	genKey := service.TableGenerationKey("shop", before.TableID)

	// Fail exactly the transaction that deletes the generation counter. It
	// must be the catalog's own: a DROP that deleted the counter in any
	// other step would get past this and leave the table at generation 0.
	h.store.mu.Lock()
	h.store.rejectTxn = func(ops []service.Op) bool {
		for _, op := range ops {
			if op.Type == service.OpDelete && string(op.Key) == genKey {
				return true
			}
		}
		return false
	}
	h.store.mu.Unlock()
	_, _, err = execQuery(prov, "shop", "DROP TABLE widgets")
	require.Error(t, err)
	h.store.mu.Lock()
	h.store.rejectTxn = nil
	h.store.mu.Unlock()

	def, err := h.tableSvc.GetTable(ctx, "shop", "widgets")
	require.NoError(t, err, "the failed DROP must leave the table resolvable")
	assert.Equal(t, uint64(1), def.Generation, "and still at its truncated generation")
}

// TestDatabase_DropTable_ReclaimsIndexEntriesTheCatalogDoesNotName proves
// DROP TABLE reclaims the generation's whole index keyspace: entries of an
// incarnation the catalog no longer lists (a dropped index whose reclaim job
// has not run yet) go with the rest.
func TestDatabase_DropTable_ReclaimsIndexEntriesTheCatalogDoesNotName(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	ctx := context.Background()

	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")
	def, err := h.tableSvc.GetTable(ctx, "shop", "widgets")
	require.NoError(t, err)
	stray := service.TableIndexPrefix("shop", def.TableID, 0, 42) + "entry"
	require.NoError(t, h.kvSvc.Put(ctx, stray, []byte{}))

	mustExecQuery(t, prov, "shop", "DROP TABLE widgets")
	sweepPastQuiescence(t, h)

	_, err = h.kvSvc.Get(ctx, stray)
	var notFound *service.KeyNotFoundError
	require.ErrorAs(t, err, &notFound)
}

// TestDatabase_DropTable_ReclaimsSequenceCountersWhenNeverAllocated is the
// other half: a table that allocated from neither counter still drops
// cleanly. SequenceServicer.Delete is documented as idempotent and may not
// report not-found, which is what lets the teardown delete both counters
// unconditionally instead of inspecting the column definitions to guess which
// exist.
func TestDatabase_DropTable_ReclaimsSequenceCountersWhenNeverAllocated(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE plain (id BIGINT UNSIGNED PRIMARY KEY)")
	mustExecQuery(t, prov, "shop", "DROP TABLE plain")

	_, err := h.tableSvc.GetTable(context.Background(), "shop", "plain")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

// racingTruncateTableService truncates the table between the provider's
// resolution and its drop -- the window go-qrdb#547's N1 names.
type racingTruncateTableService struct {
	service.TableServicer
}

func (r *racingTruncateTableService) DropTable(ctx context.Context, req *service.DropTableRequest) error {
	if _, err := r.TruncateTable(ctx, req.Database, req.Name, true); err != nil {
		return err
	}
	return r.TableServicer.DropTable(ctx, req)
}

// TestDatabase_DropTable_RacingTruncateIsRefused proves DROP TABLE is fenced
// on the generation it resolved. Unfenced, the drop named the old generation
// in its reclaim job while the TRUNCATE's rows landed in a new one nobody
// would ever reclaim. It is refused with the retryable serialization failure
// instead, and the table survives for the retry.
func TestDatabase_DropTable_RacingTruncateIsRefused(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")

	reg := h.registry.(*fakeRelationalRegistry)
	reg.tableSvcFn = func(string, string) (service.TableServicer, error) {
		return &racingTruncateTableService{TableServicer: h.tableSvc}, nil
	}
	err := h.newDDLDatabase("shop").DropTable(newTestContext(), "widgets")
	requireLockDeadlockStale(t, err)

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err, "the refused drop leaves the table in place")
	assert.Equal(t, uint64(1), def.Generation)
}
