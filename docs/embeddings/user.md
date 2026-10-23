# Embedding Service — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

Turn text into vectors. The server does not run models — it proxies to an
OpenAI-compatible endpoint you configure, caches the results, and applies the
same auth as everything else.

## Prerequisites

- At least one provider configured in the daemon's `embedding` section. With
  none configured, the capability simply is not there.
- A reachable server, client certificates, and `embedding:embed` (plus
  `embedding:list_models` to list models).

A minimal local setup with Ollama:

```bash
ollama pull mxbai-embed-large
```

```yaml
embedding:
  default_provider: "ollama"
  cache_capacity: 10000
  providers:
    - name: "ollama"
      base_url: "http://localhost:11434/v1"
      model: "mxbai-embed-large"
```

## First run

```bash
qrdb embedding models
qrdb embedding embed "the quick brown fox" --output json
```

The response carries the vector, the model that produced it, and whether it came
from the cache.

## Common tasks

**Embed several texts at once.** Results come back in the order you sent them.

```bash
qrdb embedding embed "first text" "second text" "third text" --output json
```

**Embed a large list.** `batch-embed` splits into sub-batches and reassembles in
order.

```bash
xargs -a texts.txt qrdb embedding batch-embed --batch-size 256 --output json
```

**Pick a different provider or model for one call.**

```bash
qrdb embedding embed "text" --provider openai --model text-embedding-3-large
```

**Find out what dimension to size a vector namespace at.**

```bash
qrdb embedding models --provider openai --output json
```

Then create the namespace to match:

```bash
qrdb vector namespace create --namespace docs --dimension 3072
```

## Choosing a model

| Model | Dims | Speed | Quality | Cost | Use case |
|---|---|---|---|---|---|
| `mxbai-embed-large` | 1024 | fast | excellent | free (local) | general purpose, local |
| `text-embedding-3-small` | 1536 | fast | good | low | high volume, cost-sensitive |
| `text-embedding-3-large` | 3072 | slower | excellent | medium | production, quality-critical |
| `nomic-embed-text` | 768 | fast | good | free (local) | search, local deployment |

For RAG pipelines, the higher-quality models improve retrieval accuracy
measurably. Smaller models are fine for keyword-style filtering or when compute
is constrained.

## Troubleshooting

**"embedding provider not found".** The name does not match any entry in the `embedding`
config, or the provider was deregistered at runtime. Nothing is substituted —
returning a vector from a different model would be incomparable with everything
you have already stored.

**Vectors from today do not match vectors from last week.** You changed the
model. Embeddings from different models live in different spaces and cannot be
compared. Record the `model` field the response returns alongside every vector
you store, and re-embed the whole corpus when you change models.

**A vector was rejected by the vector namespace.** Dimension mismatch — the
namespace was sized for a different model. Check `qrdb embedding models` and
create a namespace at the right dimension.

**Everything is slow and your token bill is large.** Set `cache_capacity` to a
non-zero value; `10000` is the recommended production figure. Then check the
`cached` field on responses to confirm hits are happening. Note the cache keys
on `(model, text)`, so identical text under a new model is a miss.

**A batch failed halfway.** No partial results are returned; the whole request
fails. Retry, or lower `--batch-size` if the provider is rejecting large calls.

**Requests time out against a slow local model.** Raise the provider's `timeout`
in the config; the default is 30 seconds.

**You need to keep text inside your boundary.** Every embed call sends the text
to the configured `base_url`. Point it at something you operate — Ollama or vLLM
locally — rather than a hosted API.

**Every `embedding` command fails with 404, "unknown method" or "EmbeddingServicer
must not be nil".** No providers are configured on the daemon, so the
service was never wired in.

Test reference: `make integration-test-embeddings`; coverage row: `test/integration/COVERAGE.md` `[embeddings](../../docs/embeddings/)`.
