# Row Locking — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

Design and boundaries are in [architecture.md](architecture.md). This file is
the implementation contract: the record format, the invariants, configuration,
error codes, metrics and the test surface.

## Exported surface

`pkg/rowlock` — the lock table. It is SQL-agnostic: it locks opaque keys.

```go
type Mode uint8

const (
	ModeShared    Mode = 1
	ModeExclusive Mode = 2
)

// Deps are the (tenant, database)-scoped servicers one Table locks through.
type Deps struct {
	Txn   service.TransactionServicer
	MVCC  service.MVCCKVServicer
	Lease service.LeaseServicer
	Watch service.WatchServicer
}

func New(deps Deps, scope string) (*Table, error)

// Owner is one transaction's lock holder. Its zero value is not usable;
// NewOwner allocates its identity.
func NewOwner() *Owner
func (o *Owner) Acquire(ctx context.Context, t *Table, keys []string, mode Mode, wait time.Duration) error
func (o *Owner) Holds(key string, mode Mode) bool
func (o *Owner) Release(ctx context.Context) error
func (o *Owner) ID() string
```

`pkg/sql/provider`:

```go
const ConcurrencyVariable = "qrdb_concurrency"           // database | optimistic | pessimistic
const LockWaitTimeoutVariable = "innodb_lock_wait_timeout" // seconds, 1..1073741824, default 50

type SkipLockedUnsupportedError struct{}
type TransactionAbortedError struct{ Cause error }
```

`pkg/database`:

```go
type Concurrency string

const (
	ConcurrencyOptimistic  Concurrency = "optimistic"
	ConcurrencyPessimistic Concurrency = "pessimistic"
)

type SQLConfig struct {
	Concurrency Concurrency `yaml:"concurrency" json:"concurrency,omitempty"`
}

// DatabaseConfig, DatabaseMeta and DatabaseInfo gain:
//	SQL *SQLConfig `yaml:"sql" json:"sql,omitempty"`
```

`sdk/go/transport`:

```go
type DatabaseSQLConfig struct {
	Concurrency string `json:"concurrency,omitempty"` // optimistic | pessimistic
}

type UpdateDatabaseSQLRequest struct {
	Name string             `json:"-"`
	SQL  *DatabaseSQLConfig `json:"sql"`
}

// DatabaseClient gains:
//	UpdateDatabaseSQL(ctx context.Context, req UpdateDatabaseSQLRequest) (*DatabaseInfo, error)
// CreateDatabaseRequest and DatabaseInfo gain:
//	SQL *DatabaseSQLConfig `json:"sql,omitempty"`
```

## The lock record

Every lock request — granted or waiting — is exactly one record in the
database's own (registry-namespaced) store.

```
key    = "__sys:rowlock/" + hex8(len(rowKey)) + rowKey + "/" + ownerID + "/" + modeChar
prefix = "__sys:rowlock/" + hex8(len(rowKey)) + rowKey + "/"        // one row's queue
value  = [0x01 version][mode byte][8-byte big-endian enqueue time, unix nanoseconds]
lease  = the owning transaction's lease
```

- `hex8` is the row key's length as eight lowercase hex digits. The length
  prefix makes every queue prefix unambiguous whatever bytes the row key
  contains, without inflating the key the way hex-encoding the row key would.
- `ownerID` is 32 lowercase hex digits: 16 bytes from `crypto/rand`, fresh per
  transaction. It is an identity for the queue, not an authorization input.
- `modeChar` is `s` or `x`. An owner holding `S` that requests `X` writes a
  second record: an in-place upgrade would keep the `S` record's queue
  position and jump readers granted after it.
- The enqueue time is the requesting node's wall clock and is diagnostic only;
  ordering never depends on it.
- The prefix lies under `__sys:`, which the internal-key guard already keeps out
  of user key enumeration, MVCC restore and the SQL `kv` table.

## Invariants

1. **Queue order is create revision.** Records on a row are ordered by
   `CreateRevision`, then by key (records written by one transaction share a
   revision). Raft assigns the revision, so every replica and every node
   evaluates the same queue.
2. **Grant rule.** A record is granted iff every earlier record on its row
   whose owner differs is mode-compatible with it (`S`/`S` only).
3. **Grants are monotonic.** Records are only appended or deleted, so a granted
   record stays granted until its owner releases it.
4. **One lease per locking transaction.** Granted on the first lock, renewed
   every `DefaultLeaseTTL/3`, revoked by `Release`. Every record the owner
   writes carries it. `DefaultLeaseTTL` is 10 seconds.
