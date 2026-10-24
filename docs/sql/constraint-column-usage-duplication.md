# `constraint_column_usage` is served twice

> **Status: OPEN — awaiting owner ruling** · Last verified: 2026-10-21
> Nothing is broken; the two implementations return the same rows. This
> records which one is canonical.

## The two paths

| | where | scope | how it is reached |
|---|---|---|---|
| A | `pkg/sql/pgcatalog/constraint_column_usage.go` | **PostgreSQL dialect only** | `pgcatalog.InfoSchema()` wraps go-mysql-server's `information_schema` database and overrides `GetTableInsensitive` (`columns.go:126`) |
| B | `pkg/sql/infoschema/constraint_column_usage.go` | **process-global** | `Register()` writes into go-mysql-server's package-level `information_schema.NewInformationSchemaTablesToAdd`, called from `pkg/sql/pgsql/server.go:377` |

Both compute the view from the same catalog data and report only PRIMARY KEY
and UNIQUE constraints.

## Why B says it has to exist

Its doc comment argues the view cannot be contributed through the
`DatabaseProvider` the way `pkg/sql/pgcatalog` contributes `pg_catalog`, because `NewInformationSchemaDatabase` copies the
tables map **once, at construction**, so the entry must be present before the
engine builds its catalog.

That reasoning is sound for the map, but path A does not use the map: it wraps
the constructed database and intercepts the lookup. So the stated constraint
does not rule out the dialect-scoped approach — A is a working counter-example
to it.

`pkg/sql/pgsql/server.go` is the only importer of `pkg/sql/infoschema`. Without
that `Register()` call, path A alone serves the view and the pgsql and mysql
suites stay green.

## The decision

1. **Keep A (dialect-scoped), delete `pkg/sql/infoschema`.** This is what the
   `infoSchemaByDialect` seam (`pkg/sql/engine/engine.go`) is for, and it keeps
   MySQL's `information_schema` free of a view MySQL does not have. Path B's
   own comment concedes the global registration is only defensible because the
   view is SQL-standard — a narrower claim than "it is required".
2. **Keep B (global), delete `pkg/sql/pgcatalog`'s copy** and its
   `GetTableInsensitive` branch.
3. **Keep both**, accepting that two files must be edited in step whenever the
   view's shape changes, with nothing enforcing it.

Option 3 is the current state.
