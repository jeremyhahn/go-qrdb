# Document Collections — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

```bash
qrdb collection create users --database app
qrdb collection create events --database app --id-strategy increment
qrdb collection create products --database app \
     --id-strategy field_hash --field-hash sku
```

The ID strategy is fixed for the collection's life.

## `qrdb collection info <name>`

Show collection details: ID strategy, storage codec, document count, creation
time.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection info users --database app --output json
```

## `qrdb collection list`

List all collections in a database.

| Flag | Type | Default |
|---|---|---|
| `--database` | string | `system` |

```bash
qrdb collection list --database app
```

## `qrdb collection drop <name>`

Drop a collection and delete all of its documents. **Irreversible.**

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

Every verb here has a `CommandDefinition` in the integration parity suite.
