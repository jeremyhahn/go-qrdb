# Document Collections — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

## Problem

Storing JSON documents in a raw key-value store means every application invents
its own key scheme, its own ID allocation, and its own encoding — and then
cannot list "all documents of this kind" without knowing that private scheme.

## Shape chosen

A **collection** is a named document container inside a database, with a
declared ID strategy and an optional storage codec. Documents are
`map[string]interface{}` with a `uint64` ID.

```
database  →  collection  →  document (uint64 ID)
```

Collection metadata lives under `__sys:collections/{database}/{name}`; documents
live under `__col:{database}:{collection}#{incarnation}/{id}`, in a keyspace of
their own for each create of the name (see
[Incarnation-scoped keyspaces](#incarnation-scoped-keyspaces)). Both are inside
the reserved internal namespace, so a collection's storage layout is not
something a KV caller trips over or can enumerate.

Neither key carries a tenant segment. The tenant dimension comes from the
namespace the transport registry applies underneath — see
[Trust boundaries](#trust-boundaries).

### ID strategies

Chosen at creation, fixed for the collection's life:

| Strategy | Behaviour | Use when |
|---|---|---|
| `unix_nano` (default) | ID from the insert timestamp in nanoseconds | you want roughly time-ordered IDs and no coordination |
| `increment` | monotonic counter, Raft-committed per `(database, collection)` | you want dense, small, predictable IDs |
| `field_hash` | ID hashed from a named document field (`--field-hash`) | the document has a natural key and re-inserting it must be idempotent |

`increment`'s counter is a replicated sequence owned by the collection, and it
is **torn down with the collection**: a name that is dropped and created again
starts over at 1. The alternative — carrying the old high-water mark into the
new collection — was rejected because it delivers neither property the table
above promises, and because there is nothing for a restarted counter to collide
with: a drop deletes every document in the collection before it reclaims the
counter (see [Data flow](#data-flow)).

### Explicitly supplied IDs

A document may carry its own `_id`. That is what makes re-import work — export
a collection, recreate it, put the documents back under the IDs they had — and
it is the second way an ID reaches the key space, alongside the strategy. Two
allocators over one keyspace need a rule, and there is one:

**An explicit `_id` is a claim on an identity; a generated ID is an
allocation.** The claim wins, permanently:

- **It reserves the counter.** On an `increment` collection the sequence is
  raised past the supplied value *before* the document is stored, so no ID the
  collection generates afterwards can reach it. This is the rule the relational
  layer already applies to an explicit value in an `AUTO_INCREMENT` column (see
  [../sql/engineering.md](../sql/engineering.md)), and the rule MySQL applies.
  `unix_nano` and `field_hash` have no counter, so there is nothing to reserve
  and nothing is written.
- **It never overwrites.** An insert whose `_id` already names a stored
  document is refused with a document-already-exists error. Insert requires the
  document to be absent exactly as update requires it to be present, and that
  pair is what keeps the two verbs distinguishable.

The cost is stated rather than hidden: the counter only moves up, so an
explicit `_id` far above the collection's watermark permanently strands the IDs
beneath it. That is the cheaper of the two failures — a stranded range wastes
IDs in a 64-bit space, an unreserved one destroys documents.

#### Every wire, including SQL

The rule above is the collection's, not a transport's. A document inserted over
SQL and read back over REST must be the same document, and it is not one if the
ID changed on the way in — so the SQL surface, where a collection is exposed as
a table whose column 0 is `_id`, carries the claim exactly as the document
bodies on REST, gRPC, QUIC, MCP and the SDK do.

It did not, and the shape of the failure is the one this rule exists to remove:
column 0 was read on `UPDATE` and `DELETE` and **discarded on `INSERT`**, so
`INSERT INTO events (_id, kind) VALUES (42, 'deploy')` stored the document under
the next generated ID and reported success. The column was declared, accepted
and thrown away.

The refusal travels with the claim, and on this surface it is load-bearing
beyond its error message. A collection table reports a taken `_id` as a
**primary key violation** — MySQL error 1062, PostgreSQL `SQLSTATE 23505`,
`gorm.ErrDuplicatedKey` — which is both the SQL equivalent of REST's 409 and
the error `REPLACE INTO` and `INSERT ... ON DUPLICATE KEY UPDATE` are built
on: each is "insert, and on a primary-key violation carry the existing row into
a delete or an update". Without it neither statement did what it says. Both
silently inserted a *second* document under a generated ID and left the first
one untouched.

Two consequences follow, and both are contract rather than implementation
detail:

- **An `UPDATE` that changes `_id` is a re-identification**, and the new
  identity is claimed before the old one is released. The claim reserves the
  counter and is refused if the destination is occupied, so a move that cannot
  complete leaves the document where it was. Done the other way round — release
  first — a move onto a taken ID destroyed the document it was moving and
  reported an error. A move to `_id = 0` is refused outright: zero means
  "allocate one" on an insert and nothing at all here, and allocating a fresh
  identity for a statement that named zero would be the same silent
  substitution one statement over.
- **`_id` may be omitted on an insert**, spelled either as leaving the column
  out or as `_id = 0`, and then the collection allocates from its strategy.
  This is MySQL's convention for an `AUTO_INCREMENT` column and already what
  the service reads a zero `_id` as. Before, the column was `NOT NULL` with no
  default, so SQL could not reach the generated path at all: every insert had
  to name an ID, and every ID it named was discarded.

#### The identity is not a field

`_id` names a document; it is not one of the document's fields. It is
**consumed** into the identity by whichever verb received it and never stored
alongside the data, on any transport.

This matters twice. A collection's schema is auto-discovered from the keys of
the documents written to it, so an `_id` stored as a field became an entry in
that schema — and the SQL surface builds a collection table's columns from it,
on top of the `_id` it already declares as column 0. `SELECT *` returned two
columns called `_id`, one holding the document's real ID and one holding the
echo. It also made a document's *shape* depend on how its ID was decided: an
explicitly identified document carried a field a generated one did not, in the
same collection.

So the document read back from any transport is the document's data, and its
identity is the `id` beside it. `UpdateDocument` takes its ID from the request
and consumes a body `_id` the same way, so a read-modify-write that echoes the
ID back cannot reintroduce the field.

#### What a generated ID may overwrite

Exactly one case: a `field_hash` ID. That strategy derives the ID from a named
document field, so re-inserting a document with the same natural key resolves
to the same ID **on purpose** — the idempotent re-insert the table above
promises. Everything else is refused:

| ID origin | `unix_nano` | `increment` | `field_hash` |
|---|---|---|---|
| explicit `_id` | refuse | refuse | refuse |
| generated | refuse | refuse | **overwrite** (idempotent re-insert) |

Once explicit IDs reserve the counter, `increment` cannot reach an occupied ID
at all, so a refusal there means the allocator is wrong and the caller is told
rather than losing a document. `unix_nano` reaches one only when two inserts
read the same nanosecond, which is rare rather than impossible.

#### How the refusal is enforced

**The refusal is the write declining to store, not a check performed before
it.** An insert that may not overwrite is written with `PutIfAbsent`, the only
write whose outcome is defined under concurrency, and the
`CollectionDocumentExistsError` above reports that the write did not store.

It was a read followed by an unguarded write for one release, which closed only
the overwrite a single caller reaches with no concurrency at all — and not the
race between two concurrent inserts claiming the same explicit `_id`, where
both observed the ID free, both wrote, and one document was gone with no error
anywhere. It was written that way because a database with its own store had no
guarded write to reach for; its backend now offers one, and every other engine
already did, so the guard is the write itself on every path.

**`WriteMode` is not honoured for an insert that may not overwrite.** The
guarded write is synchronous because its purpose is to report which caller won,
and an asynchronous proposal has no answer to report. An insert that *may*
overwrite makes no claim and still honours it.

**A backend without the guarded write refuses the insert rather than degrading
to a read-then-write.** The only such backend is the remote proxy, whose wire
protocol does not carry the verbs; an insert through it fails with
`database: backend does not support atomic conditional writes` instead of
quietly offering the weaker promise.

**The SQL surface inherits the strengthened guarantee the same way it inherited
the weak one.** A collection table's writes go through the same
`InsertDocument`, so its `PRIMARY KEY` is now enforced by the same guarded
write that enforces the collection's, and a declared `PRIMARY KEY` answering
with MySQL 1062 means what the surface invites a reader to assume.

#### `REPLACE INTO` and `ON DUPLICATE KEY UPDATE`

go-mysql-server implements both statements as several calls — insert; on a
primary-key violation, delete the existing row and insert again (`REPLACE`), or
update it with expressions evaluated over it (`ON DUPLICATE KEY UPDATE`) — and a
collection table is autocommit-only, so no transaction can hold the calls
together. Left as separate writes, two concurrent `REPLACE INTO`s on one `_id`
interleaved: one found the ID free between the other's delete and re-insert,
reported an insert, and then had its document written over with nobody told.

Both are now **one guarded write per row** — `CollectionService.UpsertDocument`,
which decides "insert or replace" and stores the document in the same guarded
write: `PutIfAbsent` when nothing is stored, `CompareAndSwap` against the exact
bytes it read when something is, re-decided if a concurrent writer wins the
guard. It reports which it did and the document it displaced.

- **`REPLACE INTO`** calls it from the table's replacer. When it replaced a
  document it still reports the primary-key violation, carrying exactly the
  document it displaced, because that is how go-mysql-server counts two
  affected rows for a replacement (one for an insert, as MySQL does); the
  delete and re-insert go-mysql-server then issues for that row write nothing.
- **`ON DUPLICATE KEY UPDATE`** — and every `UPDATE` on a collection table —
  is a guarded write of the row the statement read: the stored document is
  read back, compared with that row, and replaced only while it still carries
  the revision that read returned. A document a concurrent writer changed in
  between is not overwritten with a value computed from what it used to be;
  the statement fails with **MySQL 1213** (`SQLSTATE 40001`), the code clients
  retry on, as a relational table's lost optimistic commit does.
- **`UPDATE … SET _id = N`** claims `N` with a guarded insert and releases the
  old ID with a guarded delete of the revision it read. If the old document
  changed or disappeared in between, the new copy is withdrawn — guarded by the
  revision the claim wrote — and the statement fails with 1213, so a move
  neither duplicates a document nor discards a concurrent write to it. If the
  release fails any other way, it may or may not have removed the old document,
  so withdrawing the copy could leave the document under neither ID: both are
  left, and the statement fails with an error that says so and that the SQL
  layer treats as an unknown outcome.

The revision a guarded write checks is a digest of the stored bytes, carried on
the in-process service response (`GetDocumentResponse.Revision`); the write
itself swaps against the bytes. It is a concurrency token for the SQL surface's
statement-level writes, not a wire field.

### Alternatives rejected

- **A single global ID strategy.** Rejected: the three cases above genuinely
  conflict. `increment` needs a coordinated counter; `unix_nano` needs none;
  `field_hash` needs to be deterministic. Forcing one makes two of the three
  wrong.
- **Schema enforcement on write.** Not taken. `CollectionInfo` carries a
  `schema` map, but documents are free-form; the schema is descriptive, not a
  validator. Enforcing a schema here would duplicate what the relational layer
  ([../sql/](../sql/)) already does properly, with DDL and types.
- **String document IDs.** Rejected: `uint64` keeps IDs cheap to encode and
  order-preserving in key space. The JSON representation is a string
  (`json:"id,string"`) so that large values survive JavaScript clients.
- **Bulk document import.** Deliberately absent — see the scope fence below.
- **Refusing an explicit `_id` on an `increment` collection outright.**
  Rejected, though it is the cleanest contract on paper: the strategy owns ID
  assignment or the caller does, never both, and a collision becomes impossible
  rather than merely handled. The cost is that it deletes the re-import
  workflow — recreate a collection, put the documents back under the IDs they
  had — from the one strategy whose IDs are small and stable enough for a
  caller to have recorded them elsewhere. Reserving the counter makes that
  workflow *safe* instead of *forbidden* at the price of one read per explicit
  insert, and it keeps `increment` consistent with the relational layer's
  `AUTO_INCREMENT`, which takes the same view for the same reason.
- **Refusing the collision alone, without reserving the counter.** Rejected as
  a half-fix. It converts the silent overwrite into an error, which is the
  minimum bar, but it leaves the collection's own allocator walking into IDs a
  caller already used — so the caller who did nothing wrong, the one inserting
  without an `_id`, is the one who gets the error. Reserving the counter is
  what makes that unreachable; refusing the collision is what makes the
  remaining cases loud. Both, not either.
- **Treating an insert with an explicit `_id` as an upsert.** This is what the
  code did before, and it was not a decision — it was the absence of one. An
  insert that destroys a stored document and reports success is
  indistinguishable from one that stored a new document, and `UpdateDocument`
  already exists for callers that mean "replace whatever is there".
- **Refusing an explicit `_id` on the SQL surface alone,** so the discard
  becomes an error instead of silence. This was the defensible fallback if
  honouring the claim proved impractical, and it did not: the SQL layer can
  read column 0, can reach the same reservation, and already had a vocabulary
  for the refusal in `sql.NewUniqueKeyErr`. Taking it anyway would have forked
  the contract per-surface — a caller who learned it from any other transport
  would carry a false expectation into this one — for no gain. The measurement
  that would have justified it was whether the column projection was
  load-bearing; it is not, it is index arithmetic over the field list, and
  moving it cost one line.
- **Keeping `_id` as a stored document field and filtering it out of the SQL
  projection instead.** Narrower, and wrong in the direction that matters. It
  suppresses the duplicate column without addressing why the field is there,
  and it leaves a document's shape depending on how its ID was decided — so the
  next surface to enumerate a document's keys finds the echo again. The SQL
  table still skips a discovered `_id` when building its columns, but as the
  projection's own contract (column 0 *is* the identity), not as the fix:
  collections written before this change still carry the key in their stored
  schema.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Collection metadata, document CRUD, ID strategies | `pkg/service` (`collection_service.go`, `collection_types.go`) | go-qrdb |
| Document encoding (storage codec) | `pkg/codec`, backed by **go-codec** | go-qrdb / go-codec |
| Underlying storage and MVCC | `pkg/storage`, `pkg/mvcc` | go-qrdb |
| Secondary indexes on documents | `pkg/index` — see [../secondary-indexes/](../secondary-indexes/) | go-qrdb |
| SDK surface | `sdk/go` (`Client.Collections()`) | go-qrdb |

## Data flow

1. `CreateCollection` claims the name by writing its metadata under
   `__sys:collections/{db}/{name}` in the **creating** state, builds every
   declared secondary index, and only then marks it **active** (see
   [Lifecycle states](#lifecycle-states)).
2. `InsertDocument` allocates an ID per the strategy, encodes the document with
   the collection's codec, writes it through the same service and Raft path as
   any other write, and re-reads the metadata to fence the write against a
   concurrent drop.
3. `ListDocuments` is **paginated** (`page_size` / `page_token`), because a
   collection is an unbounded set by construction.
4. `DropCollection` marks the collection **dropping**, then reclaims everything
   it owns — irreversible. The order is fixed: **documents, then every
   secondary index the collection owns, then the `increment` counter, then the
   metadata**. The metadata is what makes the collection resolvable, so it goes
   last and an interrupted drop is completed by re-issuing it — or by the
   sweeper, without anyone re-issuing it. Documents go first because each
   delete retracts that document's index entries, so the indexes are already
   empty when they are dropped. A step that fails fails the whole drop rather
   than being skipped — reporting success over an unreclaimed index or counter
   is what leaves an orphan behind forever.

### Lifecycle states

A collection's metadata records one of three states, and every read of the
metadata reports it:

| State | Entered by | Serves documents | Left by |
|---|---|---|---|
| `creating` | `CreateCollection`'s first write | no | the create's last write (`active`), or its own unwind or the sweeper (`dropping`) |
| `active` | the create, once every declared index exists | **yes** | `DropCollection` (`dropping`) |
| `dropping` | `DropCollection`'s first write, a failed create's unwind, the sweeper | no | the metadata delete that ends the teardown |

Metadata written before the state existed carries none and reads as `active`.

**A create never exposes a collection whose indexes are not built.** It used
to write the metadata live first and build the indexes after, so a create whose
second index failed left a collection that resolved, served writes, declared
three indexes with one built — and could not be retried, because the retry
failed with already-exists. Now the metadata is written `creating`, the indexes
are built, and a single guarded swap makes it `active`. The first write is a
`PutIfAbsent`, so it is also the claim on the name: of two concurrent creates
one wins and the other is told the collection exists, having built nothing. A
create that cannot build every index **unwinds**: it marks the collection
`dropping` and removes what it built through the drop's own teardown, so a
failed create has created nothing and the retry succeeds.

**A drop writes `dropping` before it tears anything down.** From that write on,
every document verb refuses the collection, so no write can land in a
collection whose indexes — and therefore whose `unique` constraints — are going
away. Reads and deletes refuse it too: a collection mid-teardown would answer
with a partial view, and its teardown removes every document itself.

**Every state is visible.** `GetCollectionInfo` and `ListCollections` report a
`creating` or `dropping` collection with its state and when it entered it
(`state_since`), rather than omitting it: an omitted crashed drop would be
invisible, and an operator could not tell an abandoned name from a free one.

`state_since` is the wall clock of the node that made the transition, and the
sweeper compares it with its own node's clock. Clock skew between nodes shifts
when a stuck collection is picked up by up to the skew — a node whose clock
runs ahead sweeps it early, one running behind sweeps it late — and nothing
else: an early sweep of an operation that is in fact still running costs
repeated idempotent work or a lost activation the creator reports, never a
wrong result. Keep node clocks synchronised to well inside the five-minute
threshold.

**Every create is a new incarnation.** Each create draws a random incarnation,
kept on the metadata, and everything the create owns lives in a keyspace named
by it. A collection dropped and created again under the same name is a
different incarnation in a different keyspace, so nothing still working on the
old one — a slow writer, a slow create, a slow teardown — can reach the new one.
See [Incarnation-scoped keyspaces](#incarnation-scoped-keyspaces).

**Every transition is a guarded swap of the metadata bytes it was decided
from.** A create's activation cannot resurrect a collection a drop has marked,
and the schema discovery an insert performs — which rewrites the metadata to
record new field names — cannot write a `dropping` collection back to `active`,
or a deleted one back into existence. It used to be a plain write of a copy read
at the start of the insert, and could do both.

#### Incarnation-scoped keyspaces

Every piece of state that belongs to one incarnation of a collection lives in a
keyspace named by that incarnation:

| State | Key or name |
|---|---|
| documents | `__col:{database}:{collection}#{incarnation}/{id}` |
| the `increment` counter | sequence `__collection:{collection}#{incarnation}` |
| secondary indexes | owner `collection:{database}/{collection}#{incarnation}`, key scope the document prefix above |
| the process-local ID generator | cache key `{database}:{collection}#{incarnation}` |

A new collection's name may not contain `#`, the separator between name and
incarnation, so no new collection's keys can be read as another's. A collection
that already exists under such a name keeps working.

Only the metadata key, `__sys:collections/{database}/{name}`, is shared by every
incarnation of a name; it is the one thing a teardown removes with a
compare-and-delete of the exact record it started from.

This is what makes a stale operation harmless **by construction** rather than
by a check it has to win. A writer that loaded the metadata of incarnation 1
writes to incarnation 1's keys; if the name has since been dropped and created
again, incarnation 2's documents and counter are under different keys, and the
stale write cannot overwrite, delete or renumber them. A teardown that resumes
long after somebody else finished the drop and created the name again
reclaims only incarnation 1's prefix and counter. A new incarnation starts in a
keyspace no earlier one ever wrote, so a document orphaned by a crashed writer
of an earlier incarnation can never appear in it — and there is nothing for a
create to purge.

**Legacy collections need no migration.** Metadata written before incarnations
existed carries none. Such a collection keeps the unsuffixed prefix
`__col:{database}:{collection}/` and the unsuffixed sequence name
`__collection:{collection}` its documents and counter already have, and is read,
written and dropped there. The next create of the name after it is dropped draws
an incarnation.

#### The write fence

Marking the collection `dropping` stops every write that starts afterwards. A
write already past its check when the mark lands is closed by a **fence**: after
a document write commits, the service re-reads the metadata — a linearizable
read — and, if the collection is no longer `active`, no longer exists, or is no
longer the incarnation the write was decided from, refuses the call and
retracts the write.

The keyspace, not the fence, is what keeps such a write away from a live
collection: it went to its own incarnation's keys. The fence is what makes it
fail **loudly** — no write into a dropping or replaced collection is reported
as a success — and the retraction (a compare-and-delete of the exact bytes
written) is what keeps it from outliving its incarnation's teardown. The
ordering makes that sufficient: a drop's mark is **appended before** its
teardown scans the documents, so a write appended after that scan is appended
after the mark and its fence retracts it, and a write appended before the scan
is removed by the teardown. The same retraction reclaims the dead incarnation's
counter, which a stale insert may have recreated by allocating from it.

A write that loses its fence because the collection was dropped and created
again is refused as a conflict (the collection it was decided against is gone;
a retry writes into the new one). A retraction that itself fails is reported as
an internal error, not as the refusal, because the document may still be
stored.

One case the fence cannot close is a writer that dies between its write and its
fence, after the teardown's scan: its document outlives the drop in the dead
incarnation's keyspace, named by no metadata and reachable from no collection.
It costs its bytes of storage and nothing else.

#### The teardown

A teardown — a drop, a swept drop, a failed create's unwind — removes one dead
incarnation's keyspace. It scans the incarnation's document prefix and deletes
what it finds in the order the scan returned it, checking its deadline before
every delete so a bounded caller stops before its next write and the next
attempt resumes from what is left. It needs no guard against another incarnation — none shares
the keyspace — and takes none: no re-read of the metadata between batches and
no sort of the keys, so its cost is linear in the collection's size.

Each document delete is a compare-and-delete of the value scanned. That is
bookkeeping, not protection: it is how the teardown knows that **its** delete
removed the document, so it can report exactly the body bytes it freed, which is
what a storage quota releases. A document a concurrent guarded delete removed
first was released by that delete and is not counted again. It costs the one
proposal a plain delete would.

#### The sweeper

A create or drop can be interrupted — a crash, a leadership change, a failed
step — and leave its collection `creating` or `dropping`. The server runs a
**collection sweeper** that finishes them, so an operator never has to find the
collection and re-issue the operation by hand. Once a minute by default, each
node sweeps the databases it **already serves** and — where the store is
replicated — **leads**, and finishes every collection that has been
`creating` or `dropping` for longer than five minutes. "Already serves" is
deliberate: resolving a database starts what serving it needs — a
raft-isolated tenant's dedicated shard, a volume-isolated tenant's own host —
and the sweeper must never be the reason a node starts either.

How a node finds the databases depends on where they live:

- **The shared replicated store** — every prefix- and barrier-isolated
  tenant's databases — is swept from the system shard's leader. Its databases
  are found from a **collection work journal** kept in that same store: before
  a collection enters `creating` or `dropping`, the service writes an entry
  naming its tenant, database and collection under the internal
  `__sys:collection-work/` prefix, outside every database's keyspace. A node's
  own database catalog cannot serve here: a prefix tenant's named database is
  catalogued only on the node that created it, while its data — and its
  journal — is replicated, so after a leadership change the new leader finds
  the work regardless. An entry is removed once the collection it names is
  active or gone. Deleting a tenant removes its entries with the rest of its
  keyspace; an entry that somehow outlives its tenant is removed by the
  sweeper once stale.
- **A volume tenant's own host** is node-local: it is swept on every node where
  it is started, from the journal in its own store.
- **A raft tenant's dedicated shard** is swept where this node's replica leads
  it.
- **A database with its own node-local store** — a barrier over its own data
  directory — is swept on every node whose catalog holds it, without any
  leadership gating: each node's store is its own.

A database holding journaled work that stays unresolvable for three
consecutive passes is logged as a warning, so a stuck collection nobody can
reach does not stay silent. Each database gets a bounded share of a pass (30 seconds by
default); a teardown of a very large collection that runs past it resumes on
the next pass, and the databases after it are swept in the meantime.

- a stale `dropping` collection is torn down exactly as a re-issued drop would;
- a stale `creating` collection is **unwound, not completed** — marked
  `dropping` by a guarded swap and torn down — because the caller that issued
  it was never told it succeeded. A create that is merely slow loses its
  activation to that swap and reports the conflict rather than racing the
  sweeper.

The sweeper also removes **orphaned indexes**. Before a create builds its first
index it records the owner and the index names in an **index ledger** entry
kept with the database's keys. A creator that dies after building an index and
after its collection was removed leaves an index nothing else names; once its
ledger entry is stale and no metadata names its incarnation, the sweeper drops
those indexes, owner-checked, and the entry with them. A drop removes its own
collection's entry.

The sweeper acts on a stored record with nobody having asked, so it first
checks that the record is **self-consistent**: stored under the key its own
database and name derive, claiming the index owner its own incarnation derives,
and — for a ledger entry — naming only indexes of its own collection. A record
that is not is reported and left for an operator; nothing is torn down or
dropped on its word.

The threshold is what keeps the sweeper off an operation that is still running;
every step it takes is idempotent or guarded, so reaching one that is still
running costs repeated work, never a wrong result. The same property is why a
leadership change that briefly has two nodes sweeping one database is harmless.
A tenant whose barrier is sealed is skipped, not bypassed, and tried again on
the next pass.

### Drop order, and why the metadata key goes last

`DropCollection` tears down dependent structures **first** and deletes the
metadata key **last** — the same ordering, for the same reason, as relational
`DROP TABLE` in `pkg/sql/provider`, which reclaims a table's row and index data
before removing its catalog entry.

The metadata key is what makes a collection resolvable, so while it is present
the drop is re-issuable and finishes: a drop interrupted mid-documents, or
between the documents and the indexes, or between the indexes and the metadata,
is completed by running it again or by the sweeper. The index drop is
idempotent, so the retry costs nothing where it has already succeeded.

Every ordering has a bad interruption state, and before the `dropping` state
existed the chosen one left a live collection whose indexes were gone:

| Order | If it is interrupted before the last step |
|---|---|
| indexes, documents, metadata | The collection is **fully populated** with its declared indexes gone, for as long as the document deletion takes — the longest step. |
| documents, metadata, indexes | The metadata is gone, so nothing in the store names the surviving indexes. That state is **permanent**: no retry can find them, because the record that listed them was deleted. |
| **documents, indexes, metadata** (chosen) | The collection is **empty** and its indexes are gone, until a retry or the sweeper finishes it. |

The `dropping` state is what makes the chosen order's interrupted state
harmless rather than merely small. The collection is empty, partially indexed,
and — because it is `dropping` — refuses every write that would otherwise have
landed unindexed with its `unique` constraints unenforced. It used to accept
them, in a window one metadata delete wide when every index drop succeeded and
as wide as an operator's response time when one failed partway through the
list.

An index that cannot be dropped **fails the whole operation** rather than being
skipped. Index creation is idempotent for an index's own owner and the
*existing* definition wins, so an index a collection left behind would silently
override the definition a later create of the same collection asked for.
Reporting success over that is worse than reporting the failure, and because the
metadata survives the drop is retried. An index reported as *already absent* is
not a failure: a teardown asked to remove something that is not there has got
what it asked for.

### Index ownership

A collection's indexes are named `{database}.{collection}.{field}`, but a name
is not an owner. Every index a collection creates is **recorded as owned by
that collection** — by that *incarnation* of it: the owner is
`collection:{database}/{name}#{incarnation}`, with a fresh incarnation for each
create, kept on the collection's metadata as `index_owner`. The record lives
beside the index in the state machine, carried on the index-create command (see
[../secondary-indexes/engineering.md](../secondary-indexes/engineering.md)).

- **A create never adopts an index it did not make.** An index under one of the
  collection's derived names that somebody else holds — made through the
  explicit index API, by another collection whose `.`-joined name collides
  (`users` + `v2.email` and `users.v2` + `email` derive the same name), or by
  an earlier incarnation of the same collection — refuses the create, which
  unwinds and reports the collision. It used to be adopted silently, with the
  existing definition rather than the requested one.
- **A drop removes only what it owns.** Its index drops are owner-checked on
  the apply path, in the same step as the removal, so an index somebody else
  now holds under one of its names is left in place.
- **A create that loses its activation — or whose unwind finds the record
  already moved on — removes what it built.** If the collection was dropped
  while its indexes were being built, the drop's teardown may have run before
  some of them existed; the creator removes its own — and, because the owner
  names the incarnation, cannot remove anything a later create of the same name
  builds.
- **The explicit index API cannot drop a collection's index.** Its drop claims
  the explicit owner, so an index a collection owns is refused (409 /
  `FAILED_PRECONDITION`) rather than removed out from under the collection's
  `unique` constraint. A declared index that must go goes with its collection.
- **Two definitions of one field are refused.** They derive one index name, so
  the second would silently be the first.

Metadata written before ownership existed records no owner: its indexes were
created ownerless, and its drop removes the names it declares claiming the
explicit owner they were created with — so an index a collection has since
come to own under one of those names is left in place. Whole-scope teardowns — `DROP DATABASE`, deleting a tenant — remove every
index in their scope whoever owns it: the scope owns everything in it.

## Trust boundaries

- Collections are addressed as `(database, name)` inside the authenticated
  tenant. The database travels in the request; the tenant never does.
- Permissions are split by action, not just by object: `collections:create`,
  `collections:read`, `collections:list`, `collections:drop`,
  `collections:insert`, `collections:update`, `collections:delete`. Dropping a
  collection is a distinct grant from deleting a document in it.
- Documents and metadata are stored under reserved key prefixes (`__col:`,
  `__sys:collections/`), so they are invisible to user KV enumeration.

### Tenant isolation is a property of the SERVICE, not of the key

A collection key names the database and never the tenant, so the isolation of a
document is entirely the isolation of the engines the collection service was
built over. Every transport therefore resolves its collection servicer per
request, through `service.Registry.CollectionService(tenant, database)`, exactly
as it resolves KV and MVCC. What that accessor returns is a collection service
built over the `<tenant>/<database>/`-namespaced engines, so the physical key
is `<tenant>/<database>/__col:{database}:{collection}#{incarnation}/{id}`.

**This was not always true, and the failure mode is worth recording.** Until the
accessor existed, every transport held ONE process-wide collection servicer
built over the raw, pre-namespace engines. Under `prefix` isolation — where
tenants share one state machine and are separated only by that prefix — two
tenants naming the same database and the same collection addressed the same
documents: one could read another's, one's insert could overwrite another's at a
`field_hash`-derived ID, and one's collection name denied the other's. The
registry wrapper that isolates every other engine was applied to the bundle's
KV and index servicers, and the collection service was copied past it.

The lesson generalises beyond collections: **a service inherits isolation from
the engines it holds, never from the engines its neighbours hold.** A composite
service that is copied rather than rebuilt over the wrapped engines is
unisolated no matter how well isolated the rest of the bundle is.

The database appears twice in the physical key — once in the namespace and once
inside `__col:` — which is redundant and deliberately left alone: removing it
would change the key format and require moving existing documents, and this
design moves no keys (see the upgrade note in
[engineering.md](engineering.md#upgrading-across-the-tenant-namespace-change)).

These compositions legitimately keep a single collection servicer, and each is
recorded rather than treated as an exception to explain away:

- **A registry-less composition** — single-database servers, and the
  embedded transport when its composition configures no service registry —
  where the whole store and the caller's own keyspace are the same set. An
  embedded transport given a registry (`embedded.WithServiceRegistry`)
  resolves each named database through it, exactly as the network
  transports do.
- **A dedicated-raft database's collection service** is the one its shard
  was built with, not rebuilt per resolution — its ID generators make its
  lifetime observable — and carries the database's drop-fence write guard on
  the context of each mutating call instead of through the engines it holds.
- **A resolution that already reached a dedicated store** — a `raft`-isolated
  tenant's per-database shard, a `volume`-isolated tenant's own Host, or a
  database carrying its own barrier over its own store. Those bundles are built
  over a store belonging to exactly one `(tenant, database)` pair already, and
  the accessor returns their collection service unchanged.

## What this feature deliberately does not do

- **No schema validation on write.** Documents are free-form.
- **No bulk import.** `qrdb batch import` targets the raw KV keyspace only;
  documents go in one at a time. This is stated in the CLI help and is a real
  constraint, not an oversight to route around.
- **No query language over documents.** Filtering is by secondary index or by
  the SQL layer, not by a document query DSL.
- **No partial update.** `UpdateDocument` replaces the document.
- **No compound or case-insensitive collection indexes.** A collection index
  definition names one field, `unique` and `sparse`; compound and
  case-folding indexes are the explicit index API's
  ([../secondary-indexes/](../secondary-indexes/)).
