# RAG — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Command group: `qrdb rag` — ingest documents, run semantic queries, and manage
the document store. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

Requires an embedding provider configured on the daemon and an existing vector
namespace.

## `qrdb rag ingest`

Chunk, embed and store a document.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | unique document identifier |
| `--text` | string | — | document content |
| `--namespace` | string | — | vector namespace to store embeddings in |
| `--provider` | string | configured default | embedding provider |
| `--model` | string | provider default | embedding model |
| `--chunk-strategy` | string | `fixed` | `fixed \| sentence \| paragraph` |
| `--chunk-size` | int | 512 | target chunk size in characters |
| `--chunk-overlap` | int | 50 | overlapping characters between adjacent chunks |
| `--chunk-min-size` | int | 100 | minimum chunk size; smaller chunks are merged |

```bash
qrdb rag ingest --namespace docs --id manual-v2 --text "$(cat manual.txt)"

qrdb rag ingest --namespace docs --id essay-1 --text "$(cat essay.txt)" \
     --chunk-strategy paragraph --chunk-min-size 200
```

The response reports `chunk_count`. Re-ingesting the same `--id` replaces the
document.

## `qrdb rag query <query>`

Retrieve the most semantically similar passages.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--namespace` | string | — | vector namespace to search |
| `--top-k` | int | 0 (uses `default_top_k`, 10) | maximum results |
| `--provider` | string | configured default | embedding provider for the query |
| `--model` | string | provider default | embedding model for the query |

```bash
qrdb rag query "how do I rotate the barrier key?" --namespace docs --top-k 5
qrdb rag query "connection timeouts" --namespace docs --output json
```

**Use the same provider and model the corpus was ingested with.** Mismatched
models produce meaningless scores and no error.

Each result carries the chunk text, its similarity score, the source document
ID, the chunk index within that document, and any metadata.

## `qrdb rag list`

List ingested documents in a namespace.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--namespace` | string | — | vector namespace to list |
| `--limit` | int | 0 (no limit) | maximum documents |
| `--offset` | int | 0 | zero-based pagination offset |

```bash
qrdb rag list --namespace docs --limit 50 --offset 0 --output json
```

## `qrdb rag delete`

Remove a document and all of its chunk vectors.

| Flag | Type | Meaning |
|---|---|---|
| `--id` | string | document identifier to remove |
| `--namespace` | string | vector namespace the document is stored in |

```bash
qrdb rag delete --namespace docs --id manual-v1
```

The response reports `chunks_removed`.

## Notes

- Ingest takes **text**. There is no document format parsing — extract text from
  PDF or HTML before calling.
- The CLI has no flag for query metadata filters; the `filter` field exists on
  the API. Use the SDK or REST for filtered retrieval.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
