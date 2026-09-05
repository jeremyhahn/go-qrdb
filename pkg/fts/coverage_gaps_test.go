//go:build qrdb_fts

package fts

import (
	"encoding/binary"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ============================================================
// Minimal mock storage infrastructure
// ============================================================

// errBatch is a Batch whose Set and Delete return configurable errors.
//
// Its Reader half (Get, NewIterator) reports an empty batch: storage.Batch
// embeds storage.Reader, but InvertedIndex only ever writes through a batch,
// so no test reaches these methods.
type errBatch struct {
	setErr    error
	deleteErr error
	commitErr error
}

func (b *errBatch) Set(_, _ []byte) error        { return b.setErr }
func (b *errBatch) Delete(_ []byte) error        { return b.deleteErr }
func (b *errBatch) Get(_ []byte) ([]byte, error) { return nil, nil }
func (b *errBatch) Commit() error                { return b.commitErr }
func (b *errBatch) Rollback() error              { return nil }
func (b *errBatch) Close() error                 { return nil }
func (b *errBatch) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return newStaticIterator(nil)
}

// nopBatch succeeds silently for all operations. Its Reader half reports an
// empty batch, for the same reason as errBatch above.
type nopBatch struct{}

func (b *nopBatch) Set(_, _ []byte) error        { return nil }
func (b *nopBatch) Delete(_ []byte) error        { return nil }
func (b *nopBatch) Get(_ []byte) ([]byte, error) { return nil, nil }
func (b *nopBatch) Commit() error                { return nil }
func (b *nopBatch) Rollback() error              { return nil }
func (b *nopBatch) Close() error                 { return nil }
func (b *nopBatch) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return newStaticIterator(nil)
}

// Compile-time proof that the doubles satisfy the full storage.Batch and
// storage.Storage contracts. Without these, an interface method added to Batch
// is only caught at the use site -- which is how the missing Get went
// unnoticed.
var (
	_ storage.Batch   = (*errBatch)(nil)
	_ storage.Batch   = (*nopBatch)(nil)
	_ storage.Batch   = (*mapBatch)(nil)
	_ storage.Storage = (*baseStorage)(nil)
	_ storage.Storage = (*getErrStorage)(nil)
)

// staticIterator iterates over a fixed slice of keys.
type staticIterator struct {
	keys [][]byte
	pos  int
	err  error
}

func newStaticIterator(keys [][]byte) *staticIterator {
	return &staticIterator{keys: keys, pos: -1}
}

func (it *staticIterator) SeekToFirst() bool {
	if len(it.keys) == 0 {
		return false
	}
	it.pos = 0
	return true
}
func (it *staticIterator) SeekToLast() bool {
	if len(it.keys) == 0 {
		return false
	}
	it.pos = len(it.keys) - 1
	return true
}
func (it *staticIterator) Seek(_ []byte) bool { return it.SeekToFirst() }
func (it *staticIterator) Next() bool {
	if it.pos+1 < len(it.keys) {
		it.pos++
		return true
	}
	return false
}
func (it *staticIterator) Prev() bool {
	if it.pos > 0 {
		it.pos--
		return true
	}
	return false
}
func (it *staticIterator) Valid() bool   { return it.pos >= 0 && it.pos < len(it.keys) }
func (it *staticIterator) Key() []byte   { return it.keys[it.pos] }
func (it *staticIterator) Value() []byte { return nil }
func (it *staticIterator) Error() error  { return it.err }
func (it *staticIterator) Close() error  { return nil }

// baseStorage provides an in-memory storage.Storage for controlled injection.
type baseStorage struct {
	data map[string][]byte
}

func newBaseStorage() *baseStorage {
	return &baseStorage{data: make(map[string][]byte)}
}

// Get honours the storage.Reader contract: a missing key is (nil, nil), not an
// error. A double that reports absence as an error makes fail-open error
// handling in the code under test look like a working not-found path.
func (s *baseStorage) Get(key []byte) ([]byte, error) {
	return s.data[string(key)], nil
}
func (s *baseStorage) Set(key, value []byte) error {
	s.data[string(key)] = value
	return nil
}
func (s *baseStorage) Delete(key []byte) error {
	delete(s.data, string(key))
	return nil
}
func (s *baseStorage) List(_ []byte) ([][]byte, error) { return nil, nil }
func (s *baseStorage) Exists(key []byte) (bool, error) { _, ok := s.data[string(key)]; return ok, nil }
func (s *baseStorage) Batch() storage.Batch            { return &nopBatch{} }
func (s *baseStorage) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return newStaticIterator(nil)
}
func (s *baseStorage) Snapshot() (storage.Snapshot, error) { return nil, nil }
func (s *baseStorage) Close() error                        { return nil }

