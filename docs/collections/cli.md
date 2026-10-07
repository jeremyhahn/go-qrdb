# Document Collections — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

Command group: `qrdb collection`, with a `doc` subgroup for the documents
inside. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

Every verb in this group takes its own `--database` flag (default `system`),
distinct from the root `--database` persistent flag.

## `qrdb collection create <name>`

Create a new collection within a database.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--database` | string | `system` | target database |
| `--id-strategy` | string | `unix_nano` | `unix_nano \| increment \| field_hash` |
| `--field-hash` | string | — | document field to hash; **required** when `--id-strategy=field_hash` |
| `--storage-codec` | string | server default | document storage codec override |
| `--index` | string, repeatable | — | secondary index as `FIELD[,unique][,sparse]` |

```bash
qrdb collection create users --database app
qrdb collection create events --database app --id-strategy increment
qrdb collection create products --database app \
     --id-strategy field_hash --field-hash sku
qrdb collection create accounts --database app \
     --index email,unique --index region
```

The ID strategy is fixed for the collection's life.

`--index` declares a secondary index the collection creates, owns and drops
with itself. An unknown option or an empty field name is refused rather than
ignored. The collection reports `creating` until every declared index exists and
serves documents only once it is `active`; if an index cannot be built the
command fails and nothing is left behind, so running it again is safe. A field
whose derived index name (`{database}.{collection}.{field}`) is already held by
another index — one made with `qrdb index create`, say — is refused with a
conflict instead of being shared.

## `qrdb collection info <name>`

Show collection details: lifecycle state, ID strategy, storage codec, document
count, creation time, and declared indexes. A collection that is not `active`
shows when it entered its state, e.g. `State: dropping (since
2026-09-25T12:00:00Z)`.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection info users --database app --output json
```

## `qrdb collection list`

List all collections in a database, with a `STATE` column. A collection
that is `creating` or `dropping` is listed, not hidden.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection list --database app
```

## `qrdb collection drop <name>`

Drop a collection and delete all of its documents, together with any secondary
index the collection declared. **Irreversible.**

The collection is marked `dropping` first — from then on it accepts no document
reads or writes — and its metadata is removed last. An interrupted drop leaves
the collection listed as `dropping`; the server finishes it on its own within a
few minutes, and re-running the command finishes it immediately. If the index
teardown fails the command reports the failure instead of succeeding.

Also discards the collection's `increment` counter, so a collection later
created with the same name starts at ID 1. The command is safe to re-run: if it
fails partway, re-issuing it completes the drop.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection drop scratch --database app
```

## `qrdb collection doc insert <collection>`

Insert a document. Supply the JSON inline or from a file.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--database` | string | `system` | target database |
| `--data` | string | — | document JSON, inline |
| `--file` | string | — | document JSON file path, `-` for stdin |

```bash
qrdb collection doc insert users --database app \
     --data '{"email":"alice@example.com","status":"active"}'

qrdb collection doc insert users --database app --file user.json
cat user.json | qrdb collection doc insert users --database app --file -
```

The response carries the allocated document `id` as a JSON string.

A document that carries its own `_id` is stored under that ID rather than a
generated one, and that ID is reserved against every ID the collection
generates afterwards:

```bash
qrdb collection doc insert events --database app --data '{"_id": 42, "kind": "deploy"}'
```

An `_id` that already names a stored document is **refused** — the command
exits non-zero with "document already exists" rather than replacing it. Use
`qrdb collection doc update` to replace a document. See
[user.md](user.md#supplying-your-own-document-id).

`_id` names the document; it is not stored as one of its fields, so
`qrdb collection doc get events 42` prints `{"kind": "deploy"}` and not
`{"_id": 42, "kind": "deploy"}`. The same insert is available over SQL — see
[user.md](user.md#doing-the-same-thing-over-sql) and
[`qrdb sql`](../sql/cli.md).

## `qrdb collection doc get <collection> <id>`

Retrieve a document by ID.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection doc get users 1755561600000000000 --database app
```

## `qrdb collection doc update <collection> <id>`

**Replace** an existing document. This is not a merge — the document you supply
becomes the document.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |
| `--data` | string | — |
| `--file` | string | — |

```bash
qrdb collection doc update users 1755561600000000000 --database app \
     --data '{"email":"alice@example.com","status":"disabled"}'
```

## `qrdb collection doc delete <collection> <id>`

Delete a document by ID.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection doc delete users 1755561600000000000 --database app
```

## `qrdb collection doc list <collection>`

List documents in a collection. **Paginated.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--database` | string | `system` | target database |
| `--page-size` | int32 | server default | results per page |
| `--page-token` | string | — | pagination token from a previous response |

```bash
qrdb collection doc list users --database app --page-size 100 --output json
qrdb collection doc list users --database app --page-token "$NEXT"
```

Follow `next_page_token` until it is empty; one page is not the collection.

## No bulk document import

`qrdb batch import` loads into the **raw KV keyspace only**. There is no
collection-targeted import; documents go in one at a time through
`collection doc insert`. See [../transactions/cli.md](../transactions/cli.md).

## Parity

Every verb here is driven on every network transport by `collection_cli_test.go`
(`make integration-test-collections`). Per-transport accounting: the
`collections` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
