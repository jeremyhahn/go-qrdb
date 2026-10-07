# Embedding Service — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Command group: `qrdb embedding` — generate vector embeddings for text and list
available models. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

The daemon must have at least one provider configured in its `embedding`
section; otherwise the capability is unavailable.

## `qrdb embedding embed <text> [text...]`

Generate embeddings for one or more texts. Results come back in input order.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--provider` | string | configured `default_provider` | registered provider name |
| `--model` | string | provider default | embedding model |

```bash
qrdb embedding embed "the quick brown fox"
qrdb embedding embed "first text" "second text" --output json
qrdb embedding embed "text" --provider openai --model text-embedding-3-large
```

The response names the model actually used and reports whether the result came
from the cache.

## `qrdb embedding batch-embed <text> [text...]`

Generate embeddings for a large list of texts, split into sub-batches. Results
are aggregated in input order.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--provider` | string | configured default | registered provider name |
| `--model` | string | provider default | model used for **all** batches |
| `--batch-size` | int | 0 (service default, 256) | maximum texts per provider call |

```bash
qrdb embedding batch-embed "t1" "t2" "t3" --batch-size 256
xargs -a texts.txt qrdb embedding batch-embed --provider openai --output json
```

## `qrdb embedding models`

List the embedding models available from a provider.

| Flag | Type | Default |
|---|---|---|
| `--provider` | string | configured default |

```bash
qrdb embedding models
qrdb embedding models --provider openai --output json
```

Each entry carries the model name and, where the provider reports them, its
dimensions and maximum token count. **Match the dimensions to the vector
namespace you intend to store into** — see
[../vectors/cli.md](../vectors/cli.md).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