// batchStorage overrides Batch() to return a custom batch.
type batchStorage struct {
	*baseStorage
	b storage.Batch
}

func (s *batchStorage) Batch() storage.Batch { return s.b }

// getErrStorage fails every Get with a fixed error, modelling a backend that
// reports a real read failure (ClosedError, OpError, ErrEmptyKey) rather than a
// missing key.
type getErrStorage struct {
	*baseStorage
	err error
	b   storage.Batch
}

func (s *getErrStorage) Get(_ []byte) ([]byte, error) { return nil, s.err }
func (s *getErrStorage) Batch() storage.Batch         { return s.b }

// mapBatch applies its buffered writes to a baseStorage on Commit, so a test
// can assert what a code path did and did not write. A batch double that
// discards writes would make any "nothing was written" assertion vacuous.
type mapBatch struct {
	store *baseStorage
	sets  map[string][]byte
	dels  map[string]struct{}
}

func newMapBatch(store *baseStorage) *mapBatch {
	return &mapBatch{
		store: store,
		sets:  make(map[string][]byte),
		dels:  make(map[string]struct{}),
	}
}

func (b *mapBatch) Set(key, value []byte) error {
	b.sets[string(key)] = value
	delete(b.dels, string(key))
	return nil
}

func (b *mapBatch) Delete(key []byte) error {
	b.dels[string(key)] = struct{}{}
	delete(b.sets, string(key))
	return nil
}

func (b *mapBatch) Get(key []byte) ([]byte, error) {
	if _, ok := b.dels[string(key)]; ok {
		return nil, nil
	}
	if v, ok := b.sets[string(key)]; ok {
		return v, nil
	}
	return b.store.Get(key)
}

func (b *mapBatch) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return newStaticIterator(nil)
}

func (b *mapBatch) Commit() error {
	for k := range b.dels {
		delete(b.store.data, k)
	}
	for k, v := range b.sets {
		b.store.data[k] = v
	}
	return b.Rollback()
}

func (b *mapBatch) Rollback() error {
	b.sets = make(map[string][]byte)
	b.dels = make(map[string]struct{})
	return nil
}

func (b *mapBatch) Close() error { return nil }

// iterBatchStorage overrides both NewIterator and Batch.
type iterBatchStorage struct {
	*baseStorage
	iter storage.Iterator
	b    storage.Batch
}

func (s *iterBatchStorage) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return s.iter
}
func (s *iterBatchStorage) Batch() storage.Batch { return s.b }

// buildTermKey builds a term storage key using the same format as InvertedIndex.
// Format: "fts:{name}:term:{token}:{entityKey}{uint32BE(len(entityKey))}"
func buildTestTermKey(name, token string, entityKey []byte) []byte {
	prefix := []byte("fts:" + name + ":term:" + token + ":")
	buf := make([]byte, 0, len(prefix)+len(entityKey)+4)
	buf = append(buf, prefix...)
	buf = append(buf, entityKey...)
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, uint32(len(entityKey)))
	return append(buf, lenBytes...)
}

// buildDocKey builds a doc metadata key using the same format as InvertedIndex.
func buildTestDocKey(name string, entityKey []byte) []byte {
	return append([]byte("fts:"+name+":doc:"), entityKey...)
}

// ============================================================
// ftsError.Error() — 0% → 100%
// ============================================================

// TestFTSError_Error exercises the Error() method on ftsError.
func TestFTSError_Error(t *testing.T) {
	err := errKeyTooShort
	assert.Equal(t, "fts: key too short", err.Error())
}

// ============================================================
// extractEntityKeyFromTermKey — 75% → 100%
// ============================================================

// TestExtractEntityKeyFromTermKey_TooShort covers the len(key)<4 guard.
func TestExtractEntityKeyFromTermKey_TooShort(t *testing.T) {
	_, err := extractEntityKeyFromTermKey([]byte{0x01, 0x02})
	require.Error(t, err)
	assert.Equal(t, errKeyTooShort, err)
}

// TestExtractEntityKeyFromTermKey_LengthOverflow covers keyLen+4 > len(key).
func TestExtractEntityKeyFromTermKey_LengthOverflow(t *testing.T) {
	// Last 4 bytes declare length=10, but key is only 4 bytes total.
	key := make([]byte, 4)
	binary.BigEndian.PutUint32(key, 10)
	_, err := extractEntityKeyFromTermKey(key)
	require.Error(t, err)
	assert.Equal(t, errKeyTooShort, err)
}

