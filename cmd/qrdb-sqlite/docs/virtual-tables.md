# Virtual Tables

All virtual tables are registered automatically when the SQLite driver is
initialised. No `CREATE VIRTUAL TABLE` statement is required.

## qrdb_kv

Flat key-value store. Key is `TEXT PRIMARY KEY`; value is an opaque `BLOB`.

```sql
CREATE TABLE x(key TEXT PRIMARY KEY, value BLOB)
```

### Examples

```sql
INSERT INTO qrdb_kv (key, value) VALUES ('cfg:timeout', '30s');
SELECT value FROM qrdb_kv WHERE key = 'cfg:timeout';          -- point lookup
SELECT key, value FROM qrdb_kv WHERE key LIKE 'cfg:%';        -- prefix scan
UPDATE qrdb_kv SET value = '60s' WHERE key = 'cfg:timeout';
DELETE FROM qrdb_kv WHERE key = 'cfg:timeout';
SELECT * FROM qrdb_kv;                                         -- full scan
```

### Index Strategy

| Constraint | Strategy | Cost |
|---|---|---|
| `key = ?` | Point lookup | 1 |
| `key LIKE 'p%'` or `key GLOB 'p*'` | Prefix scan | 10 |
| None | Full scan | 1 000 000 |

---

## qrdb_entity

Schema-flexible JSON entities with a synthetic `_id TEXT PRIMARY KEY`. Additional
columns are declared at module registration time based on the entity schema.

```sql
CREATE TABLE x(_id TEXT PRIMARY KEY, <field columns...>)
```

### Examples

```sql
INSERT INTO qrdb_entity (_id, name, score) VALUES ('u1', 'Alice', 99);
SELECT * FROM qrdb_entity WHERE _id = 'u1';
SELECT * FROM qrdb_entity WHERE score > 50;    -- range index pushdown
UPDATE qrdb_entity SET score = 100 WHERE _id = 'u1';
DELETE FROM qrdb_entity WHERE _id = 'u1';
```

### Index Strategy

| Constraint | Strategy |
|---|---|
| `_id = ?` | ID point lookup |
| `field = ?` | Equality index lookup |
| `field > ? / < ?` | Index range scan |
| None | Full scan |

---

## qrdb_collection

Schema-aware document collections. Schemas are auto-discovered from qrdbd at
connect time. Each collection is registered as its own virtual table module with
the collection name.

### Examples

```sql
-- Assuming collection "products" with columns: id TEXT, price REAL, in_stock INTEGER
SELECT id, price FROM products WHERE id = 'sku-001';    -- point lookup
INSERT INTO products (id, price, in_stock) VALUES ('sku-002', 19.99, 1);
UPDATE products SET in_stock = 0 WHERE id = 'sku-002';
DELETE FROM products WHERE id = 'sku-002';
```

### Auto-Discovery

On startup the gateway calls the collection service to list all known collection
schemas and registers each as a virtual table module. Collections created after
the gateway starts require a reconnect to be visible.

### Type Mapping

| qrdb type | SQLite type |
|---|---|
| `VARCHAR` | `TEXT` |
| `DOUBLE` | `REAL` |
| `BOOLEAN` | `INTEGER` |
| `BIGINT` | `INTEGER` |
| `JSON` | `TEXT` |
| `BLOB` | `BLOB` |

---

## qrdb_ts_datapoints

Raw time-series measurements. Timestamps are Unix nanoseconds.

```sql
CREATE TABLE x(metric TEXT, timestamp_ns INTEGER, value REAL, tags TEXT)
```

`tags` is a JSON array of `{"key": "...", "value": "..."}` objects.

### Examples

```sql
INSERT INTO qrdb_ts_datapoints (metric, timestamp_ns, value, tags)
VALUES ('mem.free', 1700000000000000000, 4096.0, '[{"key":"host","value":"db-1"}]');

-- Range query — pushed down to time-series range index
SELECT metric, timestamp_ns, value
FROM qrdb_ts_datapoints
WHERE metric = 'mem.free'
  AND timestamp_ns >= 1700000000000000000
  AND timestamp_ns <= 1700003600000000000;

-- All datapoints for one metric
SELECT * FROM qrdb_ts_datapoints WHERE metric = 'mem.free';
```

### Index Strategy

| Constraint | Strategy |
|---|---|
| `metric = ? AND timestamp_ns >= ? AND timestamp_ns <= ?` | Metric + range |
| `metric = ?` | Metric-only scan |
| None | Full scan (expensive) |

---

## qrdb_ts_metrics

Metric catalogue — lists all known metrics and their metadata.

```sql
CREATE TABLE x(name TEXT PRIMARY KEY, description TEXT, unit TEXT, retention_ns INTEGER)
```

### Examples

```sql
SELECT name, unit FROM qrdb_ts_metrics;
```

---

## qrdb_vector_namespaces

Lists vector namespaces and their configuration.

```sql
CREATE TABLE x(name TEXT PRIMARY KEY, dimensions INTEGER, distance TEXT)
```

`distance` is one of `cosine`, `euclidean`, `dot`.

### Examples

```sql
SELECT * FROM qrdb_vector_namespaces;
INSERT INTO qrdb_vector_namespaces (name, dimensions, distance)
VALUES ('embeddings', 768, 'cosine');
```

---

## qrdb_vectors

Raw vectors stored as `BLOB` of little-endian `float32` values.

```sql
CREATE TABLE x(id INTEGER PRIMARY KEY, namespace TEXT, vector BLOB, metadata TEXT)
```

### Examples

```sql
-- Upsert a vector
INSERT INTO qrdb_vectors (id, namespace, vector, metadata)
VALUES (42, 'embeddings', X'3f800000...', '{"source":"doc-1.pdf"}');

-- Fetch by composite key
SELECT id, metadata FROM qrdb_vectors
WHERE id = 42 AND namespace = 'embeddings';

-- Scan a namespace
SELECT id, metadata FROM qrdb_vectors WHERE namespace = 'embeddings';
```

### Index Strategy

| Constraint | Strategy |
|---|---|
| `id = ? AND namespace = ?` | Composite point lookup |
| `namespace = ?` | Namespace scan |
| None | Full scan |

### Limitations

Approximate nearest-neighbour (ANN) search is not expressible in plain SQL.
Use the QRDB SDK or gRPC API directly for similarity search. The virtual table
is intended for catalogue browsing, bulk ingestion, and individual record access.
