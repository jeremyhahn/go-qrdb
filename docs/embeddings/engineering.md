# Embedding Service — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Exported surface

`sdk/go/transport`:

```go
type EmbeddingClient interface {
	Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)
	BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)
	ListEmbeddingModels(ctx context.Context, provider string) (*ListEmbeddingModelsResponse, error)
}

type EmbedRequest struct {
	Texts    []string `json:"texts"`
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
}
type EmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
	Cached     bool        `json:"cached"`
	Usage      *UsageInfo  `json:"usage,omitempty"`
}

type BatchEmbedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	BatchSize int      `json:"batch_size,omitempty"`
}
type BatchEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
	Usage      *UsageInfo  `json:"usage,omitempty"`
}

type UsageInfo struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ListEmbeddingModelsResponse struct{ Models []EmbeddingModelInfo }
type EmbeddingModelInfo struct {
	Name       string `json:"name"`
	Dimensions int    `json:"dimensions,omitempty"`
	MaxTokens  int    `json:"max_tokens,omitempty"`
}
```

Server side, `pkg/service/embedding_service.go`:

```go
type EmbeddingServicer interface {
	Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)
	BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)
	ListModels(ctx context.Context, req *ListEmbeddingModelsRequest) (*ListEmbeddingModelsResponse, error)
	RegisterProvider(name string, provider embedding.Provider) error
	DeregisterProvider(name string) error
	Close() error
}
```

Wired into every protocol server with `server.WithEmbedding()`.

## Invariants

- **`Embeddings` is positional**: `Embeddings[i]` is the vector for `Texts[i]`.
  `BatchEmbed` aggregates its sub-batches **in input order**, so the guarantee
  holds across the split.
- **Empty `Provider` means the configured `default_provider`; empty `Model`
  means that provider's configured model.** The response always names the model
  actually used, so a caller can record it alongside the vector.
- **Cache keys are the `(provider, model, text)` tuple.** The same text embedded
  under two models yields two entries; one is never served for the other.
- **`Cached` is reported on the response** so a caller can tell a cache hit from
  a provider call.
- **The cache has no TTL** and is cleared on service shutdown. It retains
  embeddings for the service lifetime.
- **`BatchEmbed`'s default batch size is 256** when `BatchSize` is unset — safe
  for most providers.
- **A provider is named, not chosen.** There is no failover: a failing provider
  fails the request. Silently substituting another model would return vectors
  incomparable with everything already stored.
- **`RegisterProvider` refuses a name that is already registered** rather than
  replacing it.
- **A provider name is not an allowlist.** `embedding.NewManager` accepts any
  `Provider` name — one of the built-ins or one an embedder registers itself
  through `RegisterProvider` — so an out-of-tree provider can be the configured
  default. A name with no provider registered under it is reported at first
  use as `*ProviderNotFoundError` naming it, which is also what a typo in a
  built-in's name gets. `ErrProviderInvalid` is no longer returned and is kept
  only so existing matches compile.

## Configuration

Section `embedding`:

| Field | Type | Default | Description |
|---|---|---|---|
| `default_provider` | string | `""` | provider used when a request names none |
| `cache_capacity` | int | `0` | max embeddings in the LRU cache; **0 disables caching** |
| `providers` | list | `[]` | configured provider endpoints |

Each entry of `providers`:

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | unique provider identifier |
| `base_url` | string | required | OpenAI-compatible base URL |
| `api_key` | string | `""` | bearer token; empty for unauthenticated endpoints |
| `model` | string | required | default embedding model name |
| `timeout` | string | `"30s"` | per-request timeout, Go duration format |
| `max_retries` | int | `3` | retries for transient failures (5xx, network) |

```yaml
embedding:
  default_provider: "ollama"
  cache_capacity: 10000
  providers:
    - name: "ollama"
      base_url: "http://localhost:11434/v1"
      model: "mxbai-embed-large"
      timeout: "30s"
      max_retries: 3
    - name: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "${OPENAI_API_KEY}"
      model: "text-embedding-3-large"
      timeout: "60s"
      max_retries: 2
    - name: "vllm-local"
      base_url: "https://vllm.example.com/v1"
      model: "Alibaba-NLP/gte-large-en-v1.5"
      timeout: "30s"
```

`cache_capacity: 10000` is the recommended production setting; it reduces both
token usage and latency.

**`api_key` is secret material.** Supply it through the secret-reference
resolver rather than as a literal where the deployment allows — see
[../credentials/](../credentials/).

## Error semantics

Source: `pkg/service/embedding_errors.go`.

| Error type | Condition | Fails closed? |
|---|---|---|
| `*EmbeddingProviderNotFoundError` | the requested provider is not registered | **yes** — no substitution |
| `*EmbeddingProviderRegistrationError` | the provider name is already registered | yes |
| `*EmbeddingServiceError` | generic embedding generation failure | yes |

| Condition | Behavior |
|---|---|
| Provider unreachable after `max_retries` | request fails; no partial embeddings returned |
| Missing `embedding:embed` / `embedding:list_models` | permission denied |
| No providers configured | the capability is unavailable rather than silently degraded |

## Persistence

**None.** The service is stateless apart from the in-memory LRU cache. Vectors
it produces are persisted only if a caller stores them — see
[../vectors/](../vectors/).

## Test strategy

- **Unit**: provider registry (register, deregister, duplicate-name refusal),
  cache key tupling, batch splitting and input-order aggregation, default
  provider and model resolution, `Cached` reporting.
- **Provider clients** are exercised against a stub OpenAI-compatible HTTP
  server, not a live model — the tests must not need a GPU or a network.
- **Error tests** per function, happy path plus error path.
- **Integration**: embedding parity across transports in the standalone stage.

## Observability

- `UsageInfo` (`prompt_tokens`, `total_tokens`) is returned per call and is the
  input to any cost accounting.
- `Cached` distinguishes a cache hit from a provider call — the primary signal
  for whether `cache_capacity` is doing anything.
- Embedding calls are audited through [../audit/](../audit/).
