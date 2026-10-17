# Vector Database — CLI

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.
>
> "Target" notes describe the vector half of the multi-model database program
> ([architecture.md](architecture.md)); they are approved and not
> implemented. OD-n refers to the ruled decisions in
> [../databases/architecture.md](../databases/architecture.md).

Command group: `qrdb vector`, with a `namespace` subgroup. **Present only in
builds carrying the `qrdb_vector` tag** (`make build WITH_QRDB_VECTOR=1`);
confirm with `qrdb admin capabilities`.

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags). Nearly every verb takes
`--namespace` and an optional `--shard-id`.

**`--shard-id` selects nothing today**, and is removed from every verb in the
target design (OD-14 A). The root `--database` flag decides which database —
and therefore which storage — a namespace belongs to:

```bash
qrdb --database docs-db vector namespace create --namespace docs --dimension 768
```

## Namespaces

### `qrdb vector namespace create`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--namespace` | string | — | namespace name |
| `--dimension` | uint32 | — | vector dimensionality |
| `--distance-metric` | string | `cosine` | `cosine \| euclidean \| dot-product` |
| `--index-type` | string | `hnsw` | `hnsw \| flat \| ivf-flat` |
| `--shard-id` | uint | — | target shard |

```bash
qrdb vector namespace create --namespace docs --dimension 768
qrdb vector namespace create --namespace faces --dimension 512 \
     --distance-metric euclidean --index-type flat
```

Geometry is fixed at creation. Changing it means a new namespace.

### `qrdb vector namespace info` / `list` / `delete`

| Flag | Applies to |
|---|---|
| `--namespace` | `info`, `delete` |
| `--shard-id` | all three |

```bash
qrdb vector namespace list --output json
qrdb vector namespace info --namespace docs
qrdb vector namespace delete --namespace scratch
```

## Vector CRUD

### `qrdb vector upsert`

| Flag | Type | Meaning |
|---|---|---|
| `--namespace` | string | namespace name |
| `--id` | string | vector ID (uint64, as a string) |
| `--vector` | string | comma-separated float values |
| `--metadata` | string | JSON metadata object |
| `--shard-id` | uint | target shard |

```bash
qrdb vector upsert --namespace docs --id 1 \
     --vector 0.12,0.98,-0.44 --metadata '{"source":"manual.pdf","page":3}'
```

The vector's length must equal the namespace dimension.

### `qrdb vector get`

Flags: `--namespace`, `--id`, `--shard-id`, `--consistency` (`default`,
`leader`, `any`).

```bash
qrdb vector get --namespace docs --id 1 --consistency leader
```

### `qrdb vector delete`

Flags: `--namespace`, `--id`, `--shard-id`.

### `qrdb vector list`

Flags: `--namespace`, `--shard-id`, `--limit`, `--cursor` (pagination cursor).

```bash
qrdb vector list --namespace docs --limit 100
qrdb vector list --namespace docs --limit 100 --cursor "$NEXT"
```

### `qrdb vector update-metadata`

Flags: `--namespace`, `--id`, `--metadata` (JSON), `--shard-id`.

### `qrdb vector batch-upsert`

Flags: `--namespace`, `--file` (JSON path, `-` for stdin), `--shard-id`.

```bash
qrdb vector batch-upsert --namespace docs --file vectors.json
```

### `qrdb vector batch-delete`

Flags: `--namespace`, `--ids` (comma-separated uint64), `--shard-id`.

```bash
qrdb vector batch-delete --namespace docs --ids 1,2,3
```

## Search

### `qrdb vector search` — k-NN from a query vector

| Flag | Type | Meaning |
|---|---|---|
| `--namespace` | string | namespace name |
| `--vector` | string | comma-separated float query vector |
| `--k` | int | number of nearest neighbours |
| `--min-score` | float | minimum similarity threshold |
| `--ef-search` | int | HNSW `ef` parameter (HNSW indexes only) |
| `--consistency` | string | `default \| leader \| any` |
| `--shard-id` | uint | target shard |

```bash
qrdb vector search --namespace docs --vector 0.1,0.2,0.3 --k 10
qrdb vector search --namespace docs --vector 0.1,0.2,0.3 --k 10 \
     --min-score 0.75 --ef-search 200
```

### `qrdb vector search-by-id` — k-NN from an existing vector

Flags: `--namespace`, `--id`, `--k`, `--consistency`, `--shard-id`.

```bash
qrdb vector search-by-id --namespace docs --id 1 --k 5
```

Use this instead of fetching a vector and passing it back to `search`.

### `qrdb vector search-metadata` — metadata filters only

Flags: `--namespace`, `--filters` (JSON filter object), `--limit`, `--offset`,
`--consistency`, `--shard-id`.

```bash
qrdb vector search-metadata --namespace docs \
     --filters '{"source":"manual.pdf"}' --limit 50 --offset 0
```

### `qrdb vector search-hybrid` — similarity plus metadata

Flags: `--namespace`, `--vector`, `--k`, `--filters`, `--min-score`,
`--ef-search`, `--consistency`, `--shard-id`.

```bash
qrdb vector search-hybrid --namespace docs --vector 0.1,0.2,0.3 --k 10 \
     --filters '{"source":"manual.pdf"}' --min-score 0.7
```

## Index maintenance

Both operations are **asynchronous**: they return a handle and you poll it.

### `qrdb vector rebuild-index` / `rebuild-progress`

```bash
JOB=$(qrdb vector rebuild-index --namespace docs --output json | jq -r .job_id)
qrdb vector rebuild-progress --namespace docs --job-id "$JOB"
```

`rebuild-index` flags: `--namespace`, `--shard-id`.
`rebuild-progress` flags: `--namespace`, `--job-id`.

### `qrdb vector compact` / `compact-progress`

```bash
OP=$(qrdb vector compact --namespace docs --output json | jq -r .operation_id)
qrdb vector compact-progress --namespace docs --operation-id "$OP"
```

`compact` flags: `--namespace`, `--shard-id`.
`compact-progress` flags: `--namespace`, `--operation-id`, `--shard-id`.

### `qrdb vector index-stats`

Flags: `--namespace`, `--shard-id`.

```bash
qrdb vector index-stats --namespace docs --output json
```

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
