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

Each index write is scoped to the same (tenant, database) pair as the entity it
indexes, so an index can never span tenants.

## What this feature deliberately does not do

- **No expression indexes** (`LOWER(name)`).
- **No partial indexes** (`WHERE status = 'active'`).
- **No covering indexes** — a query still fetches the entity.
- **No relevance ranking or text search** — that is
  [../full-text-search/](../full-text-search/).
- **No eventual consistency.** Indexes update atomically with the entity, and
  that is a deliberate write-amplification cost.
