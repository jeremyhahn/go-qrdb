# SQL Wire Protocol

QRDB supports MySQL and PostgreSQL wire protocol compatibility, allowing standard SQL tools (`mysql`, `psql`, JDBC, pgx, ORMs) to interact with the KV store.

## Architecture

Both protocols share a single SQL engine ([go-mysql-server](https://github.com/dolthub/go-mysql-server)) and reuse the existing mTLS auth, RBAC, multi-tenancy, and audit systems.

```
SQL client --> Wire protocol layer --> Shared SQL engine --> KV service --> Raft
                (MySQL or PG)          (go-mysql-server)     (tenant-scoped)
```

### PostgreSQL Path

PostgreSQL clients connect via [psql-wire](https://github.com/jeroenrinzema/psql-wire). Queries are translated from PG syntax to GMS-compatible SQL before execution.

```
psql --> psql-wire (PG framing) --> PG translator --> go-mysql-server --> KVTable --> Raft
```

## Build Tags

SQL protocols are opt-in via build tags (enabled by default in the Makefile):

| Flag | Build Tag | Description |
|------|-----------|-------------|
| `WITH_QRDB_MYSQL=1` | `qrdb_mysql` | MySQL wire protocol |
| `WITH_QRDB_PGSQL=1` | `qrdb_pgsql` | PostgreSQL wire protocol |

Disable with `WITH_QRDB_MYSQL=0` or `WITH_QRDB_PGSQL=0`.

## Configuration

```yaml
server:
  mysql_port: 3306
  pgsql_port: 5432

protocols:
  mysql: true
  postgresql: true

sql:
  default_database: "qrdb"
  max_connections: 100
  query_timeout: "30s"

tls:
  enabled: true        # Required for SQL protocols
  ca_file: "ca.pem"
  cert_file: "server.pem"
  key_file: "server-key.pem"
  client_auth: "require_and_verify"

auth:
  enabled: true        # Required for SQL protocols
```

**Validation:** TLS and auth must be enabled when MySQL or PostgreSQL protocols are active.

### CLI Flags

```bash
qrdbd start \
  --enable-mysql --mysql-port 3306 \
  --enable-pgsql --pgsql-port 5432 \
  --sql-database qrdb \
  --tls-ca-file ca.pem --tls-cert-file server.pem --tls-key-file server-key.pem
```

| Flag | Default | Description |
|------|---------|-------------|
| `--enable-mysql` | `false` | Enable MySQL wire protocol server |
| `--mysql-port` | `3306` | MySQL wire protocol server port |
| `--enable-pgsql` | `false` | Enable PostgreSQL wire protocol server |
| `--pgsql-port` | `5432` | PostgreSQL wire protocol server port |
| `--sql-database` | `qrdb` | Default SQL database name |

## Authentication

Both protocols use mTLS exclusively -- no password authentication. The certificate Common Name (CN) maps to the username, and the Organizational Unit (OU) maps to the tenant ID.

```
Client cert (CN=alice, OU=acme-corp)
  --> mTLS handshake
  --> Identity{Subject: "alice", TenantID: "acme-corp"}
  --> User store enrichment (roles)
  --> QRDBSession (tenant-scoped)
```

## Authorization

Per-query RBAC authorization maps SQL statements to QRDB permissions:

| SQL Statement | Permission |
|---------------|------------|
| `SELECT ... FROM kv` | `kv:range` |
| `INSERT INTO kv` | `kv:put` |
| `UPDATE kv` | `kv:put` |
| `DELETE FROM kv` | `kv:delete` |
| `SELECT ... FROM status` | `admin:status` |
| `SELECT ... FROM peers` | `admin:peers` |

Session management statements (`SET`, `SHOW`, `USE`, `BEGIN`, `COMMIT`) are public and require no authorization.

## KV Table Schema

```sql
CREATE TABLE kv (
    key   VARCHAR(65535) PRIMARY KEY,
    value BLOB NOT NULL
);
```

## SQL Operations

| SQL | Service Call |
|-----|-------------|
| `SELECT * FROM kv` | `kvSvc.Scan(ctx, tenantPrefix)` |
| `SELECT * FROM kv WHERE key = ?` | `kvSvc.Get(ctx, prefixedKey)` |
| `INSERT INTO kv (key, value) VALUES (?, ?)` | `kvSvc.Put(ctx, prefixedKey, value)` |
| `UPDATE kv SET value = ? WHERE key = ?` | `kvSvc.Put(ctx, prefixedKey, value)` |
| `DELETE FROM kv WHERE key = ?` | `kvSvc.Delete(ctx, prefixedKey)` |

## Multi-Tenancy

Tenant isolation is enforced at the storage level. Keys are automatically prefixed with the tenant ID from the client certificate:

```sql
-- Client cert: CN=alice, OU=acme-corp
INSERT INTO kv (key, value) VALUES ('config/db', x'...');
-- Stored as: acme-corp/config/db

SELECT * FROM kv WHERE key = 'config/db';
-- Reads: acme-corp/config/db
-- Returns: key='config/db' (prefix stripped)
```

## Index-Accelerated WHERE Clauses

When a KV table has secondary indexes, the SQL analyzer automatically uses them to accelerate WHERE clauses.

```sql
-- If "email" is indexed:
SELECT * FROM kv WHERE key LIKE 'user:%' AND value LIKE '%alice%';
-- Uses email index for user: prefix, then post-filters value

-- Range queries:
SELECT * FROM kv WHERE key >= 'user:100' AND key <= 'user:200';
-- Uses key index (if available) for range scan
```

The query optimizer ([go-mysql-server](https://github.com/dolthub/go-mysql-server) Analyzer) examines available indexes and evaluates plan costs:

- **Full table scan:** Cost 1000 (O(n) items)
- **Index lookup:** Cost 10 (O(k) items matching predicate)
- **Index range scan:** Cost 50 (O(log n + k) range traversal)

The optimizer selects the lowest-cost plan.

### CREATE INDEX / DROP INDEX DDL

Create and drop indexes via standard SQL DDL:

```sql
-- Create unique index
CREATE UNIQUE INDEX email_idx ON kv (key) WHERE key LIKE 'user:%';

-- Create non-unique index
CREATE INDEX status_idx ON kv (value);

-- Drop index
DROP INDEX email_idx ON kv;
```

**Note:** QRDB's KV table is schemaless (key-value pairs), so indexes are applied at the column level. Column selection is performed via `LIKE` or prefix patterns.

### StatisticsTable Interface

QRDB implements the `StatisticsTable` interface from go-mysql-server, allowing the query optimizer to:

1. Enumerate available indexes
2. Estimate row counts per index
3. Cost different query plans
4. Select the optimal execution strategy

The optimizer queries the `information_schema.statistics` table to discover indexes:

```sql
SELECT * FROM information_schema.statistics
WHERE TABLE_NAME = 'kv' AND TABLE_SCHEMA = 'qrdb';
```

Returns:

```
TABLE_CATALOG | TABLE_SCHEMA | TABLE_NAME | NON_UNIQUE | INDEX_SCHEMA | INDEX_NAME | SEQ_IN_INDEX | COLUMN_NAME | COLLATION | CARDINALITY | COMMENT
qrdb          | qrdb         | kv         | 0          | qrdb         | PRIMARY    | 1            | key         | A         | 1000000     |
qrdb          | qrdb         | kv         | 1          | qrdb         | status_idx | 1            | value       | A         | 50          |
```

The optimizer uses these statistics to estimate query cost and select an execution plan.

## Transaction Control

QRDB provides per-operation atomicity via Raft. SQL `BEGIN`/`COMMIT`/`ROLLBACK` are accepted syntactically (clients and ORMs expect them) but each statement auto-commits independently.

## PostgreSQL Syntax Translation

The PG translator handles these transformations:

| PostgreSQL | GMS Equivalent |
|------------|----------------|
| `$1, $2` | `?, ?` |
| `value::type` | `CAST(value AS type)` |
| `ILIKE` | `LIKE` |
| `LIMIT ALL` | *(removed)* |
| `"identifier"` | `` `identifier` `` |
| `E'escape'` | `'regular'` |
| `RETURNING ...` | *(stripped)* |

## Client Examples

### MySQL

```bash
mysql --ssl-cert=client.pem --ssl-key=client-key.pem --ssl-ca=ca.pem \
  -h 127.0.0.1 -P 3306 -e "SELECT * FROM kv"
```

### PostgreSQL

```bash
psql "sslmode=verify-full sslcert=client.pem sslkey=client-key.pem sslrootcert=ca.pem \
  host=127.0.0.1 port=5432 dbname=qrdb" -c "SELECT * FROM kv"
```

## Package Structure

```
pkg/sql/
  provider/       # DatabaseProvider, Database, KVTable, QRDBSession (no build tag)
  engine/         # Shared SQL engine factory, PG translator (no build tag)
  mysql/          # MySQL wire server (qrdb_mysql build tag)
  pgsql/          # PostgreSQL wire server (qrdb_pgsql build tag)
```

## Makefile Targets

```bash
make test-sql-provider     # Provider unit tests
make test-sql-engine       # Engine + translator tests
make test-sql-mysql        # MySQL server tests
make test-sql-pgsql        # PgSQL server tests
make test-sql              # All SQL tests

make coverage-sql-provider # Provider coverage (91%+ required)
make coverage-sql-engine   # Engine coverage
make coverage-sql-mysql    # MySQL coverage
make coverage-sql-pgsql    # PgSQL coverage
make coverage-sql          # All SQL coverage
```
