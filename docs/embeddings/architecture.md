# Embedding Service — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

## Problem

Vector search needs vectors, and producing them means calling a model. Every
consumer doing that itself means every consumer holds provider credentials,
implements its own retry and caching, and hardcodes one vendor's API.

## Shape chosen

A **provider-proxy**: qrdb does not run models. It holds a registry of named,
OpenAI-compatible HTTP providers and proxies embedding requests to whichever the
caller names — with an optional LRU cache in front, batching behind, and the store's own
mTLS/RBAC in front of the whole thing.

```
caller  →  EmbeddingServicer  →  LRU cache  →  named provider (OpenAI-compatible HTTP)
```

Any endpoint speaking the OpenAI embeddings API works: Ollama for local models,
OpenAI, vLLM, or anything else with that shape.

### Alternatives rejected

- **Running models in process.** Rejected: it would pull a model runtime, GPU
  management and model distribution into a database. Inference is a different
  operational problem with a different lifecycle.
- **One hardcoded provider.** Rejected: the useful deployments genuinely differ —
  local Ollama for development and air-gapped installs, a hosted API for
  production, vLLM for self-hosted scale. The registry is what lets one config
  file cover all three.
- **A qrdb-specific provider protocol.** Rejected: OpenAI-compatible is the de
  facto interface every serving stack already implements.
- **Caching by text alone.** Rejected: the cache key is the `(model, text)`
  pair, because the same text under a different model is a different vector and
  returning one for the other is silently wrong.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `EmbeddingServicer`, provider registry, cache, batching | `pkg/service/embedding_service.go`, `pkg/embedding` | go-qrdb |
| Provider clients (OpenAI-compatible HTTP) | `pkg/embedding` | go-qrdb |
| Vector storage for the results | `pkg/vector` — see [../vectors/](../vectors/) | go-qrdb |
| Chunking and retrieval built on top | `pkg/rag` — see [../rag/](../rag/) | go-qrdb |
| SDK surface | `sdk/go/transport` (`EmbeddingClient`) | go-qrdb |
| The models themselves | — | **external**: Ollama, OpenAI, vLLM, … |

## Wiring

The daemon reads the `embedding` section of the YAML config at startup,
registers each configured provider, and wires the service into every protocol
server through `server.WithEmbedding()`. REST, gRPC, QUIC, MCP and Unix all
reach it through the same service interface, so the feature is available or
absent uniformly — there is no transport where embedding works and another
where it does not.

Providers can also be registered and removed at runtime
(`RegisterProvider` / `DeregisterProvider`).

## Trust boundaries

- **Provider API keys are configuration, and they leave the process.** An
  embedding call sends caller text to an external endpoint. That is a data-egress
  decision, not merely a performance one: a deployment handling regulated data
  should point `base_url` at something inside its own boundary.
- The two capabilities carry separate permissions (`embedding:embed`,
  `embedding:list_models`), so enumerating a provider's models is not implied by
  being allowed to embed.
- The cache is in-memory, per process, and holds embeddings — derived data, but
  derived from caller text. It has no TTL and is cleared on shutdown.

## What this feature deliberately does not do

- **No model hosting, no fine-tuning, no inference beyond embeddings.**
- **No text generation.** This produces vectors, not completions.
- **No automatic provider failover.** A request names a provider (or takes the
  default); a failing provider fails the request rather than silently producing
  a vector from a different model, which would be incomparable with everything
  already stored.
- **No persistence of embeddings.** Storing them is
  [../vectors/](../vectors/)'s job.
