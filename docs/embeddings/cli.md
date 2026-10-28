# Embedding Service — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb embedding` — generate vector embeddings for text and list
available models. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

The daemon must have at least one provider configured in its `embedding`
section; otherwise the capability is unavailable.

## Exit codes, output and required flags

Every verb exits `0` on success and `1` on any error, which it prints to
stderr. No flag is required; `embed` and `batch-embed` need at least one text
argument.

| Verb | `--output table` (default) | `--output json` / `yaml` |
|---|---|---|
| `embed`, `batch-embed` | a summary: `model`, `count`, `dims`, `cached` | `embeddings`, `model`, `cached` |
| `models` | a table: `NAME`, `DIMENSIONS`, `MAX_TOKENS` | the model list |

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

`test/integration/embedding_cli_test.go` runs every verb here through the CLI
binary on every transport.
