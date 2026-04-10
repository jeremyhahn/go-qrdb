//go:build qrdb_fts

package fts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenizer_BasicTokenization(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("Hello World")
	assert.Equal(t, []string{"hello", "world"}, tokens)
}

func TestTokenizer_StopWordsRemoved(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("the quick brown fox is a great animal")
	// "the", "is", "a" are stop words
	assert.Equal(t, []string{"quick", "brown", "fox", "great", "animal"}, tokens)
}

func TestTokenizer_Deduplication(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("hello hello hello world")
	assert.Equal(t, []string{"hello", "world"}, tokens)
}

func TestTokenizer_Punctuation(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("Hello, world! How's it going?")
	// "it" is a stop word
	assert.Contains(t, tokens, "hello")
	assert.Contains(t, tokens, "world")
	assert.Contains(t, tokens, "how")
	assert.Contains(t, tokens, "s")
	assert.Contains(t, tokens, "going")
}

func TestTokenizer_CJK(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("Hello 你好世界")
	assert.Contains(t, tokens, "hello")
	assert.Contains(t, tokens, "你")
	assert.Contains(t, tokens, "好")
	assert.Contains(t, tokens, "世")
	assert.Contains(t, tokens, "界")
}

func TestTokenizer_EmptyString(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("")
	assert.Empty(t, tokens)
}

func TestTokenizer_OnlyStopWords(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("the is a an")
	assert.Empty(t, tokens)
}

func TestTokenizer_Numbers(t *testing.T) {
	tok := NewTokenizer()
	tokens := tok.Tokenize("meeting at 3pm on floor 42")
	assert.Contains(t, tokens, "meeting")
	assert.Contains(t, tokens, "3pm")
	assert.Contains(t, tokens, "floor")
	assert.Contains(t, tokens, "42")
}

func TestTokenizer_WithPositions(t *testing.T) {
	tok := NewTokenizer()
	positions := tok.TokenizeWithPositions("the quick brown fox")
	// "the" is a stop word at position 0, skipped
	require.Len(t, positions, 3)
	assert.Equal(t, "quick", positions[0].Token)
	assert.Equal(t, 1, positions[0].Position)
	assert.Equal(t, "brown", positions[1].Token)
	assert.Equal(t, 2, positions[1].Position)
	assert.Equal(t, "fox", positions[2].Token)
	assert.Equal(t, 3, positions[2].Position)
}

func TestTokenizer_CustomStopWords(t *testing.T) {
	tok := NewTokenizerWithStopWords([]string{"hello", "world"})
	tokens := tok.Tokenize("hello beautiful world today")
	assert.Equal(t, []string{"beautiful", "today"}, tokens)
}
