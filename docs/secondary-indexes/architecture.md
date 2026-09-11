# Secondary Indexes and the Query Planner — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

A key-value store answers "give me this key" cheaply and "give me every entity
whose Email is X" by scanning everything. Applications built on the store need
the second question answered at index cost, and they need the planner to pick
the index without the caller naming it — otherwise every query site hardcodes an
access path that goes stale the moment the index set changes.

## Shape chosen

Indexes plus a cost-based planner, with the index *catalog* populated from two
sources:

1. **Struct tags on entity types**, parsed at DAO construction
   (`pkg/index.ParseIndexTags`). Tag-declared indexes are registered in the
   planner's catalog automatically — no runtime `CREATE INDEX` is needed.
2. **Explicit creation** through the index API and CLI, for indexes on data that
   has no Go entity type.

Callers express intent (`Find().Where("Email = ?", v)`); the planner chooses
between an index lookup, a compound-index scan, and a full scan by comparing
costs.

### Alternatives rejected

- **Caller-named indexes at every query site.** Rejected: the access path then
  lives in application code, so adding a better index does not speed anything up
  until every caller is edited. The escape hatch still exists for power users,
  but it is not the default.
- **Automatic index creation on first query.** Rejected: it makes a read
  operation a write operation, and an accidental query then costs a full index
  build on a production dataset.
- **Eventually consistent index updates.** Rejected: indexes are updated
  atomically with the entity. An index that lags the data makes a unique
  constraint unenforceable and makes a query's result depend on timing.
- **String interpolation into an expression parser.** Rejected on security
  grounds — see the security model below.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Index types, metadata, tag parsing, compound definitions | `pkg/index` | go-qrdb |
