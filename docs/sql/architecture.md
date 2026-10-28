# SQL — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

> **Build-tag gated.** `qrdb_sql` compiles the shared SQL core; `qrdb_mysql`
> and `qrdb_pgsql` add the wire protocols and each implies `qrdb_sql`.
> `qrdb admin capabilities` reports `sql`, `mysql` and `pgsql` accordingly.

## Problem

Consumers arrive with SQL clients, ORMs and migration tools they already have.
Making them speak a bespoke API to reach data they think of relationally means
rewriting all of that; serving SQL badly — accepting statements and quietly
doing something else — is worse than not serving it.

## Shape chosen

One SQL engine ([go-mysql-server](https://github.com/dolthub/go-mysql-server)),
two wire protocols, plus an in-process path. Both wire protocols share the
engine and reuse the store's existing mTLS auth, RBAC, multi-tenancy and audit
rather than inventing SQL-shaped versions of each.

```
SQL client --> Wire protocol layer --> Shared SQL engine --> KV service --> Raft
                (MySQL or PG)          (go-mysql-server)     (tenant-scoped)
```

### PostgreSQL Path

PostgreSQL clients connect via [psql-wire](https://github.com/jeroenrinzema/psql-wire). Queries are translated from PG syntax to GMS-compatible SQL before execution.

```
psql --> psql-wire (PG framing) --> PG translator --> go-mysql-server --> KVTable        --> Raft
                                                                     \-> RelationalTable -^
```

#### Startup parameters

QRDB reports `standard_conforming_strings=on` alongside the parameters
psql-wire emits for itself. It is required rather than decorative: pgx refuses
to run *any* simple-protocol query against a server that has not reported it,
and `gorm.io/driver/postgres` forces simple protocol for `Migrator.GetRows`,
which backs `ColumnTypes` and therefore every schema-introspecting migration.
See the literal-escaping note under [PostgreSQL Syntax
Translation](#postgresql-syntax-translation) for what makes the claim true.

#### Prepared statements and cached plans

A PostgreSQL client may PARSE a statement once and then BIND/EXECUTE it many
times against the `RowDescription` it was given at Describe time; pgx does this
by default (`QueryExecModeCacheStatement`). If DDL changes the statement's
result shape in between, the rows it now produces no longer match the
descriptor the client is still decoding against.

QRDB refuses that execution with PostgreSQL's own condition — `cached plan must
not change result type`, SQLSTATE **0A000** — rather than writing rows the
client would mis-map. Column count, column names, and type OIDs are all
compared: an equal-width rename or retype is the case that would otherwise
corrupt silently, because nothing downstream notices and a client scanning by
name lands each value in the wrong field. Clients recover on their own, as they
do against PostgreSQL: pgx drops the statement from its cache on any error, so
a retry of the same SQL re-parses and succeeds.

#### `pg_catalog`

QRDB serves the subset of PostgreSQL's system catalogue that schema
introspection reads — `pg_type`, `pg_class`, `pg_namespace`, `pg_description`,
`pg_indexes` — from `pkg/sql/pgcatalog`, reachable both qualified
(`pg_catalog.pg_class`) and bare (`pg_type`), as PostgreSQL's implicit search
path allows. The relations are computed per query, read-only (they implement no
write-capability interface, so DDL against them fails during analysis), and
scoped to the session's tenant. They are served only to PostgreSQL clients.

`pg_indexes` reports a table's primary key under PostgreSQL's `<table>_pkey`
name rather than go-mysql-server's MySQL spelling `PRIMARY`, because that is
the name a client compares against; its `indexdef` is reconstructed from the
index's own metadata, since nothing in the engine keeps the original statement
text.

#### information_schema

`information_schema` is go-mysql-server's own database — the analyzer's catalog
resolves the name to its built-in instance and never consults the
`DatabaseProvider` — so QRDB cannot contribute a view to it the way it
contributes the `pg_catalog` schema. go-mysql-server exposes
`NewInformationSchemaTablesToAdd` for exactly this, documented as the hook
Doltgres injects PostgreSQL-specific tables through, and `pkg/sql/infoschema`
uses it to add **`constraint_column_usage`**.

That view is SQL-standard (PostgreSQL implements it; MySQL offers the
overlapping `key_column_usage` instead), and GORM's PostgreSQL Migrator joins
it against `table_constraints` and `columns` to resolve a table's primary-key
and unique constraints. It reports only PRIMARY KEY and UNIQUE constraints:
PostgreSQL's view lists, for a FOREIGN KEY, the columns of the constraint it
*references*, which cannot be named from an `sql.ForeignKeyConstraint` alone,
and a plausible-looking wrong answer would be worse than a true partial one.

`information_schema.tables` reports `TABLE_ROWS` and `DATA_LENGTH` for a
relational table from the planner's cached row-count estimate, not from a
count of the table: listing a database's tables costs the same whatever the
tables hold. The estimate is at most about ten seconds old; `COUNT(*)` is
always exact. See [engineering.md](engineering.md#planner-statistics).


## Build Tags

The SQL layer is opt-in via build tags (enabled by default in the Makefile):

| Flag | Build Tag | Description |
|------|-----------|-------------|
| `WITH_QRDB_SQL=1` | `qrdb_sql` | Shared SQL core: go-mysql-server engine, ad-hoc `/sql/query` service, in-process driver |
| `WITH_QRDB_MYSQL=1` | `qrdb_mysql` | MySQL wire protocol (implies `qrdb_sql`) |
| `WITH_QRDB_PGSQL=1` | `qrdb_pgsql` | PostgreSQL wire protocol (implies `qrdb_sql`) |

Disable with `WITH_QRDB_MYSQL=0` or `WITH_QRDB_PGSQL=0`. What each removes,
precisely:

| Flag off | `pkg/sql/mysql` | `pkg/sql/pgsql` | go-mysql-server, vitess |
|---|---|---|---|
| `WITH_QRDB_MYSQL=0` (SQL or PG still on) | absent | present (if on) | present — GMS is PostgreSQL's engine too |
| `WITH_QRDB_PGSQL=0` (SQL or MySQL still on) | present (if on) | absent | present — GMS is MySQL's engine too |
| `WITH_QRDB_SQL=0` (forces MySQL and PG off too) | absent | absent | **absent** |

MySQL and PostgreSQL are two wire protocols over ONE SQL engine
(go-mysql-server), and go-mysql-server's own parser and error types come
from vitess — so disabling MySQL alone does not remove vitess from the
binary while PostgreSQL (or the ad-hoc `/sql/query` path) is still compiled
in: go-mysql-server keeps pulling it in regardless of which wire protocol,
if any, sits on top. `WITH_QRDB_MYSQL=0` removes qrdb's own `pkg/sql/mysql`
package, the MySQL listener, and the MySQL-specific config wiring — a
config that sets `protocols.mysql: true` on a binary built this way is
refused at startup with a typed `ValidationError` (`pkg/config/mode.go`),
the same pattern `protocols.postgresql` uses for `WITH_QRDB_PGSQL=0`. Only
`WITH_QRDB_SQL=0` removes go-mysql-server and vitess themselves: the
Makefile forces `WITH_QRDB_SQL=1` whenever either wire protocol is
enabled, since both are engines over the shared `pkg/sql` core, so this is
the only configuration where the SQL engine's own dependencies are absent.
With SQL on and PG off and MySQL off, `qrdb_sql` alone still compiles the
ad-hoc `/sql/query` service and the in-process driver, both backed by the
same go-mysql-server engine. Only with `qrdb_sql` off is the ad-hoc query
unserved (see [api.md](api.md#capability-matrix--ad-hoc-query)).

**`WITH_QRDB_SQL=0` alone is sufficient** — the Makefile forces
`WITH_QRDB_MYSQL` and `WITH_QRDB_PGSQL` to `0` as well, whatever either was
set to, including their own default of `1`. Naming
`WITH_QRDB_MYSQL=0 WITH_QRDB_PGSQL=0` alongside it is accepted and changes
nothing. The alternative, documenting that removing the SQL layer takes all
three flags, was rejected: a build flag that needs two companion flags to do
what its name says is a trap.

`test/buildgates` (`mysql_dependency_test.go` and its PostgreSQL mirror,
`pgsql_dependency_test.go`) checks this with `go list -deps` over
`cmd/qrdbd` and `cmd/qrdb`, not by inspection: it proves go-mysql-server's
listener package (`github.com/dolthub/go-mysql-server/server`, the one
package that constructs a vitess MySQL listener) is unreachable under
`WITH_QRDB_MYSQL=0`, that psql-wire is unreachable under `WITH_QRDB_PGSQL=0`,
and that vitess, go-mysql-server and psql-wire are all unreachable entirely
under `WITH_QRDB_SQL=0`. `make vet-tags` sweeps the matching rungs
(`NOMYSQL_TAGS`, `NOPGSQL_TAGS`, `NOSQLFAMILY_TAGS` in the Makefile) so the
same claim is checked by compilation, not only by the dependency-graph gate.


### Alternatives rejected

- **Writing a SQL parser and planner.** Rejected: go-mysql-server is a real
  engine with a real analyzer. Owning a parser would mean owning every
  dialect edge case forever.
- **Full AST translation for PostgreSQL.** Rejected in favour of targeted,
  literal-aware string transformation — see the translator's contract in
  [engineering.md](engineering.md). An AST round-trip through a MySQL-shaped
  parser loses PostgreSQL constructs the parser cannot represent, and produces
  confident wrong SQL rather than an honest passthrough.
- **Mapping SQL privileges onto service RBAC roles.** Rejected explicitly: the
  two planes have disjoint resources and neither consults the other. A role is
  not a grant and a grant is not a role; a mapping would make each system's
  guarantees depend on the other's vocabulary.
- **Implementing MySQL's `mysql` grant tables.** Rejected: QRDB keeps its own
  per-`(tenant, database)` privilege catalog. The `mysql` schema is served as an
  empty stand-in only because go-mysql-server's plan builder resolves it while
  building `GRANT`/`REVOKE`.
- **Serving `pg_catalog` to MySQL clients (and vice versa).** Rejected: a MySQL
  client has no more business resolving `pg_type` than a PostgreSQL client has
  resolving MySQL's grant tables.

## Authorization

SQL is authorized over **two planes with disjoint resources**. Neither consults the other, and there is deliberately no mapping between them: a role is not a grant, and a grant is not a role.

| Plane | Owner | Question it answers |
|-------|-------|---------------------|
| **Control** — service RBAC | `pkg/auth/rbac`, `pkg/auth/middleware` | May this identity reach the SQL endpoint at all, and may it create or drop a database? |
| **Data** — SQL table privileges | `pkg/sql/privilege`, `pkg/service` (`PrivilegeServicer`) | May this identity touch THIS table, or THIS column? |

RBAC gates the endpoint and the database lifecycle; privileges govern everything inside a database.

### The database lifecycle over the wire

`CREATE DATABASE` and `DROP DATABASE` over the MySQL and PostgreSQL wire are
the API's database create and drop, held to exactly the same rules and served
by the same code — the tenant-scoped `service.DatabaseServicer`:

- `CREATE DATABASE` needs `databases:create` for the session's tenant. The
  database is **owned by the session's tenant** and counts against that
  tenant's database quota and entitlements.
- `DROP DATABASE` needs `databases:delete`, and a non-system session can drop
  **only a database its own tenant owns**; another tenant's database is refused
  and left intact.
- `HasDatabase`, `USE` and every database lookup answer only for the session
  tenant's databases: another tenant's database looks exactly like a missing
  one, as in `SHOW DATABASES`.

The privilege plane permits both statements (their target is server-level)
precisely because RBAC decides them; the provider asks the authorizer, not a
SQL grant. A listener with no tenant-scoped database service or no authorizer
refuses both statements to every non-system session
(`provider.DatabaseLifecycleUnavailableError`) rather than running them
unmetered and unowned; the system-tenant session of an unauthenticated
listener keeps the operator's direct path.

### Table privileges

`GRANT`, `REVOKE`, and `SHOW GRANTS` are real MySQL statements, parsed by the real parser and rewritten by an analyzer post-analyze rule onto QRDB's own per-`(tenant, database)` privilege catalog, not MySQL's `mysql` grant tables (see [Alternatives rejected](#alternatives-rejected)).

```sql
GRANT SELECT, INSERT ON shop.orders TO 'alice'@'%';
GRANT SELECT (id, total) ON shop.orders TO 'bob'@'%';
GRANT ALL PRIVILEGES ON shop.* TO 'admin'@'%' WITH GRANT OPTION;
SHOW GRANTS FOR 'alice'@'%';
REVOKE INSERT ON shop.orders FROM 'alice'@'%';
```

Three tiers are enforced, each covering the ones below it: **database** (`ON db.*`), **table** (`ON db.tbl`), and **column** (`ON db.tbl(col)`).

Supported privileges: `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `DROP`, `ALTER`, `INDEX`, `REFERENCES`, `GRANT OPTION`, `ALL PRIVILEGES`, and `USAGE`.

Anything that cannot be represented is **refused by name** rather than stored and never checked — server-wide privileges (`SUPER`, `PROCESS`, `FILE`, `CREATE USER`, ...), routine and trigger privileges, `ON FUNCTION` / `ON PROCEDURE`, `ON *.*` at any privilege, and host-qualified grantees other than `'user'@'%'` (a grantee is a QRDB account name — `Identity.AccountName()`, never the Subject, which for a resolved account is its user id — and carries no host).

### Activation latch — the default posture

A database with **no privilege catalog is unrestricted**. The **first** privilege mutation arms the database, and from that moment it is **default-deny** for every session: an identity may do only what its record grants. `REVOKE` never disarms it, and nothing else does — the posture is monotonic and can only ever tighten.

This is MySQL's own model made per-database: MySQL's privilege system is inert until the `mysql` schema's grant tables are loaded (`mysql_db.MySQLDb.Enabled()`), and flips exactly once.

The **system tenant is exempt permanently**. There is no `--skip-grant-tables` recovery mode in QRDB, so the exemption is what makes arming a database safe. Note the consequences:

- Privileges are enforced for **non-system tenants only**. A deployment that runs everything as the system tenant is unrestricted by design.
- The catalog is scoped to `(tenant, database)` exactly as the relational table catalog is, so a tenant's catalog is bootstrapped **from inside that tenant** — which the unrestricted-until-armed rule always permits.

### Enforcement

Enforcement is a `sql.AuthorizationHandler` installed process-wide, so go-mysql-server asks about **every statement it builds**. A statement kind the handler does not model is **denied** on an armed database rather than waved through. Refusals reach the client as MySQL's own codes — `1044` (`ER_DBACCESS_DENIED_ERROR`), `1142` (`ER_TABLEACCESS_DENIED_ERROR`), `1143` (`ER_COLUMNACCESS_DENIED_ERROR`) — so a client classifies them by number.

Each query resolves the identity's privilege set **once**, from a lock-free snapshot cache gated on the catalog generation the service maintains. An unarmed database costs no reads at all.

Administering privileges requires `GRANT OPTION` at the **database** tier: the authorization information the parser attaches to `GRANT` and `REVOKE` names no target, so no finer tier can be checked. That is stricter than MySQL, which would let the holder of a table-level `GRANT OPTION` re-grant that one table.

Session management statements (`SET`, `SHOW`, `USE`, `BEGIN`, `COMMIT`) require no authorization.

### Storage

```
__sys:privs/{database}/__latch            # activation latch + monotonic catalog generation
__sys:privs/{database}/{grantee}          # privilege set (database + table + column tiers)
__sys:privs/{database}/{grantee}/__epoch  # per-grantee generation
```

All three move together in a single Raft compare-and-swap. The prefix lies under `__sys:`, which the internal-key guard already excludes from user key enumeration.


## Multi-Tenancy

Tenant isolation is enforced at the storage level. Keys are composed under the
`<tenant>/<db>/` namespace every other protocol uses
(`tenant.ComposeNamespace`), with the tenant ID taken from the client
certificate:

```sql
-- Client cert: CN=alice, OU=acme-corp; database shop
INSERT INTO kv (key, value) VALUES ('config/db', x'...');
-- Stored as: acme-corp/shop/config/db

SELECT * FROM kv WHERE key = 'config/db';
-- Reads: acme-corp/shop/config/db
-- Returns: key='config/db' (namespace stripped)
```

### Catalog visibility

The database *catalog* is scoped to the session's tenant as well, not just the
keyspace. The database manager is node-local and holds every tenant's databases
side by side, so `SHOW DATABASES`, `information_schema`, and
`pg_catalog.pg_namespace`/`pg_class` enumerate only the databases belonging to
the tenant on the connection's certificate.

Data access is scoped the same way: `service.Registry` resolves every read and
write against the session's own tenant, so another tenant's database name is
not found.

A database that carries no tenant ID is system-owned: visible to the system
tenant and to no one else. Treating it as ownerless would hide it from the
operator; treating it as everyone's would disclose it.


## Package Structure

```
pkg/sql/
  provider/       # DatabaseProvider, Database, KVTable, RelationalTable,
                  #   WriteSet, QRDBSession (qrdb_sql build tag)
  catalog/        # TableDef <-> sql.Type mapping, RowCodec, KeyCodec (qrdb_sql build tag)
  engine/         # Shared SQL engine factory (default analyzer + the privilege
                  #   post-analyze rule), PG translator + lexer (qrdb_sql build tag)
  privilege/      # SQL table privileges: the authorization handler every engine
                  #   installs, and the GRANT/REVOKE/SHOW GRANTS rewrite (qrdb_sql build tag)
  infoschema/     # information_schema additions (qrdb_sql build tag)
  lockclause/     # FOR UPDATE / FOR SHARE tokenizing and rewrite (qrdb_sql build tag)
  pgcatalog/      # pg_catalog subset (qrdb_sql build tag)
  query/          # ad-hoc /sql/query service (qrdb_sql build tag)
  internal/sqltest/ # SQL test harness, used only by the packages above (qrdb_sql build tag)
  internal/sessionguard/ # closes a SQL wire connection when a seal reaches it (qrdb_sql build tag)
  mysql/          # MySQL wire server (qrdb_mysql build tag)
  pgsql/          # PostgreSQL wire server (qrdb_pgsql build tag)
```

Every file under `pkg/sql/` — production and test alike, in all ten
packages above `mysql/` and `pgsql/` — carries `//go:build qrdb_sql`
directly, so the whole SQL core is absent from a build without the tag at
its own declaration, not merely at its callers'. Every consumer outside
`pkg/sql` — the server's ad-hoc `/sql/query` service
(`pkg/server/server_sql.go`), the desktop SQL binding
(`cmd/qrdb/bindings_sql.go`), the SDK's embedded SQL transport
(`sdk/go/transport/embedded/transport_sql.go`), and the `sdk/go/driver` /
`sdk/go/gorm` packages — compiles only under `qrdb_sql` too, so a build
without the tag links none of the SQL layer from either direction.

`pkg/api/errclass`, reached by every transport, imports no vitess package:
its SQL-error parity samples use a local `numberedSampleError` carrying the
`Number() int` shape the classifier matches on. `test/buildgates`'
dependency-graph gate (above) is what catches a leak of this kind.

For in-process SQL, see [gorm.md](gorm.md) — it covers the two in-process DSN schemes (`node://` and `embedded://`), which differ in exactly whether the relational catalog is reachable.


## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Provider, tables, write set, session | `pkg/sql/provider` | go-qrdb |
| Type mapping, row and key codecs | `pkg/sql/catalog` | go-qrdb |
| Engine factory, PG translator and lexer | `pkg/sql/engine` | go-qrdb |
| SQL table privileges and the authorization handler | `pkg/sql/privilege` | go-qrdb |
| MySQL wire server | `pkg/sql/mysql` (`qrdb_mysql`) | go-qrdb |
| PostgreSQL wire server | `pkg/sql/pgsql` (`qrdb_pgsql`) | go-qrdb |
| `pg_catalog` subset, `information_schema` additions | `pkg/sql/pgcatalog`, `pkg/sql/infoschema` | go-qrdb |
| SQL engine | — | **go-mysql-server** (upstream) |
| PostgreSQL framing | — | **psql-wire** (upstream) |
| `database/sql` driver, GORM configuration for the stock MySQL dialector | `sdk/go/driver`, `sdk/go/gorm` | go-qrdb |
| Consensus underneath every commit | — | **go-quicraft** |

## Trust boundaries

- **mTLS only.** No password authentication on either wire protocol. The
  certificate CN maps to the username and the OU to the tenant ID.
- **Two authorization planes, disjoint by design** (above): service RBAC gates
  the endpoint and the database lifecycle; SQL table privileges govern
  everything inside a database.
- **Default-deny once armed.** A statement kind the authorization handler does
  not model is denied on an armed database rather than waved through.
- **TLS and auth must both be enabled** when either wire protocol is active;
  configuration validation refuses the combination otherwise.
- **Tenant isolation is enforced at the storage level**, not by the SQL layer
  trusting a session field.

## What this feature deliberately does not do

- **No server-wide privileges.** `SUPER`, `PROCESS`, `FILE`, `CREATE USER`,
  routine and trigger privileges, `ON FUNCTION`/`ON PROCEDURE`, `ON *.*` at any
  privilege, and host-qualified grantees other than `'user'@'%'` are **refused
  by name** rather than stored and never checked.
- **No `--skip-grant-tables` recovery mode.** The permanent system-tenant
  exemption is what makes arming a database safe.
- **Privileges are enforced for non-system tenants only.** A deployment running
  everything as the system tenant is unrestricted by design.
- **No cross-database transaction.** One CAS commit targets one Raft shard.
- **`TRUNCATE TABLE` does not scan or delete rows synchronously.** Row and
  index-entry keys carry a generation number; `TRUNCATE` bumps that number in
  one CAS and hands the orphaned old generation to a background sweeper
  (`service.ReclaimSweeper`) rather than paying an O(table size)
  scan-and-delete inline. The sweeper deletes nothing until every statement
  that could still be reading the old generation has finished or been failed
  with a retryable error, and each job is swept by the one node that can reach
  its store. `DROP TABLE` hands its rows to the same sweeper. Index entries
  are keyed by an index incarnation that is never handed out twice within a
  table — no catalog change can land while an `ALTER` rewrite is open and be
  undone by its write-back — so `RENAME INDEX` moves no data and a dropped
  index's keyspace can never become a live one's. **A table written under the pre-generation key
  layout must be dropped and re-created**: this version does not read that
  layout, and there is no migration. Such a table is refused (MySQL 1459 /
  SQLSTATE 55000) rather than read as empty; `DROP TABLE` still works.
  See [engineering.md](engineering.md#truncate-table-generation-scoped-keys-o1-regardless-of-table-size).
- **No read-set conflict detection by default** — see the isolation model in
  [engineering.md](engineering.md).
- **Isolation levels are accepted, not differentiated.** MySQL's
  `transaction_isolation` names lock-engine behaviour; QRDB's transactions are
  optimistic by default and the two do not correspond. Blocking behaviour is
  selected by the concurrency mode instead — `SELECT ... FOR UPDATE` /
  `FOR SHARE` always take real row locks, and a database or session in
  `pessimistic` mode locks the rows its DML touches. See
  [../row-locking/](../row-locking/).
- **A collection table's `PRIMARY KEY`, `REPLACE` and `ON DUPLICATE KEY UPDATE`
  hold under concurrency.** The `_id` column of a document-collection table is
  enforced by `CollectionService.InsertDocument`'s guarded write, so two
  concurrent claims on the same `_id` produce one success and one **1062** —
  the same strength a *relational* table's uniqueness gets from a compare in
  the same proposal as the row, reached by a different mechanism. `REPLACE
  INTO` is one guarded upsert per row, and every write computed from a row —
  `UPDATE`, the update half of `ON DUPLICATE KEY UPDATE` — is guarded by the
  revision it read, so a lost race is a **1213** rather than an overwrite. The
  table is still autocommit-only: a guarded statement holds end to end, a
  multi-statement read-modify-write does not. See
  [engineering.md](engineering.md#collection-table-schema) and
  [../collections/architecture.md](../collections/architecture.md#replace-into-and-on-duplicate-key-update).

## Companion documents

- [../row-locking/](../row-locking/) — optimistic vs pessimistic concurrency,
  lock clauses, the replicated lock table and deadlock detection.

- [gorm.md](gorm.md) — GORM over the stock MySQL dialector, DSN forms, migrations, error mapping,
  conformance harness.
- [upstream-gaps.md](upstream-gaps.md) — go-mysql-server defects and which are
  mitigated here.
- [constraint-column-usage-duplication.md](constraint-column-usage-duplication.md)
  — an open duplication with a pending decision.
