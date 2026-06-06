//go:build qrdb_fts

package fts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnalyzer_BasicAnalysis(t *testing.T) {
	a := NewAnalyzer()
	terms := a.Analyze("Hello World")
	assert.Equal(t, []string{"hello", "world"}, terms)
}

func TestAnalyzer_WithStemmer(t *testing.T) {
	a := NewAnalyzer(WithStemmer(SimpleStemmer{}))
	terms := a.Analyze("running messages quickly")
	// SimpleStemmer strips common suffixes
	assert.Contains(t, terms, "runn")   // running -> runn
	assert.Contains(t, terms, "messag") // messages -> messag
	assert.Contains(t, terms, "quick")  // quickly -> quick
}

func TestAnalyzer_StemmerDeduplication(t *testing.T) {
	a := NewAnalyzer(WithStemmer(SimpleStemmer{}))
	// "running" and "runner" both stem to "runn"
	terms := a.Analyze("running runner")
	assert.Equal(t, []string{"runn"}, terms)
}

func TestAnalyzer_WithCustomStopWords(t *testing.T) {
	a := NewAnalyzer(WithStopWords([]string{"custom", "stop"}))
	terms := a.Analyze("custom stop words remain")
	assert.Equal(t, []string{"words", "remain"}, terms)
}

func TestAnalyzer_WithPositions(t *testing.T) {
	a := NewAnalyzer()
	positions := a.AnalyzeWithPositions("the quick brown fox")
	assert.Len(t, positions, 3) // "the" is stop word
	assert.Equal(t, "quick", positions[0].Token)
}

func TestAnalyzer_WithStemmerPositions(t *testing.T) {
	a := NewAnalyzer(WithStemmer(SimpleStemmer{}))
	positions := a.AnalyzeWithPositions("running quickly")
	assert.Len(t, positions, 2)
	assert.Equal(t, "runn", positions[0].Token)
	assert.Equal(t, "quick", positions[1].Token)
}

func TestSimpleStemmer_CommonSuffixes(t *testing.T) {
	s := SimpleStemmer{}

	cases := map[string]string{
		"running":      "runn",
		"messages":     "messag",
		"quickly":      "quick",
		"conversation": "convers",
		"happiness":    "happi",
		"movement":     "move",
		"readable":     "read",
		"possible":     "poss",
		"beautiful":    "beauti",
		"dangerous":    "danger",
		"active":       "act",
		"played":       "play",
		"player":       "play",
		"global":       "glob",
	}

	for input, expected := range cases {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, expected, s.Stem(input))
		})
	}
}

func TestSimpleStemmer_ShortWords(t *testing.T) {
	s := SimpleStemmer{}
	// Words <= 3 chars should not be stemmed
	assert.Equal(t, "the", s.Stem("the"))
	assert.Equal(t, "go", s.Stem("go"))
	assert.Equal(t, "a", s.Stem("a"))
}
