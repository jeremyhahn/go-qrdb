# Quickstart

## Client Mode — Connect to a Running qrdbd

Start a `qrdbd` daemon (see the main qrdb docs), then open the interactive SQL
shell against it:

```sh
bin/qrdb-sqlite shell \
  --mode client \
  --server localhost:7070 \
  --database mydb \
  --transport grpc
```

### Key-Value

```sql
-- Write
INSERT INTO qrdb_kv (key, value) VALUES ('user:1', 'alice');
INSERT INTO qrdb_kv (key, value) VALUES ('user:2', 'bob');

-- Point lookup (index pushdown — no full scan)
SELECT value FROM qrdb_kv WHERE key = 'user:1';

-- Prefix scan
SELECT key, value FROM qrdb_kv WHERE key LIKE 'user:%';

-- Update
UPDATE qrdb_kv SET value = 'alice2' WHERE key = 'user:1';

-- Delete
DELETE FROM qrdb_kv WHERE key = 'user:1';
```

### Entity

```sql
-- Create an entity (JSON document stored as structured fields)
INSERT INTO qrdb_entity (_id, name, age) VALUES ('e1', 'Alice', 30);

-- Query by primary key
SELECT * FROM qrdb_entity WHERE _id = 'e1';

-- Equality index lookup (pushed down)
SELECT * FROM qrdb_entity WHERE age = 30;
```

### Collection (schema-aware documents)

```sql
-- Collections are auto-discovered from qrdbd at connect time.
-- Assuming a collection named "orders" exists with columns: id, amount, status
SELECT id, amount, status FROM orders WHERE id = '123';
INSERT INTO orders (id, amount, status) VALUES ('124', 99.95, 'pending');
```

### Time Series

```sql
-- Write datapoints
INSERT INTO qrdb_ts_datapoints (metric, timestamp_ns, value, tags)
VALUES ('cpu.usage', 1700000000000000000, 42.5, '[{"key":"host","value":"web-1"}]');

-- Range query (pushed to service layer — no full scan)
SELECT metric, timestamp_ns, value
FROM qrdb_ts_datapoints
WHERE metric = 'cpu.usage'
  AND timestamp_ns >= 1700000000000000000
  AND timestamp_ns <= 1700003600000000000;

-- Metric catalogue
SELECT name, description FROM qrdb_ts_metrics;
```

### Vectors

```sql
-- Create a namespace first
INSERT INTO qrdb_vector_namespaces (name, dimensions, distance)
VALUES ('embeddings', 384, 'cosine');

-- Upsert a vector (BLOB of float32 values, little-endian)
INSERT INTO qrdb_vectors (id, namespace, vector, metadata)
VALUES (1, 'embeddings', X'...', '{"doc":"readme.md"}');

-- Scan all vectors in a namespace
SELECT id, metadata FROM qrdb_vectors WHERE namespace = 'embeddings';
```

### Transaction Batching

```sql
BEGIN;
INSERT INTO qrdb_kv (key, value) VALUES ('tx:a', '1');
INSERT INTO qrdb_kv (key, value) VALUES ('tx:b', '2');
UPDATE qrdb_kv SET value = '3' WHERE key = 'tx:a';
COMMIT;
-- All three mutations are sent as a single batch proposal to the Raft leader.
```

---

## Embedded Mode — Standalone In-Process Node

```sh
bin/qrdb-sqlite shell \
  --mode embedded \
  --config configs/qrdb-dev.yaml \
  --database mydb
```

The gateway starts a full QRDB Raft node in-process. All SQL above works
identically — the only difference is that data is stored locally according to
the backend configured in `configs/qrdb-dev.yaml` (memory, file, or pebble).

This mode is ideal for development, testing, and single-binary deployments.
