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

func TestInvertedIndex_IndexAndSearch(t *testing.T) {
	idx := newTestInvertedIndex(t)

	err := idx.Index([]byte("msg/1"), "Hey are we still meeting tomorrow at the coffee shop?")
	require.NoError(t, err)

	err = idx.Index([]byte("msg/2"), "Let's grab lunch instead of coffee")
	require.NoError(t, err)

	err = idx.Index([]byte("msg/3"), "The project deadline is tomorrow")
	require.NoError(t, err)

	// AND search: both "meeting" and "coffee" must appear
	keys, err := idx.Search("meeting coffee")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/1", string(keys[0]))
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

func TestInvertedIndex_Remove(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "hello world")
	_ = idx.Index([]byte("msg/2"), "hello there")

	// Both should match "hello"
	keys, err := idx.Search("hello")
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	// Remove msg/1
	err = idx.Remove([]byte("msg/1"))
	require.NoError(t, err)

	// Only msg/2 should match now
	keys, err = idx.Search("hello")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/2", string(keys[0]))
}

func TestInvertedIndex_UpdateDocument(t *testing.T) {
	idx := newTestInvertedIndex(t)

	_ = idx.Index([]byte("msg/1"), "hello world")

	// Update the document
	err := idx.Index([]byte("msg/1"), "goodbye world")
	require.NoError(t, err)

	// "hello" should no longer match
	keys, err := idx.Search("hello")
	require.NoError(t, err)
	assert.Empty(t, keys)

	// "goodbye" should match
	keys, err = idx.Search("goodbye")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/1", string(keys[0]))
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

func TestInvertedIndex_LargeCorpus(t *testing.T) {
	idx := newTestInvertedIndex(t)

	messages := []string{
		"Hey, are we still on for the meeting tomorrow?",
		"Just sent you the project files via email",
		"Can you review the pull request when you get a chance?",
		"The deployment is scheduled for Friday afternoon",
		"Let me know if you need help with the database migration",
		"Great work on the presentation today!",
		"I'll be working from home tomorrow",
		"The client meeting went really well",
		"Don't forget to update the documentation",
		"Sprint planning is at 10am on Monday",
	}

	for i, msg := range messages {
		key := []byte("msg/" + string(rune('0'+i)))
		err := idx.Index(key, msg)
		require.NoError(t, err)
	}

	// Search for "meeting"
	keys, err := idx.Search("meeting")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(keys), 2) // at least 2 messages mention "meeting"

	// Search for "tomorrow"
	keys, err = idx.Search("tomorrow")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(keys), 2)

	// Multi-term: "meeting" AND "tomorrow"
	keys, err = idx.Search("meeting tomorrow")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(keys), 1)
}

func TestFTSIndex_PublicAPI(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	defer s.Close()

	fts := NewFTSIndex(s, "messages.body")

	err = fts.IndexDocument([]byte("msg/1"), "hello world")
	require.NoError(t, err)

	err = fts.IndexDocument([]byte("msg/2"), "hello there friend")
	require.NoError(t, err)

	// AND search
	keys, err := fts.Search("hello world")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/1", string(keys[0]))

	// OR search
	keys, err = fts.SearchOr("hello world")
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	// Remove
	err = fts.RemoveDocument([]byte("msg/1"))
	require.NoError(t, err)

	keys, err = fts.Search("hello")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "msg/2", string(keys[0]))
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
