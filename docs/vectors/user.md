# Vector Database — User Guide

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-06
>
> "Target" notes describe the vector half of the multi-model database program
> ([architecture.md](architecture.md)); they are approved and not
> implemented. OD-n refers to the ruled decisions in
> [../databases/architecture.md](../databases/architecture.md).

Test reference: `make integration-test-vectors`; coverage row: `test/integration/COVERAGE.md` `[vectors](../../docs/vectors/)`.

Store embeddings and find the ones closest to a query. This feature stores and
searches vectors; producing them from text is
[../embeddings/user.md](../embeddings/user.md), and the whole
retrieval-and-answer pipeline is [../rag/user.md](../rag/user.md).

## Prerequisites

- A daemon built with vector support: `make build WITH_QRDB_VECTOR=1`.
  Check with `qrdb admin capabilities` — `vector` must be listed.
- A reachable server, client certificates, an unsealed barrier, and the
  `vector:*` permissions for what you do.

## First run

Create a namespace, decide its shape once, then put vectors in it.

```bash
qrdb vector namespace create --namespace docs --dimension 768

qrdb vector upsert --namespace docs --id 1 \
     --vector 0.12,0.98,-0.44 --metadata '{"source":"manual.pdf","page":3}'

qrdb vector search --namespace docs --vector 0.11,0.97,-0.40 --k 5
```

## Choosing the namespace shape

You pick these at creation and cannot change them afterwards.

| Choice | Options | Rule of thumb |
|---|---|---|
| `--dimension` | your model's output size | must match your embedding model exactly |
| `--distance-metric` | `cosine` (default), `euclidean`, `dot-product` | `cosine` for normalized text embeddings |
| `--index-type` | `hnsw` (default), `flat`, `ivf-flat` | `hnsw` for speed at scale; `flat` for exact results on small sets |

`flat` is exhaustive, so it is exact and slow. `hnsw` is approximate and fast.
If your namespace holds a few thousand vectors, `flat` may well be the better
answer.

## Common tasks

**Load vectors in bulk.**

```bash
qrdb vector batch-upsert --namespace docs --file vectors.json
```

**Find neighbours of something already stored** — no need to fetch the vector
and send it back.

```bash
qrdb vector search-by-id --namespace docs --id 1 --k 5
```

**Filter by metadata as well as similarity.**

```bash
qrdb vector search-hybrid --namespace docs --vector 0.1,0.2,0.3 --k 10 \
     --filters '{"source":"manual.pdf"}'
```

**Query metadata alone, no vector maths.**

```bash
qrdb vector search-metadata --namespace docs \
     --filters '{"source":"manual.pdf"}' --limit 50
```

**Trade recall against latency on HNSW.** Higher `--ef-search` searches more of
the graph: better recall, slower query.

```bash
qrdb vector search --namespace docs --vector 0.1,0.2,0.3 --k 10 --ef-search 400
```

**Drop weak matches.**

```bash
qrdb vector search --namespace docs --vector 0.1,0.2,0.3 --k 10 --min-score 0.8
```

`--min-score` filters results after the search; it does not make it faster.

**Page through a namespace.** Follow the cursor.

```bash
qrdb vector list --namespace docs --limit 100
qrdb vector list --namespace docs --limit 100 --cursor "$NEXT"
```

**Update metadata without re-sending the vector.**

```bash
qrdb vector update-metadata --namespace docs --id 1 --metadata '{"page":4}'
```

**Reclaim space after lots of deletions, or rebuild a degraded index.** Both are
asynchronous — start them, then poll.

```bash
OP=$(qrdb vector compact --namespace docs --output json | jq -r .operation_id)
qrdb vector compact-progress --namespace docs --operation-id "$OP"

JOB=$(qrdb vector rebuild-index --namespace docs --output json | jq -r .job_id)
qrdb vector rebuild-progress --namespace docs --job-id "$JOB"
```

**Check index health.**

```bash
qrdb vector index-stats --namespace docs --output json
```

## Where your vectors live

Today, every database's vectors on a server go into one store on that server,
encrypted with the server's key rather than your tenant's, and on a cluster
they are not copied to other servers. Vectors are not available at all in
databases of volume- or raft-isolated tenants.

**Target:** vectors live in the database you name with `--database`, in the
storage that database chose, under your tenant's key. On a `raft` database they
are replicated with everything else in it; dropping the database drops them.

## Troubleshooting

**Similarity search returns nothing after the server restarted.** This is
fixed as of 2026-10-06: the search index is rebuilt from the stored vectors,
under the namespace's own metric, when the server starts. If you still see
empty results after a restart on a server built before that fix, run
`qrdb vector rebuild-index` for the namespace and poll `rebuild-progress` as
a workaround. The target design additionally answers "index warming, retry"
while a large namespace's rebuild is still in progress, instead of
returning an empty result.

**"dimension mismatch" on upsert.** Your vector has a different length from the
namespace. That is refused rather than padded, because a padded vector produces
neighbours that look plausible and are wrong. Check that you are using the same
embedding model the namespace was sized for.

**You need a different dimension, metric or index type.** Create a new namespace
and re-upsert. Geometry is fixed at creation; there is no in-place migration.

**Search results look poor.** In order of likelihood: the metric does not match
your embeddings (normalized text embeddings want `cosine`); `--ef-search` is too
low for an HNSW index; or the index needs a rebuild after heavy churn. Try a
`flat` namespace on a sample to see what exact results look like.

**A search returns fewer than `k` results.** Either the namespace has fewer
vectors than `k`, or `--min-score` filtered them out.

**Searches got slower after mass deletion.** Deleted vectors still occupy index
space until compaction. Run `vector compact` and poll the operation.

**You started a rebuild and cannot tell if it finished.** Keep the job ID —
`rebuild-progress` is the only way to observe it. Without the handle you cannot
query it.

**`qrdb vector` is not a command.** The binary was built without
`WITH_QRDB_VECTOR=1`. Confirm with `qrdb admin capabilities`.

**Everything returns "unavailable".** Check whether the barrier is sealed
(`qrdb barrier status`) — vector data is barrier-encrypted and fails closed.

**You want to search two namespaces at once.** Not supported; a search targets
exactly one namespace. Run two searches and merge client side.
