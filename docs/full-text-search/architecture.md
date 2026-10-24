# Full-Text Search — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

> **Build-tag gated.** This feature exists only in builds carrying the
> `qrdb_fts` tag (`WITH_QRDB_FTS=1`). Without it, `pkg/fts` compiles to a stub
> whose `Available()` reports false.

## Problem

Secondary indexes answer exact and range matches. "Which messages mention
coffee?" is neither: the value being searched is a fragment of a larger text
field, and the useful match is per-token rather than per-value.

## Shape chosen

A classic inverted index over an analyzer pipeline, stored in the same storage
engine as everything else:

```
text
  → Tokenizer      Unicode word splitting, stop-word removal, CJK per-character
  → Analyzer       tokenize + lowercase + optional stemming
  → InvertedIndex  term → entity keys, in storage.Storage
  → FTSIndex       high-level API for DAO integration
```

Two search semantics: `Search` (AND — every term must appear) and `SearchOr`
(OR — any term may appear).

### Alternatives rejected

- **Embedding a search engine (Bleve, Lucene-alike).** Rejected: it brings a
  second storage engine, a second on-disk format, and a second thing to back up
  and seal. The inverted index reuses `storage.Storage`, so it inherits the
  barrier, the backend choice and the snapshot path unchanged.
- **Stemming on by default.** Rejected: stemming is language-specific and lossy.
  The default is **no stemming**; `SimpleStemmer` is opt-in and the `Stemmer`
  interface is the extension point for a real algorithm.
- **Storing tokens without positions.** Not taken — a position is stored with
  each term entry. No query reads it (see the limitations below).
- **Building it into the secondary-index path.** Rejected: the write
  amplification profile is completely different (one entry per unique term per
  document, not one per field), and forcing it into the same code path would
  make every entity write pay for a feature most deployments do not compile in.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Tokenizer (Unicode, stop words, CJK) | `pkg/fts/tokenizer.go` | go-qrdb |
| Analyzer, `Stemmer` interface, `SimpleStemmer` | `pkg/fts/analyzer.go` | go-qrdb |
| Inverted index | `pkg/fts/inverted_index.go` | go-qrdb |
| `FTSIndex` high-level API | `pkg/fts/fts.go` | go-qrdb |
| Build-tag stub | `pkg/fts/stub.go` | go-qrdb |
| Capability registration (`fts`) | `pkg/capabilities` | go-qrdb |
| Underlying storage | `pkg/storage` | go-qrdb |

## Storage format

FTS data uses the `fts:` key prefix so it coexists with entity data and
secondary indexes in one storage engine:

```
fts:{indexName}:term:{token}:{entityKey}{uint32BE(len)}  ->  uint32BE(position)
fts:{indexName}:doc:{entityKey}                          ->  uint32BE(termCount)
```

## Tokenization rules

- Text splits on whitespace and punctuation into lowercase tokens.
- **Han, Katakana and Hiragana characters** each become their own token, which
  is what makes character-level search work for Chinese and Japanese. Hangul is
  a letter like Latin script: Korean splits into whole words.
- **Stop words** — common English words are excluded by default; a custom list
  can be supplied.
- **Emoji** are treated as punctuation (split boundaries) and are not indexed.

## Trust boundaries

FTS is an **in-process library capability**. It takes a `storage.Storage` from
its caller and inherits that store's tenant scoping and barrier state; it does
not perform its own authorization, because there is no transport in front of it
to carry a principal.

That is a consequence of the exposure gap below, not an argument that
authorization is unnecessary: a transport surface for FTS needs its own
permission vocabulary.

## SDK exposure gap

**`pkg/fts` has no `sdk/go` surface, no CLI verb and no wire API on any
transport.** The only way to use it is to import the package in-process. Under
the repo's export obligation — a qrdb capability with no SDK surface is not
shippable — this is a defect, not a design choice; [api.md](api.md) lists what
closing it needs.

## What this feature deliberately does not do

- **No phrase queries.** One position per term per document is stored (the
  last occurrence), and no query reads it.
- **No relevance ranking.** Results are unordered.
- **No field-specific boosting.**
- **No fuzzy matching or typo tolerance.**
- **Single-language stop words** (English by default).