// TestExtractEntityKeyFromTermKey_Valid is a positive control ensuring both
// error branches are bracketed by a successful execution.
func TestExtractEntityKeyFromTermKey_Valid(t *testing.T) {
	entityKey := []byte("doc/42")
	key := buildTestTermKey("idx", "hello", entityKey)
	// Strip the prefix to isolate the suffix portion the function operates on.
	prefix := []byte("fts:idx:term:hello:")
	suffix := key[len(prefix):]

	got, err := extractEntityKeyFromTermKey(suffix)
	require.NoError(t, err)
	assert.Equal(t, entityKey, got)
}

// ============================================================
// incrementBytes — 75% → 100%
// ============================================================

// TestIncrementBytes_AllFF covers the all-0xff overflow path.
func TestIncrementBytes_AllFF(t *testing.T) {
	result := incrementBytes([]byte{0xff, 0xff, 0xff})
	assert.Equal(t, []byte{0x00, 0x00, 0x00, 0x00}, result)
}

// TestIncrementBytes_Normal covers the common (no-overflow) case.
func TestIncrementBytes_Normal(t *testing.T) {
	assert.Equal(t, []byte{0x01, 0x02, 0x04}, incrementBytes([]byte{0x01, 0x02, 0x03}))
}

// TestIncrementBytes_PartialOverflow covers mid-byte carry propagation.
func TestIncrementBytes_PartialOverflow(t *testing.T) {
	assert.Equal(t, []byte{0x02, 0x00}, incrementBytes([]byte{0x01, 0xff}))
}

// ============================================================
// Index: empty-positions early return — 80% → 85%+
// ============================================================

