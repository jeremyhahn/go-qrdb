# Document Collections — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

A collection is a named box for JSON documents inside a database. You create the
box, decide how document IDs are generated, then put documents in.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the
`collections:*` permissions for the verbs you use. Note that dropping a
collection and deleting a document are **separate** grants.

## First run

```bash
qrdb collection create users --database app
qrdb collection doc insert users --database app \
     --data '{"email":"alice@example.com","status":"active"}'
```

The insert prints the document `id`. Read it back:

```bash
qrdb collection doc get users <id> --database app
```

## Choosing an ID strategy

You pick this at creation and cannot change it later.

| Strategy | What you get | Pick it when |
|---|---|---|
| `unix_nano` (default) | ID from the insert time in nanoseconds | you just want unique, roughly time-ordered IDs |
| `increment` | 1, 2, 3, … | you want small dense IDs and human-readable references |
| `field_hash` | ID derived from a field you name | the document has a natural key and re-inserting it should be idempotent |

```bash
qrdb collection create events   --database app --id-strategy increment
qrdb collection create products --database app --id-strategy field_hash --field-hash sku
```

With `field_hash`, inserting the same `sku` twice lands on the same ID rather
than creating a duplicate — that is the point of it, and it is the only case
where an insert replaces a document instead of refusing.

## Supplying your own document ID

Put an `_id` in the document and it is used verbatim instead of a generated
one. This is how you re-import documents under the IDs they already had.

```bash
qrdb collection doc insert events --database app \
     --data '{"_id": 42, "kind": "deploy"}'
```

Two rules apply, on every strategy:

- **An `_id` you supply is reserved.** On an `increment` collection the counter
  jumps past it, so the next generated ID is `43` and can never come back round
  to `42`. You can mix your own IDs and generated ones freely.
- **An `_id` that is already taken is refused,** not overwritten. You get a
  "document already exists" error (HTTP 409). If you meant to replace the
  document, use `update`; if you meant to add one, leave `_id` out.

The reservation only moves the counter **up**. Insert `{"_id": 1000000}` into a
fresh `increment` collection and the IDs beneath it are gone for good — that is
deliberate, and it is why small explicit IDs are the ones worth supplying.

`_id` is not stored as one of the document's fields; it names the document. Read
`{"_id": 42, "kind": "deploy"}` back and you get ID `42` with a document of
`{"kind": "deploy"}` — the same shape you would get had you let the collection
allocate the ID.

