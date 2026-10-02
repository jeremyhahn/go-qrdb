# SQL primary-key point statements vs table size

**Date**: 2026-10-01
**Benchmark**: `test/performance/sqlpk` (`make bench-sqlpk`)
**Before**: v0.25.2-alpha. **After**: the planner row-count estimate, statement-scoped
resolution and the per-connection row-lock lease (see `docs/sql/engineering.md`,
"Planner statistics" and "Statement-scoped resolution", and
`docs/row-locking/architecture.md`, "Lease binding").

## What is measured

One single-node `qrdbd` (pebble backend, TLS 1.3 on every listener) with a tenant,
a certificate-only SQL user and one database per SQL concurrency mode, provisioned
through the `qrdb` CLI exactly as an operator would. In each database the
benchmark creates

```sql
CREATE TABLE bench (id BIGINT NOT NULL PRIMARY KEY, v BIGINT, s VARCHAR(64))
```

and grows it through 0, 1,000, 16,000 and 64,000 rows (multi-row `INSERT`s of 500).
At every size it times 200 sequential statements of each shape over one MySQL-wire
connection (`go-sql-driver/mysql`, server-side prepared statements):

| Workload | Statement |
|---|---|
| insert | `INSERT INTO bench (id, v, s) VALUES (?, ?, ?)` |
| select | `SELECT v FROM bench WHERE id = ?` |
| update | `UPDATE bench SET v = ? WHERE id = ?` |
| delete | `DELETE FROM bench WHERE id = ?` |

The keys the timed statements use are disjoint from the fill, so the table holds
exactly the stated number of rows besides the ones a size's `insert` adds and its
`delete` removes. Every statement is autocommit; in `pessimistic` mode every
`UPDATE`, `DELETE` and `INSERT` takes its row locks implicitly.

A point statement touches one row, so its latency should not depend on the size of
the table. Before this change it did: every statement's plan counted the whole
table several times (`RowCount`).

Host: 24 vCPU Intel Xeon Gold 6258R, 61 GiB RAM, Linux, Go 1.27.0. One run per
build, on a shared development host; read single-digit-millisecond differences as
noise.

## Results — p50 / p95, milliseconds

### optimistic

| Workload | rows | before p50 | before p95 | after p50 | after p95 | p50 speedup |
|---|---:|---:|---:|---:|---:|---:|
| select | 0 | 1.58 | 3.96 | 0.99 | 1.65 | 1.6x |
| select | 1,000 | 4.38 | 9.07 | 0.97 | 1.76 | 4.5x |
| select | 16,000 | 39.66 | 64.16 | 1.54 | 2.86 | 25.7x |
| select | 64,000 | 126.58 | 171.40 | 1.56 | 2.06 | 81.1x |
| update | 0 | 3.56 | 6.82 | 2.86 | 4.79 | 1.2x |
| update | 1,000 | 6.29 | 13.15 | 3.18 | 5.19 | 2.0x |
| update | 16,000 | 51.45 | 83.68 | 4.94 | 7.87 | 10.4x |
| update | 64,000 | 152.33 | 223.50 | 3.37 | 5.18 | 45.1x |
| delete | 0 | 4.08 | 8.17 | 2.90 | 4.98 | 1.4x |
| delete | 1,000 | 5.47 | 12.00 | 2.94 | 5.89 | 1.9x |
| delete | 16,000 | 42.00 | 69.17 | 4.62 | 7.91 | 9.1x |
| delete | 64,000 | 145.55 | 178.00 | 3.81 | 6.62 | 38.2x |
| insert | 0 | 2.66 | 4.81 | 2.81 | 4.88 | 0.9x |
| insert | 1,000 | 3.04 | 5.87 | 2.70 | 5.45 | 1.1x |
| insert | 16,000 | 4.21 | 8.23 | 4.07 | 9.01 | 1.0x |
| insert | 64,000 | 3.59 | 6.78 | 3.27 | 5.25 | 1.1x |

### pessimistic

