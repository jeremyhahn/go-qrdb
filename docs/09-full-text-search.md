# Full-Text Search

QRDB provides optional full-text search (FTS) for entity fields via a
custom inverted index built on `storage.Storage`. Because FTS data is
stored in the same storage layer as entity data, it participates in Raft
replication and is consistent with the primary data.

## Build Tag

FTS is behind the `qrdb_fts` build tag to keep the core binary lean:

```bash
go build -tags qrdb_fts ./...
```

Without the tag, the `fts` package compiles as a no-op stub.

## Architecture

```
Text Input
  |
  Analyzer (tokenizer + stemmer)
  |
  InvertedIndex (storage.Storage)
  |
  +-- Term entries: fts:{name}:term:{token}:{entityKey}{len} -> position
  +-- Doc metadata: fts:{name}:doc:{entityKey} -> termCount
```

### Components

- **Tokenizer**: Unicode-aware word splitting with stop word removal.
  CJK characters are treated as individual tokens.
- **Analyzer**: Chains tokenization + lowercasing + optional stemming.
  Default: no stemming. `SimpleStemmer` provides basic English suffix
  stripping. Custom stemmers implement the `Stemmer` interface.
- **InvertedIndex**: Maps terms to entity keys in storage. Supports
  AND and OR search semantics.
- **FTSIndex**: High-level API wrapping InvertedIndex for DAO integration.

## Usage

### Creating an FTS Index

```go
import "github.com/jeremyhahn/go-qrdb/pkg/fts"

// Basic (no stemming)
idx := fts.NewFTSIndex(storage, "messages.Body")

// With stemming
analyzer := fts.NewAnalyzer(fts.WithStemmer(fts.SimpleStemmer{}))
idx := fts.NewFTSIndex(storage, "messages.Body", fts.WithAnalyzer(analyzer))
```

### Indexing Documents

```go
entityKey := []byte("messages/12345")
text := "Hey, are we still meeting tomorrow at the coffee shop?"
err := idx.IndexDocument(entityKey, text)
```

### Searching

```go
// AND search: all terms must match
keys, err := idx.Search("meeting coffee")
// Returns entity keys containing both "meeting" and "coffee"

// OR search: any term may match
keys, err := idx.SearchOr("meeting lunch")
// Returns entity keys containing "meeting" or "lunch" (or both)
```

### Removing Documents

```go
err := idx.RemoveDocument(entityKey)
```

## Tokenization

The tokenizer splits text on whitespace and punctuation, producing
lowercase tokens. Special handling:

- **CJK characters** (Han, Katakana, Hiragana): Each character becomes
  its own token, enabling character-level search for Chinese, Japanese,
  and Korean text.
- **Stop words**: Common English words (a, the, is, etc.) are excluded
  by default. Custom stop word lists can be provided.
- **Emoji**: Treated as punctuation (split boundaries), not indexed.

## Stemming

The `SimpleStemmer` strips common English suffixes:

| Input | Stemmed |
|-------|---------|
| running | runn |
| messages | messag |
| quickly | quick |
| conversation | convers |

For production use with high accuracy, implement the `Stemmer` interface
with a Porter2 algorithm or language-specific stemmer.

## Search Semantics

### AND (default)

All query terms must appear in the document:

```go
keys, _ := idx.Search("coffee morning")
// Matches: "Good morning! Want coffee?"
// Does NOT match: "Good morning!" (missing "coffee")
```

### OR

Any query term may appear:

```go
keys, _ := idx.SearchOr("coffee tea")
// Matches: "Want coffee?", "Want tea?", "Coffee or tea?"
```

## Storage Format

FTS data uses the `fts:` key prefix to coexist with entity data and
secondary indexes in the same storage:

```
fts:{indexName}:term:{token}:{entityKey}{uint32BE(len)} -> uint32BE(position)
fts:{indexName}:doc:{entityKey} -> uint32BE(termCount)
```

## Performance

- **Indexing**: O(T) per document where T = number of unique terms.
  Batch writes minimize storage round-trips.
- **Search**: O(T * K) where T = query terms, K = average postings per
  term. AND search intersects sets starting with the smallest.
- **Storage**: Each term-entity pair is one storage entry. For a corpus
  of 100k messages with ~20 unique terms each, expect ~2M index entries.

## Limitations

- No phrase queries (position data is stored but not yet used for
  phrase matching)
- No relevance ranking (results are unordered)
- No field-specific boosting
- No fuzzy matching or typo tolerance
- Remove operation scans all term entries (O(total index entries))
- Single-language stop words (English by default)