| Cost-based planner, plan cache | `pkg/planner` | go-qrdb |
| DAO fluent query API (`Find`, `Where`, `QueryBuilder`, `Condition`) | `pkg/dao` | go-qrdb |
| Index storage (PebbleDB and memory backends) | `pkg/index`, `pkg/storage` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Index()`, `Client.KVStore()`) | go-qrdb |
| Full-text search | `pkg/fts` — see [../full-text-search/](../full-text-search/) | go-qrdb |

## Index naming

Index names are **database-scoped and hierarchical**:

```
{database}.{entityType}.{FieldName}
```

for example `qrdb.User.Email`, `myapp.User.Email`. This prevents collisions
across entity types, lets the planner route a query to the right index, and
makes multi-database deployments work without a shared namespace.

## Store capability: not every store can maintain an index

The catalog above describes what the ENTITY declares. It is not the same
question as what the STORE can serve, and conflating the two produced the worst
class of bug this subsystem has had.

A `kvstore.KVStore` implementation that cannot maintain secondary indexes
**must** say so, by returning `kvstore.ErrIndexUnsupported` from
`RegisterEntityIndexes` and from every index query method. Returning
`(nil, nil)` is forbidden. An empty result with a nil error is
indistinguishable from a maintained index with no matches, so a caller reading
its own work queue concludes there is no work — forever, with no error anywhere
and no failure counter moving.

`pkg/dao` probes the capability once, at DAO construction:

| Store answer at registration | Catalog | Planned query (`Find().Where(...)`) | Direct call (`QueryByIndex`) |
|---|---|---|---|
| `nil` (indexes maintained) | tag-declared indexes | index lookup or scan | answered from the index |
| `ErrIndexUnsupported` | **empty for that kind** | **full scan — correct, slower** | `ErrIndexUnsupported` |
| any other error | — | DAO construction **fails** | — |

The probe is **per index kind**. Single-field and compound definitions are
registered in separate calls, and tracked by separate flags, because a store
can serve one and not the other — `lifecycle.kvStoreAdapter` maintains real
single-field indexes through the engine and has no compound verb at all.
Registering them together forces a false choice: either one kind's refusal
disables the other, or the store accepts the batch and silently drops what it
cannot do, which tells the DAO those indexes exist and routes queries into a
method that errors. `RegisterEntityIndexes` therefore ACCUMULATES per entity
type rather than replacing, and a store that cannot serve a definition
**refuses it** rather than skipping it and returning `nil`.

### The index name is the caller's, not the store's

`EntityIndex.IndexName` carries the fully resolved name, and every store uses
it verbatim (`EntityIndex.ResolvedName`). The caller is the only party that
knows the name it will later look the index up by: a DAO configured with
`WithDatabase` resolves `{database}.{entityType}.{FieldName}`, while a store
composing its own name produces the unqualified form — so the DAO advertised
an index, planned a lookup, and queried a name nobody created. The entity type
stays a separate argument because it is also the `{entityType}/` key prefix a
store filters writes by, and the two must not be conflated.

The empty catalog is the load-bearing part. Before it, the planner built its
catalog purely from struct tags and never asked the store, so a field tagged
`index:"true"` routed to `QueryIndex` on a store maintaining nothing and
returned zero rows with a nil error — while the *same field without the tag*
fell back to a full scan and was correct. **Adding the index was what broke the
query**, and it failed toward "nothing found".

The two outcomes are deliberately different by entry point. A planned query can
choose another access path, so it does and stays correct. `QueryByIndex` names
the access path in its signature, so it cannot silently substitute one; it fails
closed with the typed error.

The third row matters too: a transient index-subsystem failure must NOT be
reported as `ErrIndexUnsupported`, or the DAO permanently downgrades itself to
full scans over a store that was only briefly unavailable.

### Which stores maintain indexes

| Store | Indexes | Why |
|---|---|---|
| `lifecycle.kvStoreAdapter` (system tenant, via `StoreResolver`) | **yes**, when built with the node's `IndexServicer` | It routes to the same `KVEngine` that applies its writes, so index entries are maintained inside the Raft-applied batch, backfilled on registration, and replicated |
| `kvstore.MemoryKVStore` | **yes** | Maintains entries on the write path from the value's JSON fields; single-process |
| `seal.tenantSharedKVStore` (dedicated tenant, via `StoreResolver`) | no — refuses | Stores only ciphertext in the shared store, so nothing beneath it can read a field value to index. See below |
| `seal.tenantBarrierKVStore` | no — refuses | Same reason |
| `lifecycle.NewKVStoreAdapter` (volume-pool cursor) | no — refuses | Built without an `IndexServicer`; its record is a single cluster-wide scalar with nothing to index |
| `embedded.serverKVAdapter` | no — refuses | Wraps a bare `KVServicer` with no index subsystem behind it |

**Refused: an OPEN range bound on the SDK transport path** (tracked as #36).
`transport.IndexClient.IndexScan` takes plain strings and cannot express
"unbounded", and the planner leaves one side nil for every one-sided range —
so `Team >= "beta"` would become `["beta", "")` and return nothing. The
adapter refuses a nil bound instead.

**Numerically ordered fields are not advertised to the planner** (#35). Index
terms are decimal text compared lexicographically, so `"10"` sorts before
`"2"` and `Age >= 2` would return an incomplete result — worse than empty,
because a partial answer looks like a real one. `buildCatalog` omits numeric
single-field indexes, and compound indexes with any numeric member, so the
planner chooses a correct full scan.

Equality is sound on decimal text, so the cost is a performance one: a numeric
equality query full-scans instead of using its index. The direct
`QueryByIndex` path is unaffected and still uses it. The surgical fix — the
planner refusing only RANGE plans on such fields, using the `FieldType` it
already carries — changes the planner's documented contract and needs a
ruling; so does threading order-preserving numeric encoding through
`EntityIndex` and the Raft command format.

**Refused: `index:"case_insensitive"` on the engine-backed and transport
paths** (tracked as #34). `protocol.EncodeIndexCreate` carries no
case-sensitivity flag, so `KVEngine.handleIndexCreate` builds every index
case-sensitive. Accepting the definition would create a case-SENSITIVE index
while reporting success, and a lookup for `Alpha` over a stored `alpha` would
return empty with a nil error — so those adapters refuse it and the DAO plans
a correct full scan. Only an explicit `case_insensitive` tag is affected:
`index.ParseIndexTags` defaults to case-sensitive, so ordinary `index:"true"`
and `index:"unique"` fields keep their real engine index. `MemoryKVStore`
honours the flag itself and does not refuse.

**Open: indexing barrier-encrypted tenant stores.** The two `seal` stores exist
to guarantee that an operator reading the shared store sees only ciphertext. An
index built above the encryption — the only layer where plaintext exists —
would place the indexed field value into an index KEY in that same shared
store, in the clear, defeating the property. A keyed/deterministic index term
preserves it for exact-match lookups but destroys the ordering that range scans
need. Choosing between those is a cryptographic design decision, and it is
deliberately not made in the implementation. Until it is ruled on, these stores
refuse, and a DAO over them answers correctly by full scan.

## Index kinds

| Kind | Declared as | Behaviour |
|---|---|---|
| Non-unique | `index:"true"` | many entities per value |
| Unique | `index:"unique"` | uniqueness enforced on write |
| Sparse | sparse flag | entities missing the field are simply not indexed |
| Numeric | numeric flag | order-preserving numeric encoding |
| Case-insensitive | `index:"case_insensitive"` | string normalisation before indexing |
| Compound | `CompoundIndexRegisterer` | multiple fields, order significant |

## Compound indexes and prefix matching

A compound index `(A, B, C)` serves any query matching a **contiguous prefix** of
its fields. Field order is significant: the first field is the leading field
used for prefix scans.

| Query pattern | Usable | Plan |
|---|---|---|
| `A = ?` | yes (1-field prefix) | CompoundIndexScan |
| `A = ? AND B = ?` | yes (2-field prefix) | CompoundIndexLookup |
| `A = ? AND B = ? AND C = ?` | yes (full match) | CompoundIndexLookup |
| `A = ? AND B > ?` | yes (prefix + range) | CompoundIndexScan |
| `B = ?` alone | **no** — no leading prefix | falls back to a single-field index |
| `A = ? AND C = ?` (gap) | partial — `A = ?` prefix, `C = ?` post-filtered | CompoundIndexScan |

Compound keys use **order-preserving encoding** so lexicographic byte comparison
matches logical field ordering: strings are byte-stuffed and null-terminated
(`\x00` escaped as `\x00\xFF`, terminated `\x00\x00`); numerics are 8-byte
big-endian with the sign bit flipped. That is what makes a prefix scan a
contiguous seek.

## Cost model

| Plan | Base cost | With N fields matched |
|---|---|---|
| CompoundIndexLookup, non-unique | 3 | 3 / N |
| CompoundIndexLookup, unique | 1 | 1 |
| CompoundIndexScan | 20 | 20 / N |
| Single-field IndexLookup | 10 | n/a |

A compound index covering two or more predicates is normally cheaper than any
single-field index, because it avoids post-filtering.

## Trust boundaries

The query surface is a parser that takes caller-supplied text, so it is treated
as an attack surface rather than a convenience:

- **Field validation** — a `Where` field must resolve to a known field on the
  entity type; unknown fields are rejected rather than passed through.
- **Operator whitelist** — only whitelisted operators are accepted.
- **Parameterized values** — values travel as bound parameters, never spliced
  into the expression text.
- **Zero-allocation expression parser** — the parser does not build intermediate
  strings from caller input.

**Index-write scoping is NOT uniform across the two write paths**, and the
earlier claim here that "each index write is scoped to the same (tenant,
database) pair as the entity it indexes, so an index can never span tenants" is
true of only one of them. Corrected against measured behaviour:

| Write path | Scoping |
|---|---|
| DAO / entity writes (`KVEngine.PutWithIndexes`, `DeleteWithIndexes`) | filtered to the indexes whose names carry the entity type's prefix — scoped as described |
| Raft apply path (`handlePut` / MVCC put, the path a KV, collection or SQL write actually takes) | **unfiltered** — every registered index on the state machine is offered every value, and any index whose field name appears in that JSON value indexes the key |

On the second path an index named `dbA.users.email` also indexes a
`dbB`-prefixed key and a plain KV key whose value carries an `email` field. The
name is a routing label, not a filter. Tenants under `volume` or `raft`
isolation each get their own state machine, so the blast radius stops at the
shard; tenants under **prefix** isolation share one, so it does not stop at the
tenant. Narrowing the second path to a key-prefix filter changes the Raft
command format and is tracked as its own defect — it is stated here rather than
left implied, because the paragraph it replaces asserted the property as a
guarantee.

## What this feature deliberately does not do

- **No expression indexes** (`LOWER(name)`).
- **No partial indexes** (`WHERE status = 'active'`).
- **No covering indexes** — a query still fetches the entity.
- **No relevance ranking or text search** — that is
  [../full-text-search/](../full-text-search/).
- **No eventual consistency.** Indexes update atomically with the entity, and
  that is a deliberate write-amplification cost.