5. **Release points.** `COMMIT` (after the commit proposal is decided, whether it
   succeeded or lost its CAS), `ROLLBACK`, the end of an autocommit statement,
   a deadlock-victim rollback, the start of a new transaction on the session
   (defensively), and the session's end (`SessionEnd`, connection close, driver
   `Close`). An infrastructure failure of `COMMIT` that leaves the write set in
   place for a retry also leaves the locks in place.
6. **Enqueue is one proposal per batch.** A statement's locks on many keys are
   written in as few transactions as the per-proposal ceiling allows, never one
   per key.
7. **A timed-out or cancelled request deletes its own waiting records** before
   it returns, so it stops blocking the requests queued behind it. Records the
   owner already held stay held.
8. **Deadlock detection runs once per wait, after enqueue, before waiting**,
   over at most `MaxDetectionRecords` (10,000) records read linearizably. A
   cycle through the requester, or a lock table larger than the bound, makes
   the requester the victim.
9. **A missing own record is a lost lock.** A waiter whose record disappeared
   (its lease expired, or the database's namespace was reclaimed) fails with
   `*rowlock.LockLostError` and its transaction is rolled back.
10. **One database per owner.** An owner is bound to the first database it
    locks in; locking in a second is refused with
    `provider.CrossDatabaseTransactionError` before anything is written.
11. **The commit CAS is unconditional.** Pessimistic mode changes what a write
    is compared against (the grant-time revision instead of the snapshot
    revision), never whether it is compared.

## Lock intent per statement

`pkg/sql/provider` derives the lock a table read must take from three inputs:

| Input | Source |
|---|---|
| The statement's lock clauses | the first statement of `ctx.Query()`, parsed with the engine's parser options and walked once; cached by text in a bounded map |
| The session's concurrency mode | `qrdb_concurrency`; `database` (the default) defers to the database's `sql.concurrency`; absent everywhere means `optimistic` |
| The session's lock-wait timeout | `innodb_lock_wait_timeout` (seconds) |

| Statement | Optimistic | Pessimistic |
|---|---|---|
| `SELECT` | none | none |
| `SELECT ... FOR UPDATE` / `FOR SHARE` | `X` / `S` on rows of the tables in the clause's query block | same |
| `UPDATE`, `DELETE` | none | `X` on every row the statement reads from its target tables, with current reads |
| `INSERT`, `REPLACE`, upsert | none | `X` on the new row key and every UNIQUE entry it occupies, then a current-read duplicate check |
| key-changing `UPDATE` | none | additionally `X` on the new row key and new UNIQUE entries |

Every surface that runs SQL sets the statement text on its `*sql.Context`:
the MySQL wire (go-mysql-server does it per command), the PostgreSQL wire (the
translated text, per statement), the in-process driver and the ad-hoc query
service.

### Current reads

For each locked key the provider reads the latest committed version
(`MVCCKVServicer.GetRevision`) **after** the grant and records it on the
transaction's write set as that key's baseline:

- a later read of the key in the same transaction returns the baseline (or
  the transaction's own buffered write);
- the key's commit precondition is `ModRevision == baseline` (or
  `Version == 0` when the key was absent), not the snapshot's state;
- in autocommit, the statement's own compare-and-swap carries the same
  precondition, so an autocommit pessimistic `UPDATE` is guarded against a
  writer that took no lock (1213 instead of a lost update).

## Configuration

Per database — YAML, at creation, and through `UpdateDatabaseSQL`:

```yaml
databases:
  - name: app
    engine: kv
    backend: pebble
    sql:
      concurrency: pessimistic   # optimistic (default) | pessimistic
```

An empty or absent value means `optimistic`. Any other value is refused with
`database.InvalidConcurrencyError`. Like every other per-database setting, it is
recorded in the database catalog of the node that applied it.

Per session:

```sql
SET qrdb_concurrency = 'pessimistic';   -- or 'optimistic', or 'database'
SET innodb_lock_wait_timeout = 5;       -- MySQL wire, seconds
SET lock_timeout = '5s';                -- PostgreSQL wire
```

- `qrdb_concurrency` is session-scoped only; `SET GLOBAL` is refused for the
  same reason as `qrdb_isolation`. It is sampled once per transaction.
- `innodb_lock_wait_timeout` is re-registered with MySQL's own range
  (1 to 1073741824 seconds) and default (50).
- PostgreSQL `lock_timeout` is translated to `innodb_lock_wait_timeout`: a
  duration or a bare millisecond count is rounded **up** to whole seconds, and
  `0` (PostgreSQL's "no limit") becomes the maximum. The PostgreSQL default is
  therefore 50 seconds, not unlimited.
- The PostgreSQL wire passes `SET [SESSION] qrdb_* {=|TO} value` through to the
  engine instead of treating it as a no-op, which is also what makes
  `SET qrdb_isolation` work there.

The driver DSN parameter `concurrency=optimistic|pessimistic` sets
`qrdb_concurrency` on every connection it opens.

## Error mapping

| Condition | Go type | MySQL | PostgreSQL | REST | gRPC / Unix / MCP | QUIC |
|---|---|---|---|---|---|---|
| Lock wait timed out | `*rowlock.LockWaitTimeoutError` | **1205** HY000 | **55P03** | 409 | `ABORTED` | `StatusAborted` |
| `NOWAIT` and the row is locked | `*rowlock.LockNotAvailableError` | **3572** HY000 | **55P03** | 409 | `ABORTED` | `StatusAborted` |
| Deadlock victim | `*rowlock.DeadlockError` | **1213** 40001 | **40P01** | 409 | `ABORTED` | `StatusAborted` |
| Lock request lost (lease expired, namespace reclaimed) | `*rowlock.LockLostError` | **1213** 40001 | **40001** | 409 | `ABORTED` | `StatusAborted` |
| Statement in a transaction already rolled back as a victim | `*provider.TransactionAbortedError` | **1213** 40001 | **25P02** | 409 | `ABORTED` | `StatusAborted` |
| `SKIP LOCKED` | `*provider.SkipLockedUnsupportedError` | **1235** 42000 | **0A000** | 501 | `UNIMPLEMENTED` | `StatusUnimplemented` |

- The MySQL wire forwards only `*mysql.SQLError` verbatim, so 1205, 3572 and
  1235 are rendered in that shape at the provider boundary; their numbers are
  exported as `rowlock.SQLCodeLockWaitTimeout`, `rowlock.SQLCodeLockNowait` and
  `provider.SQLCodeSkipLockedUnsupported`. Deadlock, lost-lock and aborted
  errors are wrapped in go-mysql-server's `sql.ErrLockDeadlock`, which keeps the
  typed cause reachable through `Cause()`.
- `pkg/api/errclass` classifies every row above, by Go type and by MySQL number,
  so the ad-hoc query capability reports the same class on all six transports.
- A deadlock victim's transaction is **aborted, not ended**: until the client
  issues `ROLLBACK` (or `COMMIT`, which reports the abort again), every
  statement that touches a relational table fails with
  `TransactionAbortedError`. This is PostgreSQL's behaviour and is stricter
  than MySQL, which would let the following statements autocommit outside the
  block the client opened.

## Observability

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `qrdb_sql_row_lock_requests_total` | counter | `mode`, `outcome` | lock requests by outcome: `granted`, `waited`, `timeout`, `nowait`, `deadlock`, `lost`, `canceled` |
| `qrdb_sql_row_lock_wait_seconds` | histogram | `mode` | time a request spent waiting, for requests that waited |
| `qrdb_sql_row_lock_waiters` | gauge | — | requests currently waiting on this node |

Lock records are visible to an operator with `qrdb kv` tooling against the
database's internal prefix only; they are never listed to SQL clients.

## Limits

| Limit | Value | Why |
|---|---|---|
| `DefaultLeaseTTL` | 10 s | how long a crashed holder's locks outlive it |
| keepalive interval | `DefaultLeaseTTL / 3` | two missed renewals are survivable |
| `MaxDetectionRecords` | 10,000 | bounds the deadlock detector's read |
| records per enqueue proposal | 1,000 | keeps an enqueue under the proposal ceiling |
| lock-wait timeout | 1 s – 1073741824 s, default 50 s | MySQL's own range |

## Test surface

- `pkg/rowlock`: grant rule and FIFO (table-driven over queues), shared
  compatibility, upgrade, re-entry, wait and wake-up on release, lock-wait
  timeout and `NOWAIT` cleaning up their waiting records, deadlock cycle
  detection (two- and three-party), the detection bound, lease expiry releasing
  locks, lost-lock detection, release idempotence, metrics. Run against a real
  in-memory MVCC store, lessor and watch manager, not against doubles that
  re-state the grant rule.
- Leader failover preserving locks: a three-node cluster where the leader is
  stopped while a lock is held and a second session is waiting; the waiter
  stays blocked until the holder commits on the new leader.
- `pkg/sql/provider`: lock-clause analysis, current reads, pessimistic DML,
  victim rollback and aborted state, release points, cross-database refusal.
- Wire: `FOR UPDATE` / `FOR SHARE` / `NOWAIT` / `SKIP LOCKED` and the `SET`
  overrides on both MySQL and PostgreSQL, with the error codes above.
- `test/integration`: two sessions serialising on `FOR UPDATE` across replicas,
  and a deadlock victim.
- `sdk/go/gorm/conformance`: `TestConcurrentMany2ManyAssociation` passes in
  pessimistic mode.
