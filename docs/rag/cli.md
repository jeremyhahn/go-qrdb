# RAG — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb rag` — ingest documents, run semantic queries, and manage
the document store. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

Requires an embedding provider configured on the daemon. Ingest creates the
vector namespace on first use; a query on a namespace that does not exist is a
not-found.

## `qrdb rag ingest`

Chunk, embed and store a document.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | unique document identifier; required |
| `--text` | string | — | document content; required |
| `--namespace` | string | — | vector namespace to store embeddings in; required |
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
document: its chunks are overwritten by index, and any chunk past the new chunk
count is removed, so nothing of the old version stays searchable.

## `qrdb rag query <query>`

Retrieve the most semantically similar passages.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--namespace` | string | — | vector namespace to search; required |
| `--top-k` | int | 0 (server default, 5) | maximum results |
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
| `--id` | string | document identifier to remove; required |
| `--namespace` | string | vector namespace the document is stored in; required |

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

`TestRAG_CLI_PerProtocol` (`test/integration/rag_cli_test.go`) runs every verb
here over REST, gRPC, QUIC, MCP and the Unix socket against a node with no
embedding provider: flag validation and the not-configured refusal. The
configured path runs in the Docker `test/integration/api` suite
(`TestRAGIngestQueryE2E`, `TestRAGListDeleteDocumentE2E`).
