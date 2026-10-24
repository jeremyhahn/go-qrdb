# Full-Text Search — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

> **Build-tag gated: `qrdb_fts`** (`WITH_QRDB_FTS=1` in the Makefile). Without
> the tag, `pkg/fts` is the stub in `stub.go` and `Available()` returns false.

## Exported surface

`pkg/fts` — in-process only; there is no SDK, CLI or wire surface (see
[api.md](api.md)).

```go
// Availability
func Available() bool          // true only in a qrdb_fts build

// Analyzer
type Stemmer interface { /* stemming contract */ }
type Analyzer struct{ /* ... */ }
type AnalyzerOption func(*Analyzer)

func NewAnalyzer(opts ...AnalyzerOption) *Analyzer
func WithStemmer(s Stemmer) AnalyzerOption
func WithStopWords(words []string) AnalyzerOption

type SimpleStemmer struct{}

// Tokenizer
type Tokenizer struct{ /* ... */ }
type TokenPosition struct{ /* ... */ }

func NewTokenizer() *Tokenizer
func NewTokenizerWithStopWords(words []string) *Tokenizer

// Inverted index (low level)
type InvertedIndex struct{ /* ... */ }

func NewInvertedIndex(store storage.Storage, name string, analyzer *Analyzer) *InvertedIndex
func (idx *InvertedIndex) Index(entityKey []byte, text string) error
func (idx *InvertedIndex) Remove(entityKey []byte) error
func (idx *InvertedIndex) Search(query string) ([][]byte, error)
func (idx *InvertedIndex) SearchOr(query string) ([][]byte, error)

// FTSIndex (high level, for DAO integration)
type FTSIndex struct{ /* ... */ }
type FTSOption func(*ftsConfig)

func NewFTSIndex(store storage.Storage, name string, opts ...FTSOption) *FTSIndex
func WithAnalyzer(a *Analyzer) FTSOption
func (f *FTSIndex) IndexDocument(entityKey []byte, text string) error
func (f *FTSIndex) RemoveDocument(entityKey []byte) error
func (f *FTSIndex) Search(query string) ([][]byte, error)
func (f *FTSIndex) SearchOr(query string) ([][]byte, error)
```

Usage:

```go
// Default: no stemming
idx := fts.NewFTSIndex(store, "messages.Body")

// With stemming
analyzer := fts.NewAnalyzer(fts.WithStemmer(fts.SimpleStemmer{}))
idx = fts.NewFTSIndex(store, "messages.Body", fts.WithAnalyzer(analyzer))

_ = idx.IndexDocument([]byte("messages/12345"),
	"Hey, are we still meeting tomorrow at the coffee shop?")

keys, _ := idx.Search("meeting coffee")    // AND: both terms
keys, _ = idx.SearchOr("meeting lunch")    // OR: either term
```

## Invariants

- **Default analysis does not stem.** `NewAnalyzer()` with no options
  tokenizes and lowercases only. Stemming changes what is stored, so turning it
  on requires reindexing.
- **`Search` is AND, `SearchOr` is OR.** AND intersects the postings of every
  query term.
- **Results are unordered.** There is no relevance score, so a caller must not
  read the slice order as ranking.
- **Tokenization rules**: whitespace and punctuation split; tokens lowercase;
  Han, Katakana and Hiragana one token per character, Hangul split into words;
  English stop words removed by default; emoji treated as punctuation and not
  indexed.
- **Positions are stored but unused.** The value at a term key is the position
  of the term's last occurrence in the document; no search path reads it.
- **The index and the documents share one `storage.Storage`.** FTS inherits its
  store's tenant scope and barrier state rather than holding its own.
- **`Available()` is the only correct availability probe.** Both the real
  implementation and the stub define it, so a caller compiles either way.

## Storage layout

```
fts:{indexName}:term:{token}:{entityKey}{uint32BE(len)}  ->  uint32BE(position)
fts:{indexName}:doc:{entityKey}                          ->  uint32BE(termCount)
```