| Workload | rows | before p50 | before p95 | after p50 | after p95 | p50 speedup |
|---|---:|---:|---:|---:|---:|---:|
| select | 0 | 2.84 | 6.07 | 1.63 | 3.50 | 1.7x |
| select | 1,000 | 13.14 | 28.33 | 2.11 | 3.57 | 6.2x |
| select | 16,000 | 41.04 | 70.12 | 1.60 | 3.78 | 25.7x |
| select | 64,000 | 239.03 | 405.04 | 2.30 | 4.45 | 104.1x |
| update | 0 | 11.32 | 24.93 | 6.19 | 12.71 | 1.8x |
| update | 1,000 | 19.54 | 39.49 | 9.38 | 15.47 | 2.1x |
| update | 16,000 | 55.44 | 138.88 | 7.83 | 14.13 | 7.1x |
| update | 64,000 | 175.66 | 307.72 | 8.78 | 14.74 | 20.0x |
| delete | 0 | 11.69 | 21.32 | 5.92 | 9.71 | 2.0x |
| delete | 1,000 | 28.08 | 60.53 | 9.97 | 15.83 | 2.8x |
| delete | 16,000 | 58.29 | 104.47 | 7.13 | 12.78 | 8.2x |
| delete | 64,000 | 184.75 | 323.13 | 8.53 | 13.73 | 21.7x |
| insert | 0 | 7.67 | 16.50 | 6.19 | 9.23 | 1.2x |
| insert | 1,000 | 13.14 | 27.06 | 7.89 | 12.31 | 1.7x |
| insert | 16,000 | 9.74 | 16.95 | 7.66 | 13.12 | 1.3x |
| insert | 64,000 | 14.70 | 34.47 | 8.89 | 16.88 | 1.7x |

## Reading the results

- **Point statements no longer scale with the table.** Before, `select`, `update`
  and `delete` grew by about 1.7–2 µs per row in either mode — 127–239 ms at
  64,000 rows. After, they are flat across sizes within the run's noise.
  `insert` never scaled: go-mysql-server does not cost a plan for
  `INSERT ... VALUES`, so it never asked for the row count.
- **Pessimistic writes at 0 rows** dropped from about 11.5 ms to about 6 ms: a
  locking autocommit statement no longer grants and revokes a lease of its own
  (two Raft proposals). What remains is the lock enqueue, the row's own
  compare-and-swap and the release — three proposals — plus the lock-queue and
  current reads. Why the release is not folded into the row's write is in
  `docs/row-locking/engineering.md`, "Cost of a locking statement".
- **What is left that the shard still pays for size:** one background count per
  table generation, per node, every ten seconds, off every statement's path. It
  still holds go-quicraft's state-machine read lock while it runs, which
  `Apply` waits behind; see `docs/sql/engineering.md`, "Known limits".

## Atomic autocommit statements

A later change made every autocommit statement its own transaction (all its
rows or none), with the statement's row-lock release folded into its commit.
A single-row statement must not pay for that. The two builds were run
alternately, three rounds each, on the same host while other workloads were
running on it (load average 10–20), so each cell is the median of three runs.

Rounds of 400 statements per workload, tables of 0 and 1,000 rows (p50 / p95,
milliseconds):

| mode | workload | row-by-row p50 / p95 | atomic p50 / p95 |
|---|---|---:|---:|
| optimistic | insert | 3.21 / 6.47 | 3.42 / 7.12 |
| optimistic | select | 0.92 / 1.78 | 1.00 / 2.16 |
| optimistic | update | 3.18 / 6.94 | 3.39 / 6.94 |
| optimistic | delete | 3.25 / 6.41 | 3.54 / 8.75 |
| pessimistic | insert | 7.51 / 12.07 | 5.94 / 12.42 |
| pessimistic | select | 1.04 / 2.04 | 1.05 / 3.08 |
| pessimistic | update | 8.36 / 13.78 | 7.46 / 14.17 |
| pessimistic | delete | 8.75 / 14.68 | 7.07 / 14.56 |

The optimistic differences are within the run-to-run noise: `select`, whose
path the change does not touch, moved by as much. A tighter re-run of the
optimistic workloads (1,000 statements per workload, empty table, three
alternating rounds; p50 of each round, milliseconds):

| workload | row-by-row | atomic |
|---|---|---|
| insert | 2.86, 3.41, 3.06 | 3.02, 2.95, 5.59 |
| select | 0.95, 0.98, 1.15 | 0.91, 0.99, 0.98 |
| update | 7.24, 3.32, 3.53 | 3.33, 3.48, 2.99 |
| delete | 3.82, 3.57, 3.65 | 3.34, 4.47, 3.26 |

No single-row regression is measurable. In pessimistic mode a single-row
write is about 1–1.7 ms faster: its lock release is now part of its commit,
two proposals instead of three. An optimistic `INSERT` does one more read
than before (its duplicate check runs before the row is buffered rather than
after a failed write); at a local read's cost it does not show above the noise.

## Reproducing

```bash
make bench-sqlpk
make bench-sqlpk SQLPK_ARGS="-ops 500 -modes pessimistic"
```

`-sizes`, `-modes`, `-ops`, `-label`, `-work` and `-out` are the benchmark's
flags; it prints the Markdown table above and writes the raw results as JSON.
