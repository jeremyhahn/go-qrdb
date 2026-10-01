# Row Locking — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

Row locking has one configuration knob outside SQL — a database's concurrency
mode — and it is set through the `qrdb database` verb group. Everything else is
SQL issued over the wire protocols, the in-process driver or `qrdb sql query`.
All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb database create <name> --sql-concurrency <mode>`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--sql-concurrency` | string | `optimistic` | `optimistic \| pessimistic` — the database's default SQL concurrency mode |

```bash
qrdb database create shop --sql-concurrency pessimistic
```

Every other `create` flag is unchanged; see
[../databases/cli.md](../databases/cli.md).

## `qrdb database update-sql <name>`

Change the concurrency mode of an existing database. New transactions pick it
up; a transaction already open keeps the mode it started with.

| Flag | Type | Required | Meaning |
|---|---|---|---|
| `--concurrency` | string | yes | `optimistic \| pessimistic` |

```bash
qrdb database update-sql shop --concurrency pessimistic
qrdb database info shop --output json    # "sql": {"concurrency": "pessimistic"}
```

An unknown mode exits non-zero with an invalid-argument error and changes
nothing.

## `qrdbd` configuration

There is no `qrdbd` flag. A database declared in the YAML file carries the
setting under its own `sql` section:

```yaml
databases:
  - name: shop
    engine: kv
    backend: pebble
    sql:
      concurrency: pessimistic
```

## SQL statements

These are not CLI verbs, but they are the rest of the feature's surface and work
the same through `qrdb sql query`, `mysql`, `psql` and the in-process driver.

```sql
SET qrdb_concurrency = 'pessimistic';     -- this session only; 'database' restores the default
SELECT @@qrdb_concurrency;

SET innodb_lock_wait_timeout = 5;          -- MySQL wire, seconds
SET lock_timeout = '5s';                   -- PostgreSQL wire

SELECT * FROM accounts WHERE id = 1 FOR UPDATE;
SELECT * FROM accounts WHERE id = 1 FOR SHARE;
SELECT * FROM accounts WHERE id = 1 LOCK IN SHARE MODE;
SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT;
```

`qrdb sql query` runs one statement per call with no transaction around it, so
a lock it takes is released when the statement ends. Locks are only useful
inside a transaction, over a wire protocol or the in-process driver.

## Parity

`qrdb database update-sql` and `--sql-concurrency` have `CommandDefinition`s in
the integration parity suite alongside the other `qrdb database` verbs.