The `fts:` prefix keeps FTS data from colliding with entity data and secondary
indexes in the same store.

**Compatibility**: the key layout and the `uint32BE` encoding are the on-disk
format. Changing the analyzer configuration (stemmer, stop words) changes which
tokens are written, so it is a reindex, not a migration.

## Error semantics

`pkg/fts` returns a package-local typed error (`ftsError`); `fmt.Errorf` is not
used.

| Condition | Behavior |
|---|---|
| Built without `qrdb_fts` | `Available()` is false; the stub provides no index type |
| Storage write failure during indexing | error returned; the document is not partially searchable in a way the caller cannot see |
| Query with only stop words | empty result — every term was removed by the analyzer |
| `Remove` for an unindexed key | no-op |

## Performance characteristics

- **Indexing**: O(T) per document, T = unique terms. Writes are batched to
  minimize storage round-trips.
- **Search**: O(T × K), T = query terms, K = average postings per term. AND
  search intersects the posting sets of every query term.
- **Storage**: one entry per term-entity pair. A corpus of 100k documents with
  ~20 unique terms each yields on the order of 2M index entries.
- **`Remove` scans all term entries** — O(total index entries). This is the
  sharpest cost and the reason bulk deletion is expensive.

## Test strategy

- **Unit**, all under the `qrdb_fts` tag: analyzer (basic, with stemmer, stemmer
  deduplication, custom stop words, positions), `SimpleStemmer` (common
  suffixes, short words), inverted index (index and search, OR search, no
  results), tokenizer (CJK, emoji, stop words).
- **Stub tests** verify the non-tagged build compiles and `Available()` reports
  false. These run on every push through `make test` / `make coverage`, under
  the Makefile's default `BUILD_TAGS` (`WITH_QRDB_FTS=0`).
- **The real implementation is gated separately**, `make coverage-fts`
  (`go test -race -tags "$BUILD_TAGS qrdb_fts" ./pkg/fts/...` plus a coverage
  check), run as its own CI step in the `image` job. `make coverage`'s own
  profile never compiles this package's real code -- only the stub -- so the
  stub tests above do not exercise it, and this is the step that does, on
  every push. Its coverage is ratcheted against
  `test/coverage-baseline-fts.txt`, a baseline separate from
  `test/coverage-baseline.txt` because the two are measured under different
  tag sets and the ratchet refuses to compare across a tag mismatch.
  `make coverage-fts-baseline` regenerates it, the same way
  `make coverage-baseline` regenerates the main one.
- **Lint is a known gap.** `make lint` runs with `LINT_TAGS`, which is
  `BUILD_TAGS` joined -- true coverage for a tag most features carry by
  default, but `qrdb_fts` is not one of them (`WITH_QRDB_FTS` defaults to 0), so
  `golangci-lint` never sees `analyzer.go`, `tokenizer.go` or
  `inverted_index.go`, only `stub.go`. `LINT_TAGS` is shared by every
  `make lint` invocation, so adding `qrdb_fts` to it would lint the whole tree
  under one more tag rather than add a package-scoped check, which needs its
  own design.
- **Not covered by the integration parity suite**, because there is no transport
  surface to exercise.

## Known gaps

- **No SDK, CLI or wire surface** — see [api.md](api.md). This is an export
  obligation defect, not a scope decision.
- No phrase queries (positions stored, unused), no relevance ranking, no field
  boosting, no fuzzy matching, single-language stop words.

## Integration testing

The feature has no CLI surface, so it has no integration test: `make integration-test-full-text-search` selects none. The per-backend `FTSIndex` assertions (memory, file, Pebble) are unit tests in `pkg/fts/fts_test.go`, which `make test-fts` runs under the `qrdb_fts` tag. The per-transport accounting, including every gap, is the `full-text-search` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
