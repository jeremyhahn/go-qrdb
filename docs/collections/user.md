# Document Collections — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
than creating a duplicate — that is the point of it.

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

**Delete a collection.** This removes every document in it, along with any
secondary index the collection declared, and cannot be undone. If the command
fails partway through, the collection is still listed — run it again and it
finishes.

```bash
qrdb collection drop scratch --database app
```

## Troubleshooting

**"collection not found" on an insert.** Collections are never created
implicitly. Check the spelling and the `--database` — a typo would otherwise
have quietly produced a second collection that then diverges from the first.

**`--id-strategy field_hash` was rejected.** It needs `--field-hash <field>`
naming which document field to hash. Without it there is nothing to derive the
ID from.

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
