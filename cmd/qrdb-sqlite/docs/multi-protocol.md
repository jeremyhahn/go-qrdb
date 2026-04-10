# Multi-Protocol Access

## How Concurrent Access Works

All protocol handlers — gRPC, REST, QUIC, MCP, and the SQLite gateway — share
the same service layer (`pkg/service/`). Every write, regardless of originating
protocol, goes through the service layer as a Raft proposal to the leader. Reads
are served from the same replicated state machine.

This means a Python client writing via the REST API, a Go service using gRPC, and
a data analyst running SQL through `qrdb-sqlite` all see the same data with the
same consistency guarantees.

```
Python (psql / REST)  ──►┐
Go service (gRPC)     ──►├──► Service layer (pkg/service/) ──► Raft ──► Storage
Java client (MySQL)   ──►┤
qrdb-sqlite (SQL)     ──►┘
```

## Example: Polyglot Stack

Scenario: a web application writes orders via gRPC, a Python analytics job reads
via REST, and a DBA inspects data via `qrdb-sqlite`.

**Go service writes an order (gRPC):**

```go
client.KV().Put(ctx, "order:1001", orderJSON)
```

**Python analytics reads (REST):**

```python
resp = requests.get("http://qrdbd:8080/v1/kv/order:1001")
```

**DBA inspects via SQLite:**

```sql
SELECT key, value FROM qrdb_kv WHERE key LIKE 'order:%';
-- Returns the row written by the Go service.
```

All three operations target the same logical database. The DBA does not need a
separate database connection or schema export.

## Consistency Across Protocols

Each protocol independently sets its consistency level per request. A
`linearizable` read from any protocol goes to the Raft leader and reflects the
latest committed state. An `eventual` read may return data from any follower
and may be slightly behind.

If the Go service writes with linearizable consistency and the Python job reads
with eventual consistency, the Python job may briefly see stale data. Set both to
`linearizable` when strict ordering across protocols matters.

For `qrdb-sqlite`:

```sql
PRAGMA qrdb_consistency = 'linearizable';
SELECT * FROM qrdb_kv WHERE key = 'order:1001';
```

## Schema Visibility

- **KV and entity tables** have no schema coordination — any client can write
  any key at any time.
- **Collections** have a schema stored in qrdbd. Changes to the collection schema
  (e.g. adding a column) require the `qrdb-sqlite` gateway to reconnect to
  pick up the new schema. Running collections are auto-discovered at startup;
  new collections created after startup require a reconnect.
- **Time-series metrics** are registered on first write and visible to all
  protocols immediately.
- **Vector namespaces** are created explicitly and visible to all protocols.

## Practical Patterns

**Seed data via SQL, consume via gRPC:**

```sql
BEGIN;
INSERT INTO qrdb_kv (key, value) VALUES ('config:feature_x', 'enabled');
INSERT INTO qrdb_kv (key, value) VALUES ('config:max_retries', '3');
COMMIT;
```

The Go service reads these keys over gRPC without any extra coordination.

**Export a snapshot for analysis:**

```sh
bin/qrdb-sqlite shell \
  --mode client \
  --server localhost:7070 \
  --consistency eventual \
  --chunk-size 1024
```

```sql
.output /tmp/kv_snapshot.csv
.mode csv
SELECT key, value FROM qrdb_kv;
.output stdout
```

**Mixed-write safety:** concurrent writes from multiple protocols are serialized
by the Raft leader. No client-side locking is needed. Last-write-wins semantics
apply for the same key written from two protocols simultaneously.