// TestInvertedIndex_IndexOnlyStopWords covers the len(positions)==0 early return.
func TestInvertedIndex_IndexOnlyStopWords(t *testing.T) {
	idx := newTestInvertedIndex(t)

	err := idx.Index([]byte("doc/sw"), "the is a an")
	require.NoError(t, err)

	keys, err := idx.Search("the")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// TestIndex_BatchSetError covers the batch.Set error path when writing term keys.
func TestIndex_BatchSetError(t *testing.T) {
	sentinel := errors.New("batch set failed")
	entityKey := []byte("doc/1")
	store := &batchStorage{
		baseStorage: newBaseStorage(),
		b:           &errBatch{setErr: sentinel},
	}
	idx := NewInvertedIndex(store, "test", nil)

	// Precondition, asserted rather than assumed: the doc is not indexed, so
	// Remove short-circuits and Index reaches the term-key writes. Stating it
	// here is what keeps the test pinned to batch.Set as the failing call.
	require.NoError(t, idx.Remove(entityKey))

	err := idx.Index(entityKey, "hello world")
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// TestIndex_RemoveError covers the error propagation from Remove inside Index.
// Remove will fail when batch.Delete fails during the term-key scan phase.
func TestIndex_RemoveError(t *testing.T) {
	sentinel := errors.New("remove-induced delete error")
	entityKey := []byte("doc/1")
	indexName := "rmtest"

	// Build a valid term key so the iterator yields it and triggers batch.Delete.
	termKey := buildTestTermKey(indexName, "hello", entityKey)
	iter := newStaticIterator([][]byte{termKey})

	docKey := buildTestDocKey(indexName, entityKey)
	base := newBaseStorage()
	_ = base.Set(docKey, []byte{0, 0, 0, 1}) // doc exists → Remove proceeds

	store := &iterBatchStorage{
		baseStorage: base,
		iter:        iter,
		b:           &errBatch{deleteErr: sentinel},
	}
	idx := NewInvertedIndex(store, indexName, nil)

	// Index calls Remove first; Remove will fail on batch.Delete.
	err := idx.Index(entityKey, "hello world")
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// ============================================================
// SearchOr: empty-terms early return — 85.7% → 100%
// ============================================================

// TestInvertedIndex_SearchOrEmptyQuery covers the len(terms)==0 guard.
func TestInvertedIndex_SearchOrEmptyQuery(t *testing.T) {
	idx := newTestInvertedIndex(t)
	_ = idx.Index([]byte("doc/1"), "hello world")

	keys, err := idx.SearchOr("")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// TestInvertedIndex_SearchOrOnlyStopWords verifies SearchOr returns empty
// when every query word is a stop word.
func TestInvertedIndex_SearchOrOnlyStopWords(t *testing.T) {
	idx := newTestInvertedIndex(t)
	_ = idx.Index([]byte("doc/1"), "hello world")

	keys, err := idx.SearchOr("the is a an")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// ============================================================
// Remove: iter.Error and batch.Delete error paths — 80.8% → higher
// ============================================================

// TestInvertedIndex_RemoveNotIndexed verifies that removing a never-indexed
// entity is a no-op (covers the early-return nil branch in Remove).
func TestInvertedIndex_RemoveNotIndexed(t *testing.T) {
	idx := newTestInvertedIndex(t)
	err := idx.Remove([]byte("nonexistent"))
	require.NoError(t, err)
}

// TestRemove_GetError_Propagates pins the fail-open defect from #298: a real
// storage read error on the doc-metadata lookup must surface from Remove, not
// be reported as "not indexed, nothing to remove".
func TestRemove_GetError_Propagates(t *testing.T) {
	sentinel := errors.New("storage closed during remove")
	base := newBaseStorage()
	store := &getErrStorage{baseStorage: base, err: sentinel, b: newMapBatch(base)}
	idx := NewInvertedIndex(store, "geterr", nil)

	err := idx.Remove([]byte("doc/1"))
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// TestIndex_GetError_NoDuplicateTermWrite is the consequence of the same
// defect. Index calls Remove first, so a swallowed read error would let the
// old postings survive while new ones are written for the new text. The batch
// here really applies to the backing map, so the "nothing was written"
// assertion is not vacuous.
func TestIndex_GetError_NoDuplicateTermWrite(t *testing.T) {
	sentinel := errors.New("storage closed during reindex")
	entityKey := []byte("doc/1")
	indexName := "geterr"

	// The doc is already indexed under its original text.
	base := newBaseStorage()
	pos := []byte{0, 0, 0, 0}
	require.NoError(t, base.Set(buildTestDocKey(indexName, entityKey), []byte{0, 0, 0, 2}))
	require.NoError(t, base.Set(buildTestTermKey(indexName, "hello", entityKey), pos))
	require.NoError(t, base.Set(buildTestTermKey(indexName, "world", entityKey), pos))
	before := len(base.data)

	store := &getErrStorage{baseStorage: base, err: sentinel, b: newMapBatch(base)}
	idx := NewInvertedIndex(store, indexName, nil)

	err := idx.Index(entityKey, "goodbye moon")
	require.Error(t, err)
	assert.Equal(t, sentinel, err)

	// Nothing was written: no postings for the new text, and the old ones are
	// untouched. A fail-open Remove leaves all four terms indexed at once.
	assert.Len(t, base.data, before)
	for _, token := range []string{"goodbye", "moon"} {
		_, ok := base.data[string(buildTestTermKey(indexName, token, entityKey))]
		assert.False(t, ok, "term %q must not be indexed after a failed reindex", token)
	}
	for _, token := range []string{"hello", "world"} {
		_, ok := base.data[string(buildTestTermKey(indexName, token, entityKey))]
		assert.True(t, ok, "term %q must survive a failed reindex", token)
	}
}

// TestRemove_IteratorError covers the iter.Error() propagation in Remove.
func TestRemove_IteratorError(t *testing.T) {
	sentinel := errors.New("iterator error during remove")
	entityKey := []byte("doc/1")
	indexName := "test"

	// erroring iterator yields no keys but reports an error.
	iter := newStaticIterator(nil)
	iter.err = sentinel

	docKey := buildTestDocKey(indexName, entityKey)
	base := newBaseStorage()
	_ = base.Set(docKey, []byte{0, 0, 0, 1}) // doc exists

	store := &iterBatchStorage{
		baseStorage: base,
		iter:        iter,
		b:           &nopBatch{}, // batch succeeds so we reach iter.Error()
	}
	idx := NewInvertedIndex(store, indexName, nil)

	err := idx.Remove(entityKey)
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// TestRemove_DocDeleteError covers the batch.Delete(docKey) error in Remove.
// The term scan succeeds (empty iterator) but deleting doc metadata fails.
func TestRemove_DocDeleteError(t *testing.T) {
	sentinel := errors.New("doc delete failed")
	entityKey := []byte("doc/1")
	indexName := "test"

	docKey := buildTestDocKey(indexName, entityKey)
	base := newBaseStorage()
	_ = base.Set(docKey, []byte{0, 0, 0, 1}) // doc exists

	store := &iterBatchStorage{
		baseStorage: base,
		iter:        newStaticIterator(nil), // empty term scan
		b:           &errBatch{deleteErr: sentinel},
	}
	idx := NewInvertedIndex(store, indexName, nil)

	err := idx.Remove(entityKey)
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// ============================================================
// getTermKeys: corrupt-key continue path — 84.6% → 100%
// ============================================================

// TestGetTermKeys_CorruptKey covers the extractEntityKeyFromTermKey error path
// (continue branch) inside getTermKeys. The corrupt entry must be silently
// skipped while the valid entry is returned.
func TestGetTermKeys_CorruptKey(t *testing.T) {
	idx := newTestInvertedIndex(t)

	err := idx.Index([]byte("doc/1"), "hello world")
	require.NoError(t, err)

	// Inject a corrupt term key directly: its suffix is only 3 bytes, which
	// is too short for extractEntityKeyFromTermKey to parse.
	corruptKey := append(idx.buildTermPrefix("hello"), 0x01, 0x02, 0x03)
	err = idx.storage.Set(corruptKey, []byte{0})
	require.NoError(t, err)

	keys, err := idx.Search("hello")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "doc/1", string(keys[0]))
}

// ============================================================
// Search / SearchOr: iterator error propagation
// ============================================================

// TestSearch_GetTermKeysError covers the getTermKeys error path in Search.
func TestSearch_GetTermKeysError(t *testing.T) {
	sentinel := errors.New("iterator fail in search")
	iter := newStaticIterator(nil)
	iter.err = sentinel

	store := &iterBatchStorage{
		baseStorage: newBaseStorage(),
		iter:        iter,
		b:           &nopBatch{},
	}
	idx := NewInvertedIndex(store, "test", nil)

	_, err := idx.Search("hello world")
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// TestSearchOr_GetTermKeysError covers the getTermKeys error path in SearchOr.
func TestSearchOr_GetTermKeysError(t *testing.T) {
	sentinel := errors.New("iterator error in searchor")
	iter := newStaticIterator(nil)
	iter.err = sentinel

	store := &iterBatchStorage{
		baseStorage: newBaseStorage(),
		iter:        iter,
		b:           &nopBatch{},
	}
	idx := NewInvertedIndex(store, "test", nil)

	_, err := idx.SearchOr("hello world")
	require.Error(t, err)
	assert.Equal(t, sentinel, err)
}

// ============================================================
// Tokenizer: position tracking edge cases
// ============================================================

// TestTokenizer_PositionsWithLeadingStopWords verifies positions reflect the
// original word index even when leading words are stop words.
func TestTokenizer_PositionsWithLeadingStopWords(t *testing.T) {
	tok := NewTokenizer()
	positions := tok.TokenizeWithPositions("the is hello world")
	require.Len(t, positions, 2)
	assert.Equal(t, "hello", positions[0].Token)
	assert.Equal(t, 2, positions[0].Position)
	assert.Equal(t, "world", positions[1].Token)
	assert.Equal(t, 3, positions[1].Position)
}

// TestTokenizer_PositionsOnlyStopWords verifies that TokenizeWithPositions
// returns an empty slice when every word is a stop word.
func TestTokenizer_PositionsOnlyStopWords(t *testing.T) {
	tok := NewTokenizer()
	assert.Empty(t, tok.TokenizeWithPositions("the is a an"))
}

// ============================================================
// SearchOr: multi-document deduplication
// ============================================================

// TestInvertedIndex_SearchOrMultipleDocuments exercises SearchOr returning
// results from multiple documents and validates deduplication.
func TestInvertedIndex_SearchOrMultipleDocuments(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("doc/a"), "alpha beta gamma")
	_ = idx.Index([]byte("doc/b"), "delta epsilon")
	_ = idx.Index([]byte("doc/c"), "gamma zeta")

	keys, err := idx.SearchOr("alpha gamma")
	require.NoError(t, err)
	require.Len(t, keys, 2)
	strs := []string{string(keys[0]), string(keys[1])}
	sort.Strings(strs)
	assert.Equal(t, []string{"doc/a", "doc/c"}, strs)
}

// ============================================================
// AND search: intersection corner cases
// ============================================================

// TestInvertedIndex_SearchANDNoCommonDoc covers AND returning empty when no
// document contains all query terms.
func TestInvertedIndex_SearchANDNoCommonDoc(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("doc/1"), "alpha only here")
	_ = idx.Index([]byte("doc/2"), "beta only there")

	keys, err := idx.Search("alpha beta")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// TestInvertedIndex_SearchMultiTermIntersection covers the AND intersection
// loop for more than two query terms.
func TestInvertedIndex_SearchMultiTermIntersection(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("doc/1"), "apple banana cherry")
	_ = idx.Index([]byte("doc/2"), "apple banana")
	_ = idx.Index([]byte("doc/3"), "banana cherry")

	keys, err := idx.Search("apple banana cherry")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "doc/1", string(keys[0]))
}
