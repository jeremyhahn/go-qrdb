# Embedding Service — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Embed | `POST /v1/embeddings` | `Embed` | `embedding.Embed` | `qrdb_embed_text` | `EmbeddingClient.Embed` | `embedding:embed` |
| Batch embed | `POST /v1/embeddings/batch` | `BatchEmbed` | `embedding.BatchEmbed` | `qrdb_batch_embed` | `EmbeddingClient.BatchEmbed` | `embedding:embed` |
| List models | `GET /v1/embedding/models` | `ListEmbeddingModels` | `embedding.ListModels` | `qrdb_list_embedding_models` | `EmbeddingClient.ListEmbeddingModels` | `embedding:list_models` |

All three are present on all six transports. Routes are registered only when
the embedding service is wired (at least one provider configured); otherwise the
capability is absent rather than failing per request.

Embed and batch-embed share `embedding:embed` — batching is a client-side
strategy, not a distinct privilege. Listing models is separate, so enumerating a
provider's catalogue is not implied by being allowed to embed.

## Schemas

`POST /v1/embeddings` — `EmbedRequest`:

```json
{ "texts": ["the quick brown fox", "jumps over"],
  "model": "text-embedding-3-large", "provider": "openai" }
```

`model` and `provider` are optional; empty means the configured defaults.

Response — `EmbedResponse`:

```json
{ "embeddings": [[0.01, -0.22, ...], [0.05, 0.13, ...]],
  "model": "text-embedding-3-large",
  "cached": false,
  "usage": { "prompt_tokens": 12, "total_tokens": 12 } }
```

**`embeddings` is positional**: `embeddings[i]` corresponds to `texts[i]`.
`model` names the model actually used. `cached` distinguishes a cache hit from a
provider call.

`POST /v1/embeddings/batch` — `BatchEmbedRequest` adds `batch_size` (maximum
texts per provider call; default 256 when unset). The response has no `cached`
field. Sub-batch results are aggregated **in input order**.

`GET /v1/embedding/models` — optional `provider` query parameter. Response:

```json
{ "models": [ { "name": "text-embedding-3-large", "dimensions": 3072, "max_tokens": 8191 } ] }
```

`dimensions` and `max_tokens` are present only where the provider reports them.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Unknown provider | 404 | `NOT_FOUND` — `*EmbeddingProviderNotFoundError`, never substituted |
| Provider unreachable after `max_retries` | 503 | `UNAVAILABLE` — no partial embeddings |
| Malformed request (empty `texts`) | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| No embedding service configured | 404 (route unregistered) | `UNIMPLEMENTED` |

There is no failover between providers. A request that names a provider gets
that provider's model or an error — silently returning a vector from a different
model would be incomparable with everything already stored.

## Pagination

Not applicable. `ListEmbeddingModels` returns the provider's full catalogue;
embed calls are bounded by the `texts` the caller sent.

## Versioning

Embedding REST paths are under `/v1/`. Nothing here is deprecated.
