<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Embedding Service

QRDB includes an embedding service for generating vector embeddings from text. The service is OpenAI-compatible, supporting multiple providers: Ollama, OpenAI, vLLM, Azure OpenAI, and any other OpenAI API-compatible endpoint. The feature is gated behind the `qrdb_vector` build tag.

## Service Interface

Source: `pkg/service/embedding_service.go`

The `EmbeddingServicer` interface provides four methods:

```go
type EmbeddingServicer interface {
    // Embed generates embeddings for a list of texts
    Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)

    // BatchEmbed splits texts into sub-batches and aggregates results
    BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)

    // ListModels returns available embedding models from a provider
    ListModels(ctx context.Context, req *ListEmbeddingModelsRequest) (*ListEmbeddingModelsResponse, error)

    // RegisterProvider adds a provider dynamically at runtime
    RegisterProvider(name string, provider embedding.Provider) error

    // DeregisterProvider removes a provider
    DeregisterProvider(name string) error

    // Close releases all provider resources
    Close() error
}
```

## Provider Configuration

Providers are OpenAI-compatible HTTP clients. Each provider requires a base URL and model name, with optional API key and timeout settings.

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
      base_url: "http://10.0.0.5:8000/v1"
      model: "Alibaba-NLP/gte-large-en-v1.5"
      timeout: "30s"
```

### Provider Setup Examples

**Ollama (local, no auth)**

```bash
ollama pull mxbai-embed-large
ollama serve  # runs on localhost:11434
```

```yaml
providers:
  - name: "ollama"
    base_url: "http://localhost:11434/v1"
    model: "mxbai-embed-large"
```

**OpenAI**

```yaml
providers:
  - name: "openai"
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY}"
    model: "text-embedding-3-large"
```

**Azure OpenAI**

```yaml
providers:
  - name: "azure"
    base_url: "https://your-resource.openai.azure.com/openai/deployments/your-deployment/v1"
    api_key: "${AZURE_OPENAI_API_KEY}"
    model: "text-embedding-3-large"
```

**vLLM (self-hosted)**

```bash
python -m vllm.entrypoints.openai.api_server \
    --model Alibaba-NLP/gte-large-en-v1.5 \
    --port 8000 \
    --trust-remote-code
```

```yaml
providers:
  - name: "vllm"
    base_url: "http://vllm-server:8000/v1"
    model: "Alibaba-NLP/gte-large-en-v1.5"
```

## Configuration Reference

### Configuration Section: `embedding`

| Field                | Type                         | Default | Description |
|----------------------|------------------------------|---------|-------------|
| `default_provider`   | string                       | `""`    | Name of the provider used when none is specified in a request |
| `cache_capacity`     | int                          | `0`     | Max embeddings in LRU cache; 0 disables caching |
| `providers`          | []EmbeddingProviderConfig    | `[]`    | List of configured provider endpoints |

### Provider Configuration: `providers[*]`

| Field        | Type   | Default | Description |
|--------------|--------|---------|-------------|
| `name`       | string | required | Unique provider identifier |
| `base_url`   | string | required | OpenAI-compatible base URL (e.g., `http://localhost:11434/v1`) |
| `api_key`    | string | `""`    | Bearer token for authentication; leave empty for unauthenticated endpoints |
| `model`      | string | required | Default embedding model name |
| `timeout`    | string | `"30s"` | Per-request timeout in Go duration format |
| `max_retries` | int   | `3`     | Retry attempts for transient failures (5xx, network errors) |

## Caching Behavior

When `cache_capacity` is set to a non-zero value, the service maintains an in-memory LRU cache of embeddings. Lookups are keyed by (provider, model, text) tuple.

- Cache hits are served without network calls
- Cache fills on successful provider responses
- Cache is cleared on service shutdown
- No TTL or expiration; cache retains embeddings for the service lifetime

Set `cache_capacity: 10000` for production deployments to reduce token usage and latency.

## Batch Processing

The `BatchEmbed` method splits large text lists into sub-batches respecting the provider's limits:

```go
// Embed 1000 texts in batches of 256
resp, err := embeddingService.BatchEmbed(ctx, &BatchEmbedRequest{
    Texts:     largeTextList,
    Provider:  "openai",
    Model:     "text-embedding-3-large",
    BatchSize: 256,
})

// Results are aggregated in input order
embeddings := resp.Embeddings  // len = 1000
```

Default batch size is 256 when not specified. This is safe for most providers.

## API Endpoints

### REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/embedding/embed` | POST | Generate embeddings for a list of texts |
| `/api/v1/embedding/batch-embed` | POST | Batch embed with automatic sub-batching |
| `/api/v1/embedding/models` | GET | List available models from a provider |

Request body for `/api/v1/embedding/embed`:

```json
{
  "texts": ["text1", "text2"],
  "model": "mxbai-embed-large",
  "provider": "ollama"
}
```

Response:

```json
{
  "embeddings": [
    [0.1, 0.2, 0.3, ...],
    [0.4, 0.5, 0.6, ...]
  ],
  "model": "mxbai-embed-large",
  "usage": {
    "prompt_tokens": 15,
    "total_tokens": 15
  },
  "cached": false
}
```

### gRPC API

```protobuf
service EmbeddingService {
    rpc Embed(EmbedRequest) returns (EmbedResponse);
    rpc BatchEmbed(BatchEmbedRequest) returns (BatchEmbedResponse);
    rpc ListModels(ListEmbeddingModelsRequest) returns (ListEmbeddingModelsResponse);
}
```

Source: `pkg/api/grpc/service_embedding.go`

### MCP (Model Context Protocol)

Tools are registered as "embedding_embed", "embedding_batch_embed", and "embedding_list_models".

Source: `pkg/api/mcp/tools_embedding.go`

## Model Selection

Choose embedding models based on your use case:

| Model | Dims | Speed | Quality | Cost | Use Case |
|-------|------|-------|---------|------|----------|
| `mxbai-embed-large` | 1024 | Fast | Excellent | Free (local) | General-purpose, local |
| `text-embedding-3-small` | 1536 | Fast | Good | Low | High-volume, cost-sensitive |
| `text-embedding-3-large` | 3072 | Slower | Excellent | Medium | Production, quality-critical |
| `nomic-embed-text` | 768 | Fast | Good | Free (local) | Search, local deployment |

For RAG pipelines, higher-quality models (text-embedding-3-large, mxbai-embed-large) improve retrieval accuracy. Smaller models are suitable for keyword-based filtering or when compute is constrained.

## Error Types

Source: `pkg/service/embedding_errors.go`

| Error Type | Condition |
|------------|-----------|
| `*EmbeddingProviderNotFoundError` | Requested provider is not registered |
| `*EmbeddingProviderRegistrationError` | Provider name is already registered |
| `*EmbeddingServiceError` | Generic embedding generation failure |

## Cross-References

- [RAG Pipeline](../rag/README.md) -- Uses embedding service to generate query and document embeddings
- [Configuration](../12-configuration.md) -- `embedding` section
- [Vector Database](../15-vectors.md) -- Stores generated embeddings
- [API Reference](../api.md) -- REST endpoint specifications
