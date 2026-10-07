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
| Index storage (`StoragePebbleIndex`, any `storage.Storage` backend) | `pkg/index`, `pkg/storage` | go-qrdb |
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

Under a transport, the name additionally carries the composite
`{tenant}/{database}/` namespace the registry prefixes every KEY with, so the
name an index is created and queried under is
`{tenant}/{database}/{database}.{entityType}.{FieldName}`. Both prefixes come
from one function, `tenant.ComposeNamespace` — see
[../multi-tenancy/architecture.md](../multi-tenancy/architecture.md).

## Index key scope — a name is not a predicate

**An index name routes a query. It does not decide which writes the index
takes.** That is a separate property, the index's **key scope**: the byte
prefix every key it indexes must carry. Maintenance on the Raft apply path
consults the scope on every write, insert and delete.

A scope has two halves, composed exactly the way the keyspace is:

```
scope = {tenant}/{database}/   +   declared key prefix
        (read back off the        (what the creator knew:
         index name)               a collection's documents,
                                   a DAO entity type)
```

- The **namespace half** is read back off the index name. The registry that
  prefixes a key with `{tenant}/{database}/` prefixes the index name with the
  same string, so reading it back is the exact inverse of the wrapper, not an
  inference.
- The **declared half** is what the name cannot say. A collection index
  declares `__col:{database}:{collection}#{incarnation}/`, the prefix its
  incarnation's documents are keyed under; a DAO entity index declares `{entityType}/`. It is declared
  **relative** to the namespace, so a declaration made below the registry stays
  correct whether or not a registry is in play — exactly like the key it
  describes.

An index with neither half is **unscoped** and indexes every JSON value in the
state machine. That is deliberate rather than accidental: a bare name is not
attributable to a keyspace, and an index whose creator declared nothing is the
creator's whole database by intent. It is reachable only from a single-tenant,
registry-less composition, because every transport namespaces both keys and
index names.

**This was not always true, and the gap was a tenancy boundary.** Maintenance
on the apply path used to iterate every registered index and decide purely on
whether the written JSON happened to carry a field of the right name — so an
index named for one database also indexed another database's keys, another
collection's documents, and plain key-value keys, and under `prefix` tenant
isolation (where tenants share one state machine) it indexed another tenant's
keys, which `IndexQuery` then returned. The correctly scoped path existed and
was wired only to the DAO helpers, which almost nothing takes. Both halves of
the scope above, and the tests that pin them, exist to close that. That
includes a DAO built `WithDatabase`: its qualified name
`{database}.{entityType}.{FieldName}` carries no `{tenant}/{database}/`
namespace of its own, so the namespace half is empty and the declared half —
`{entityType}/`, read off the entity type the adapter was given, not the
database string in the name — is the whole scope. `lifecycle.kvStoreAdapter`
maintains it like any other single-field index; a dedicated test
drives the write through a REAL `KVEngine`, not a mock, because a mock can
only show the adapter's `CreateIndex` request was shaped correctly, not that
the engine answers a query against it with the write.

Two consequences worth stating:

- **Index creation backfills inside the scope**, and bounds its scan by it.
  An unscoped backfill would do in one step what unscoped maintenance did over
  time.
- **A unique index cannot reject a write outside its scope.** The unique
  pre-check is scoped too, so one tenant's stored value can neither deny
  another tenant's write nor reveal, by denying it, that the value exists.

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
| `lifecycle.kvStoreAdapter` (system tenant, via `StoreResolver`) | **yes**, when built with the node's `IndexServicer` | It routes to the same engine host that applies its writes (`engine.MVCCEngine` on every backend as of Lane 1A — see [../databases/](../databases/)), so index entries are maintained inside the Raft-applied batch, backfilled on registration, and replicated |
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

**`index:"case_insensitive"` is honoured on every backend** (#34). The
case-sensitivity flag is threaded end to end: `protocol.IndexFlagCaseInsensitive`
carries it on the wire, and `KVEngine.handleIndexCreate`, `MVCCEngine`'s
equivalent handler, the storage-backed `StoragePebbleIndex` (any
`storage.Storage`, not only Pebble), and
`kvStoreAdapter.RegisterEntityIndexes` all derive `index.IndexConfig.CaseSensitive`
from it rather than defaulting to case-sensitive. A case-insensitive
definition now produces a real case-insensitive index everywhere, so a lookup
for `Alpha` over a stored `alpha` matches instead of returning empty with a
nil error. `MemoryKVStore` honours the flag the same way.

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

**Index maintenance is key-scoped, and that is what keeps an index inside its
tenant.** Every write is offered only to the indexes whose key scope covers the
key (see [Index key scope](#index-key-scope--a-name-is-not-a-predicate) above),
so an index created through any transport — where the name carries the
`{tenant}/{database}/` namespace — can hold only its own tenant's and its own
database's keys.

The statement this replaces read *"Each index write is scoped to the same
(tenant, database) pair as the entity it indexes, so an index can never span
tenants"*, and it was **false when it was written**: it described the DAO
helpers, while the Raft apply path — the path a key-value write, a collection
document insert and a SQL row write all take — filtered nothing. It is recorded
here rather than quietly deleted because it was the security section of this
feature's own requirements contract, which is the document a reviewer consults
to decide whether a change is safe.

The residual is named rather than implied: an **unscoped** index — no namespace
in its name and no declared prefix — still spans the whole state machine. No
transport can create one; a single-tenant in-process composer can, and for it
"the whole state machine" and "its own database" are the same set.

## What this feature deliberately does not do

- **No expression indexes** (`LOWER(name)`).
- **No partial indexes** (`WHERE status = 'active'`).
- **No covering indexes** — a query still fetches the entity.
- **No relevance ranking or text search** — that is
  [../full-text-search/](../full-text-search/).
- **No eventual consistency.** Indexes update atomically with the entity, and
  that is a deliberate write-amplification cost.
