# RAG — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Put documents in, get the most relevant passages back. What you do with those
passages — build a prompt, call a model, rerank — is up to you; this feature
stops at ranked results.

## Prerequisites

- An embedding provider configured on the daemon
  ([../embeddings/user.md](../embeddings/user.md)).
- A reachable server, client certificates, an unsealed barrier, and the `rag:*`
  permissions.

The first ingest into a namespace creates it, with the model's dimension and
the cosine metric. To use another metric, create the namespace first:

```bash
qrdb embedding models --output json         # what dimension does the model emit?
qrdb vector namespace create --namespace docs --dimension 1024 \
     --distance-metric euclidean
```

## First run

```bash
qrdb rag ingest --namespace docs --id manual-v2 --text "$(cat manual.txt)"
qrdb rag query "how do I rotate the barrier key?" --namespace docs --top-k 5
```

Each result gives you the chunk text, a similarity score, which document it came
from, and where in that document it sat.

## Choosing a chunking strategy

| Strategy | Good for | Watch out for |
|---|---|---|
| `fixed` (default) | prose, unstructured text | splits mid-sentence; overlap duplicates content |
| `sentence` | anything where sentence integrity matters | chunk sizes vary; a sentence over `--chunk-size` is split mid-sentence |
| `paragraph` | essays, articles, structured docs | a paragraph over `--chunk-size` is split into fixed windows |

```bash
qrdb rag ingest --namespace docs --id essay-1 --text "$(cat essay.txt)" \
     --chunk-strategy paragraph --chunk-min-size 200
```

## Tuning

**Chunk size.** 256–512 gives better precision at more overhead; 512–1024 is a
balanced default; 1024+ means fewer vectors and lower cost but may bury the
relevant passage. For `mxbai-embed-large`, 512 characters works well; larger
models often do better at 1024.

**Overlap.** None risks missing a passage that straddles a boundary. 50
characters is the recommended default. 100+ costs storage for diminishing
returns. Sentence and paragraph chunking repeat nothing between chunks; overlap
applies only where they split an oversized sentence or paragraph.

**TopK.** 5 is fast and fine for interactive queries; 10 gives good coverage;
20+ is for exploratory search or a large context window. Retrieving more and
reranking downstream is a reasonable strategy.

**Metadata filters.** Add metadata at ingest so you can narrow at query time.
Filtering shrinks the search space and improves relevance. There is no CLI flag
for it — use the SDK or REST:

```bash
curl --cert client.pem --key client-key.pem --cacert ca.pem \
     -X POST https://db.example.com/v1/rag/query \
     -d '{"query":"timeouts","namespace":"docs","top_k":5,
          "filter":{"source":"manual.pdf","language":"en"}}'
```

## Common tasks

**Replace a document.** Delete it, then ingest the same `--id` again. Ingesting
over it without the delete overwrites its chunks by index but keeps any extra
chunks the old version had, and they stay searchable.

```bash
qrdb rag delete --namespace docs --id manual-v2
qrdb rag ingest --namespace docs --id manual-v2 --text "$(cat manual-updated.txt)"
```

**See what is in the corpus.**

```bash
qrdb rag list --namespace docs --limit 50 --output json
```

**Remove a document and its vectors.**

```bash
qrdb rag delete --namespace docs --id manual-v1
```

The response says how many chunks went with it.

## Troubleshooting

**Query results are nonsense but there is no error.** Almost certainly the query
was embedded with a different model from the corpus. Vectors from different
models live in different spaces; the maths still produces numbers. Pass the same
`--provider` and `--model` you ingested with, and re-ingest the corpus if you
have changed models — there is no automatic re-embedding.

**"namespace not found" on a query.** Nothing has been ingested into that
namespace in this database yet. Check the `--namespace` and the database the
query targets match the ones you ingested into.

**Ingest failed partway and the document is half in.** Re-ingest the same
document ID; that is the recovery path.

**Retrieval misses passages you know are there.** Usually chunking: a fixed
split cut the passage in half, or the chunks are too large and the relevant
sentence is diluted. Try `--chunk-strategy sentence`, or a smaller
`--chunk-size` with a little more `--chunk-overlap`.

**Ingesting a huge document was refused.** `max_document_size` is set on the
daemon. Note that its default is `0`, meaning unlimited — if you are taking
untrusted input, it should not be left there.

**A PDF ingested as gibberish.** Ingest takes plain text; there is no format
parsing. Extract the text first.

**You want the model to answer, not just retrieve.** That is deliberately
outside this feature. Prompt assembly, reranking, fusion and generation belong
to the layer above; this API hands you ranked passages to build with.

**Queries are slow or expensive.** The query itself is one embedding call plus a
vector search. Turn on the embedding cache (`cache_capacity`) — repeated queries
then skip the provider entirely.

Test reference: `make integration-test-rag`; coverage row: `test/integration/COVERAGE.md` `[rag](../../docs/rag/)`.
