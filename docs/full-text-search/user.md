# Full-Text Search — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Full-text search finds documents that *mention* a word, as opposed to
[secondary indexes](../secondary-indexes/user.md), which find records whose
field *equals* a value.

**Read this first:** FTS is currently reachable only from Go code that imports
`pkg/fts` directly, in a build compiled with the `qrdb_fts` tag. There is no
CLI verb and no API endpoint. If you are looking for a `qrdb fts search`
command, it does not exist yet — see [api.md](api.md).

## Prerequisites

- A build with FTS compiled in: `make build WITH_QRDB_FTS=1`.
- Confirm it is present: `qrdb admin capabilities` lists `fts`.
- In code, `fts.Available()` reports the same thing, so you can branch on it
  without build tags of your own.

## First run

```go
import "github.com/jeremyhahn/go-qrdb/pkg/fts"

idx := fts.NewFTSIndex(store, "messages.Body")

_ = idx.IndexDocument([]byte("messages/12345"),
	"Hey, are we still meeting tomorrow at the coffee shop?")

keys, _ := idx.Search("meeting coffee")
// -> [messages/12345]
```

## Common tasks

**Find documents containing every term** — this is the default.

```go
keys, _ := idx.Search("coffee morning")
// matches "Good morning! Want coffee?"
// does NOT match "Good morning!"  (no "coffee")
```

**Find documents containing any term.**

```go
keys, _ := idx.SearchOr("coffee tea")
// matches "Want coffee?", "Want tea?", "Coffee or tea?"
```

**Take a document out of the index.**

```go
_ = idx.RemoveDocument([]byte("messages/12345"))
```

**Turn on stemming**, so "running" also matches "run"-ish queries. Stemming is
off by default because it is language-specific and lossy.

```go
analyzer := fts.NewAnalyzer(fts.WithStemmer(fts.SimpleStemmer{}))
idx := fts.NewFTSIndex(store, "messages.Body", fts.WithAnalyzer(analyzer))
```

`SimpleStemmer` strips common English suffixes — `running` → `runn`,
`messages` → `messag`, `quickly` → `quick`. It is a rough tool. For real
accuracy, implement the `Stemmer` interface with Porter2 or a
language-specific stemmer.

**Change the stop-word list.**

```go
analyzer := fts.NewAnalyzer(fts.WithStopWords([]string{"the", "and", "foo"}))
```

**Search CJK text.** Nothing to configure — Han, Katakana and Hiragana
characters are each indexed as their own token, so character-level search works
out of the box.

## Troubleshooting

**A search returns nothing and you are sure the word is there.** Three usual
causes: the word is a stop word and was removed at index time; you turned
stemming on (or off) *after* indexing, so the stored tokens no longer match the
query's; or the text was indexed under a different index name. Changing the
analyzer means reindexing — the tokens on disk were produced by the old one.

**Searching a phrase matches documents with the words scattered.** Phrase
queries do not exist. `Search("coffee shop")` is "contains coffee AND contains
shop", not "contains the phrase". Position data is stored for a future phrase
implementation but nothing reads it yet.

**The results are in a useless order.** There is no relevance ranking; results
are unordered. Sort them yourself on whatever your application considers
important.

**Emoji do not match.** They are treated as punctuation and are not indexed.

**A typo returns nothing.** There is no fuzzy matching or typo tolerance.

**Deleting lots of documents is slow.** `RemoveDocument` scans all term entries,
which is O(total index entries). For a bulk purge, rebuilding the index is
usually faster than removing document by document.

**`fts.Available()` is false.** The binary was built without the `qrdb_fts`
tag. Rebuild with `WITH_QRDB_FTS=1`.

**You need this over the network.** There is no transport surface today. That is
a tracked gap, and the shape of the fix is an owner decision — see
[api.md](api.md).
