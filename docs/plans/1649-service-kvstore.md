# #1649 / PR #1664: service-backed DAO store

## fx1664u: service-backed DAO store and store parity

Tests (in `sdk/go`, run with `-tags=codec_json,raft`):

- `conformance/service_kvstore_test.go`: each test boots one node per cell
  (standalone memory, standalone pebble, single-node raft; no listener) and
  builds `server.NewServiceKVStore(n.Services().KV, .Index, .Transaction)`.
  - `TestServiceKVStore_DAOWithUniqueIndex`: two saves are found by their
    unique and plain indexes. A third save with a taken value is refused and
    nothing is written. An update frees the old value for another entity, and
    a delete frees its value.
  - `TestServiceKVStore_CompoundEntityFallsBackToScan`: a compound-index
    entity is found by `Find().Where(...)` over its compound fields.
    `QueryByCompoundIndex` and the store's `QueryCompoundIndex` both return
    `sdk.ErrKVIndexUnsupported`.
  - `TestServiceKVStore_TransactionalDAO`: `sdk.NewDAOTransactional` plus a
    unit of work commits two saves. A `PutAllIfAllUnchanged` with a stale
    guard is refused and writes none of its keys.
  - `TestServiceKVStore_ScanIndexRange`: `ScanByIndex` returns the half-open
    range.
- `conformance/dao_store_parity_test.go`, `TestDAOStoreParity`: runs one
  scenario table on `sdk.NewEmbeddedKVStore` (Pebble in a temp dir) and on the
  service store of each cell, and requires the same outcome at each step. The
  steps: save, get, get missing, list, count, index lookup, unique refusal, a
  unique value moved between two entities in op order, delete, the deleted
  value freed, and compound-field lookup.
- `example_dao_stores_test.go`, `ExampleNewEmbeddedKVStore`: the
  `docs/embedding.md` snippet, compiled and run with an `// Output:` block.

Defects fixed: none. The parity run found no difference except the one below,
which is by design.

The first run failed one case. `QueryByCompoundIndex` over the service store
returned `kvstore: store does not maintain secondary indexes`, not a scan
result. That error is the store's typed refusal of a named compound-index
query (`pkg/server/lifecycle/kv_adapter.go`, `QueryCompoundIndex`). The DAO
passes it through and does not scan, so the code behaves as designed. The
query builder does fall back to a scan on the compound fields, and the tests
now use it. Making a named `QueryByCompoundIndex` scan on a store without
compound indexes would be a change in `pkg/dao` (`generic_dao.go`). That file
is outside this lane's file map and is left for the coordinator to decide.

By-design differences, asserted rather than changed:

- The service store keeps no compound index (`kv_adapter.go:451`). A named
  compound query returns `sdk.ErrKVIndexUnsupported`, and the embedded store
  answers it from its index (`embedded_kvstore.go`, `QueryCompoundIndex`).
- Only the service store offers a guarded batch (`PutAllIfAllUnchanged`), so
  `NewDAOTransactional` units of work run on it and not on the embedded store.
- The embedded store refuses to index nested entity types (`/` in the type).

No CHANGELOG line: no defect fixed.
