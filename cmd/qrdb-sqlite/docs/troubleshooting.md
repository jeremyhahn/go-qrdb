# Troubleshooting

## CGo Build Errors

**`cgo: C compiler "gcc" not found`**

CGo requires a C compiler. Install one:

```sh
# Debian/Ubuntu
sudo apt install gcc

# macOS
xcode-select --install
```

Then rebuild with `CGO_ENABLED=1 make build-sqlite-gateway`.

**`sqlite3.h: No such file or directory`**

Install SQLite development headers:

```sh
sudo apt install libsqlite3-dev   # Debian/Ubuntu
sudo dnf install sqlite-devel     # Fedora/RHEL
```

---

## Connection Refused

**Symptom**: `dial tcp localhost:7070: connect: connection refused`

- Confirm `qrdbd` is running: `bin/qrdbd status`
- Check the port: `--server` must match the port in `qrdbd`'s config
- For Unix socket mode, verify the socket path exists: `ls /var/run/qrdbd.sock`
- For embedded mode, verify `--config` points to a valid YAML file

**Symptom in embedded mode**: `config: required when mode is embedded`

Pass `--config /path/to/qrdb.yaml`. Embedded mode cannot start without a server
configuration.

---

## Empty Tables

**`SELECT * FROM qrdb_kv` returns zero rows when data should exist**

1. Confirm you are connected to the correct database: `PRAGMA qrdb_status;`
2. Verify the database name matches what was used to write the data.
3. If using eventual consistency, the local replica may be behind. Switch to
   linearizable: `PRAGMA qrdb_consistency = 'linearizable';`
4. Confirm the qrdbd leader is healthy: `bin/qrdbd status`

**Collections not visible**

Collections are auto-discovered at gateway startup. If a collection was created
after the gateway connected, restart `qrdb-sqlite` to pick up the new schema.

---

## Slow Queries

**Full scan on a large table**

Ensure the query includes an indexed constraint:

```sql
-- Slow (full scan)
SELECT * FROM qrdb_kv WHERE value = 'x';

-- Fast (index pushdown)
SELECT * FROM qrdb_kv WHERE key = 'mykey';
SELECT * FROM qrdb_kv WHERE key LIKE 'prefix:%';
```

SQLite's query planner uses the cost estimates returned by `BestIndex`. If a
full scan is being chosen unexpectedly, run `EXPLAIN QUERY PLAN` to inspect:

```sql
EXPLAIN QUERY PLAN SELECT * FROM qrdb_kv WHERE key = 'x';
```

**Latency higher than expected for bulk writes**

Wrap bulk writes in a transaction. See [performance-tuning.md](performance-tuning.md).

---

## Transaction Errors

**`ROLLBACK` after a commit**

qrdb-sqlite flushes the batch on `COMMIT`. If the Raft proposal fails (leader
unavailable, quorum lost), the commit returns an error and the SQLite transaction
is rolled back. Check `qrdbd` cluster health and retry.

**Nested savepoints**

Nested `SAVEPOINT` is not supported. Only flat `BEGIN` / `COMMIT` / `ROLLBACK`
transactions are coalesced into batch proposals.

---

## Embedded Mode Issues

**High memory usage**

Embedded mode runs a full QRDB node in-process. PebbleDB allocates significant
block cache by default. Tune `storage.block_cache_mb` in the YAML config, or
switch to the `file` backend for lighter memory use.

**Port conflicts**

Embedded mode binds the gRPC and REST ports defined in the YAML config. If
another process owns those ports, startup fails. Edit the config to use
alternative ports or set them to `0` to disable unused listeners.

---

## Debug Mode

Run with `--debug` to see detailed logs including transport round-trips, index
strategy selection, and batch flush events:

```sh
bin/qrdb-sqlite shell --debug --mode client --server localhost:7070 --database mydb
```

Logs are written to stderr and do not interfere with SQL output.
