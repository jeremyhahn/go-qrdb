//go:build qrdb_fts

package fts

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

func newTestInvertedIndex(t *testing.T) *InvertedIndex {
	t.Helper()
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewInvertedIndex(s, "test", nil)
}

func TestInvertedIndex_SearchOR(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "meeting at the coffee shop")
	_ = idx.Index([]byte("msg/2"), "lunch at the restaurant")
	_ = idx.Index([]byte("msg/3"), "coffee break time")

	// OR search: "meeting" OR "coffee"
	// msg/1: "meeting", "coffee" - matches both
	// msg/2: "lunch", "restaurant" - no match
	// msg/3: "coffee" - matches
	keys, err := idx.SearchOr("meeting coffee")
	require.NoError(t, err)
	require.Len(t, keys, 2)
	keyStrs := []string{string(keys[0]), string(keys[1])}
	sort.Strings(keyStrs)
	assert.Equal(t, []string{"msg/1", "msg/3"}, keyStrs)
}

func TestInvertedIndex_SearchNoResults(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "hello world")

	keys, err := idx.Search("nonexistent")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestInvertedIndex_SearchEmptyQuery(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "hello world")

	keys, err := idx.Search("")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestInvertedIndex_StopWordsExcluded(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "the quick brown fox")

	// "the" is a stop word, should not be indexed
	keys, err := idx.Search("the")
	require.NoError(t, err)
	assert.Empty(t, keys)

	// "quick" should be found
	keys, err = idx.Search("quick")
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

func TestInvertedIndex_CaseInsensitive(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "Hello WORLD")

	keys, err := idx.Search("hello")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	keys, err = idx.Search("WORLD")
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

func TestInvertedIndex_MultiTermAND(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "coffee meeting tomorrow morning")
	_ = idx.Index([]byte("msg/2"), "coffee break afternoon")
	_ = idx.Index([]byte("msg/3"), "meeting agenda items")

	// "coffee" AND "meeting" - only msg/1
	keys, err := idx.Search("coffee meeting")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/1", string(keys[0]))
}

func TestFTSIndex_WithStemmer(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	defer s.Close()

	analyzer := NewAnalyzer(WithStemmer(SimpleStemmer{}))
	fts := NewFTSIndex(s, "messages.body", WithAnalyzer(analyzer))

	// "running" stems to "runn", "runner" also stems to "runn"
	err = fts.IndexDocument([]byte("msg/1"), "I was running in the park")
	require.NoError(t, err)

	// Search for "runner" should find it because both stem to "runn"
	keys, err := fts.Search("runner")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/1", string(keys[0]))
}

func TestAvailable(t *testing.T) {
	assert.True(t, Available())
}