Two field shapes are refused with a 400, because the stored document could not
hold them as sent: a field named `id` (a stored document keeps its own
identity under that key — name the identity with `_id`, or rename the field),
and a document whose only field is an object named `fields` (it would read
back as that object's contents — add another field or rename it).

## Doing the same thing over SQL

A collection is also a SQL table whose first column is `_id`, and the rules
above are the collection's, not the CLI's — they hold identically here.

```sql
-- Claim an id. Refused with a duplicate-key error (1062, or SQLSTATE 23505 on
-- the PostgreSQL port) if 42 is taken; the stored document is not touched.
INSERT INTO events (_id, kind) VALUES (42, 'deploy');

-- Let the collection allocate. Leave the column out, or write 0.
INSERT INTO events (kind)      VALUES ('deploy');
INSERT INTO events (_id, kind) VALUES (0, 'deploy');

-- Copy a collection and keep every id -- what a restore is made of.
INSERT INTO archive (_id, kind) SELECT _id, kind FROM events;

-- Replace, or update on collision, instead of being refused.
REPLACE INTO events (_id, kind) VALUES (42, 'redeploy');
INSERT INTO events (_id, kind) VALUES (42, 'redeploy')
  ON DUPLICATE KEY UPDATE kind = 'redeploy';

-- Move a document to another id. Refused, with nothing destroyed, if the
-- destination is taken.
UPDATE events SET _id = 99 WHERE _id = 42;
```

Two things to know before you lean on it:

- **A collection table is autocommit-only.** Any of these inside `BEGIN` fails
  rather than silently escaping the transaction.
- **The primary key holds under concurrent writers.** Two clients claiming `42`
  at the same instant get one success and one duplicate-key error; the insert
  is a guarded write, so it is not possible for both to be told they succeeded.
- **`REPLACE` and `ON DUPLICATE KEY UPDATE` are still not atomic.** Each is an
  insert, a refusal and a second write, with no lock across the three, so two
  of them racing on `42` can interleave. Neither can lose a document to the
  other's *insert*, which is the case the guarded write closed. If you need a
  read-modify-write that holds end to end, use a relational table
  ([../sql/user.md](../sql/user.md)), which can run it inside a transaction.

## Common tasks

**Insert from a file or a pipe.**

```bash
qrdb collection doc insert users --database app --file user.json
jq -c . user.json | qrdb collection doc insert users --database app --file -
```

**Replace a document.** `update` replaces, it does not merge — send the whole
document.

```bash
qrdb collection doc update users <id> --database app \
     --data '{"email":"alice@example.com","status":"disabled"}'
```

**Page through everything.** One page is not the collection; follow the token.

```bash
TOKEN=""
while :; do
  OUT=$(qrdb collection doc list users --database app \
        --page-size 100 ${TOKEN:+--page-token "$TOKEN"} --output json)
  echo "$OUT" | jq -c '.documents[]'
  TOKEN=$(echo "$OUT" | jq -r '.next_page_token // empty')
  [ -z "$TOKEN" ] && break
done
```

**Check what is in a collection without listing it.**

```bash
qrdb collection info users --database app --output json | jq .document_count
```

**See every collection in a database.**

```bash
qrdb collection list --database app
```

**Index a field.** Declare it when you create the collection; the collection
builds the index, keeps it maintained, and drops it with itself.

```bash
qrdb collection create accounts --database app --index email,unique
```

Every transport takes the same declaration — `"indexes":
[{"field_name":"email","unique":true}]` in a REST or MCP body, the `indexes`
field on gRPC, QUIC and the SDK. The collection only starts accepting documents
once every declared index exists, so no document can slip in unindexed or past
a `unique` constraint.

**Delete a collection.** This removes every document in it, along with every
secondary index the collection created, and cannot be undone. The collection
stops accepting documents the moment the drop starts. If the command fails
partway through, the collection is listed as `dropping` — run it again and it
finishes, or leave it: the server finishes it within a few minutes.

```bash
qrdb collection drop scratch --database app
```

A drop also discards the collection's `increment` counter, so creating a
collection with the same name afterwards starts its IDs at 1 again rather than
continuing where the old one left off. If a drop reports an error, nothing is
half-deleted in a way you have to clean up by hand — run the same command again
and it finishes, or let the server finish it.

**Know what state a collection is in.** `collection info` and `collection list`
report `active`, `creating` (its indexes are being built) or `dropping` (it is
being deleted). Only an `active` collection serves documents.

## Collections belong to your tenant

A collection lives inside one `(tenant, database)` pair. The `--database` you
pass selects the database; the tenant comes from the certificate you connect
with and is never something you send. Two tenants can use the same database
name and the same collection name without seeing, overwriting or blocking each
other — the names are yours, not the cluster's. That extends to the `increment`
counter above: it is your collection's, not the name's.

If you are upgrading a deployment that already had collections, read
[engineering.md](engineering.md#upgrading-across-the-tenant-namespace-change)
first: collections written before the tenant namespace existed are not listed
after the upgrade, and nothing moves them for you. Export them with the older
binary and re-import them as the tenant that should own them.

## Troubleshooting

**"collection ... is creating, not active" or "... is dropping, not active".**
The collection is mid-create or mid-drop. A create finishes in moments; retry.
A collection that stays `creating` or `dropping` was interrupted — the server
finishes it within a few minutes (a stuck create is undone, not completed, so
create it again afterwards). You do not need to intervene.

**A create with `--index` was refused because the index name is taken.** An
index named `{database}.{collection}.{field}` already exists and belongs to
something else — usually one made with `qrdb index create`. The collection will
not share it: it would inherit that index's definition rather than yours, and a
drop would then delete it. Drop the other index or rename the field.

**A document write was refused for quota.** Collection documents count against
the tenant's storage limit like any other data — the size of each document's
body. Delete documents, or ask for a higher limit.

**A `REPLACE INTO` or `UPDATE` on a collection table failed with 1213
(serialization failure).** Another writer changed the same document between
your statement reading it and writing it. Nothing was written; retry the
statement.

**An `UPDATE ... SET _id = ...` failed saying both ids are left.** The document
was stored under its new `_id`, and removing it from the old one failed in a
way that does not say whether the removal happened. Nothing was withdrawn, so
the document is under the new `_id` and possibly still under the old one too:
read both, and delete the old one if it is still there.

**"collection not found" on an insert.** Collections are never created
implicitly. Check the spelling and the `--database` — a typo would otherwise
have quietly produced a second collection that then diverges from the first.

**Your collections vanished after an upgrade.** If they were created before the
tenant namespace existed, they are still on disk but no longer reachable, for
the reason above. Nothing deleted them. Roll back to the previous binary,
export, upgrade, re-import.

**"database not found" on a collection command that used to work.** A
collection request resolves the database you name, so a database that was never
created is now refused rather than silently used. Create it
(`qrdb database create <name>`) or correct the `--database` value.

**`--id-strategy field_hash` was rejected.** It needs `--field-hash <field>`
naming which document field to hash. Without it there is nothing to derive the
ID from.

**"document already exists" on an insert.** The ID the insert resolved to is
already in use. If you supplied an `_id`, pick another one or use `update` to
replace the document you meant to replace. If you did not supply one, the
collection's own allocator produced an ID that is taken — report it, because on
an `increment` collection that should be impossible. Over SQL the same refusal
arrives as a duplicate-key error: MySQL 1062, PostgreSQL `SQLSTATE 23505`,
`gorm.ErrDuplicatedKey`.

**An `INSERT` into a collection table used to succeed and now fails with a
duplicate key.** It was previously discarding the `_id` you supplied and
storing the document under a generated one, which is why it never collided.
Whatever that statement inserted did not land where you asked. Re-check the
IDs; the document you thought you were writing to `42` is somewhere else.

**Documents inserted over SQL have lost an `_id` field.** They never had one to
lose — `_id` names the document rather than being one of its fields, on every
transport. If a client was reading `document._id`, read the document's `id`
instead. (Older documents may still carry the key; it is not written any more,
and the SQL table no longer emits a second `_id` column for it.)

**Your `increment` collection skipped a range of IDs.** Something was inserted
with an explicit `_id` above the counter, and the counter moved up to clear it.
The skipped IDs are not coming back. See *Supplying your own document ID*.

**Your document ID looks wrong in JavaScript.** IDs are `uint64` and are sent as
JSON *strings* precisely so they survive. If a client is turning them into
numbers, it is parsing them itself — leave them as strings.

**An update wiped fields you did not send.** `update` replaces the document.
Read it, modify it, send the whole thing back.

**You loaded a CSV with `batch import` and the documents are not in the
collection.** `batch import` targets the raw key-value keyspace, not
collections. There is no collection-targeted import. Insert documents one at a
time, or load into KV and treat them as KV.

**`doc list` returned fewer documents than `info` reports.** You read one page.
Follow `next_page_token` until it comes back empty.

**A document with the wrong shape was accepted.** Collections do not validate
against a schema — the `schema` field in `collection info` is descriptive only.
If you need enforced types and constraints, use the relational layer
([../sql/user.md](../sql/user.md)) instead.

**You need to filter documents by a field.** There is no document query
language. Put a secondary index on the field
([../secondary-indexes/user.md](../secondary-indexes/user.md)) or use SQL.
