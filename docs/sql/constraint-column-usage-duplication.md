# `constraint_column_usage` is served twice

**Status:** open decision, measured 2026-08-16. Nothing is broken; the two
implementations return the same rows. This is a "which one is canonical"
question, not a bug report.

## The two paths

| | where | scope | how it is reached |
|---|---|---|---|
| A | `pkg/sql/pgcatalog/constraint_column_usage.go` | **PostgreSQL dialect only** | `pgcatalog.InfoSchema()` wraps go-mysql-server's `information_schema` database and overrides `GetTableInsensitive` (`columns.go:126`) |
| B | `pkg/sql/infoschema/constraint_column_usage.go` | **process-global** | `Register()` writes into go-mysql-server's package-level `information_schema.NewInformationSchemaTablesToAdd`, called from `pkg/sql/pgsql/server.go:235` |

Both compute the view from the same catalog data and report only PRIMARY KEY
and UNIQUE constraints.

## Why B says it has to exist

Its doc comment argues the view cannot go through the `DatabaseProvider`
"the way pg_catalog does", because `NewInformationSchemaDatabase` copies the
tables map **once, at construction**, so the entry must be present before the
engine builds its catalog.

That reasoning is sound for the map, but path A does not use the map: it wraps
the constructed database and intercepts the lookup. So the stated constraint
does not actually rule out the dialect-scoped approach — A is a working
counter-example to it.

## What was measured (2026-08-16)

`infoschema.Register()` and its import were removed from `pgsql/server.go`,
leaving only path A:

| target | result |
|---|---|
| pgsql | **green** — 393 ran, 249 passed, 0 unexpected (vs 248 with both paths; the difference is inside the known order-dependent variance) |
| mysql | **green** |
| node  | unaffected — does not go through the PostgreSQL server |

With the registration removed, `pkg/sql/infoschema` has **no remaining
importer anywhere** in `pkg/`, `sdk/`, `cmd/` or `test/`.

The change was then **reverted**: which implementation is canonical is an
architecture decision, and path B was added on 2026-08-15 by concurrent work
whose intent is not recorded anywhere but its own doc comment.

## The decision

1. **Keep A (dialect-scoped), delete `pkg/sql/infoschema`.** This is what the
   existing `infoSchemaByDialect` seam was built for, and it keeps MySQL's
   `information_schema` free of a view MySQL does not have. Path B's own
   comment concedes the global registration is only defensible because the
   view is SQL-standard — a narrower claim than "it is required".
2. **Keep B (global), delete `pkg/sql/pgcatalog`'s copy** and its
   `GetTableInsensitive` branch.
3. **Keep both**, accepting that two files must be edited in step whenever the
   view's shape changes, with nothing enforcing it.

Option 3 is the current state and is the one worth ending deliberately rather
than by default.
