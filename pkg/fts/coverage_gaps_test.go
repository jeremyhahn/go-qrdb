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
type errBatch struct {
	setErr    error
	deleteErr error
	commitErr error
}

func (b *errBatch) Set(_, _ []byte) error { return b.setErr }
func (b *errBatch) Delete(_ []byte) error { return b.deleteErr }
func (b *errBatch) Commit() error         { return b.commitErr }
func (b *errBatch) Rollback() error       { return nil }
func (b *errBatch) Close() error          { return nil }

// nopBatch succeeds silently for all operations.
type nopBatch struct{}

func (b *nopBatch) Set(_, _ []byte) error { return nil }
func (b *nopBatch) Delete(_ []byte) error { return nil }
func (b *nopBatch) Commit() error         { return nil }
func (b *nopBatch) Rollback() error       { return nil }
func (b *nopBatch) Close() error          { return nil }

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

func (s *baseStorage) Get(key []byte) ([]byte, error) {
	v, ok := s.data[string(key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
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
	store := &batchStorage{
		baseStorage: newBaseStorage(),
		b:           &errBatch{setErr: sentinel},
	}
	// Get must return not-found so that Remove returns nil (no doc indexed yet).
	idx := NewInvertedIndex(store, "test", nil)

	err := idx.Index([]byte("doc/1"), "hello world")
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
