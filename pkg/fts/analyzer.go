//go:build qrdb_fts

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fts

import "strings"

// Analyzer processes text for indexing and search by applying a chain
// of transformations: tokenization, lowercasing, and optional stemming.
type Analyzer struct {
	tokenizer *Tokenizer
	stemmer   Stemmer
}

// Stemmer reduces words to their root form.
type Stemmer interface {
	Stem(word string) string
}

// AnalyzerOption configures an Analyzer.
type AnalyzerOption func(*Analyzer)

// WithStemmer sets a custom stemmer on the analyzer.
func WithStemmer(s Stemmer) AnalyzerOption {
	return func(a *Analyzer) {
		a.stemmer = s
	}
}

// WithStopWords sets custom stop words on the analyzer's tokenizer.
func WithStopWords(words []string) AnalyzerOption {
	return func(a *Analyzer) {
		a.tokenizer = NewTokenizerWithStopWords(words)
	}
}

// NewAnalyzer creates an Analyzer with the default tokenizer and
// optional configuration. By default, no stemmer is applied.
func NewAnalyzer(opts ...AnalyzerOption) *Analyzer {
	a := &Analyzer{
		tokenizer: NewTokenizer(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Analyze processes text into a list of normalized terms suitable for
// indexing or search. Terms are lowercased and optionally stemmed.
func (a *Analyzer) Analyze(text string) []string {
	tokens := a.tokenizer.Tokenize(text)
	if a.stemmer == nil {
		return tokens
	}

	result := make([]string, 0, len(tokens))
	seen := make(map[string]struct{})
	for _, t := range tokens {
		stemmed := a.stemmer.Stem(t)
		if _, dup := seen[stemmed]; dup {
			continue
		}
		seen[stemmed] = struct{}{}
		result = append(result, stemmed)
	}
	return result
}

// AnalyzeWithPositions processes text into terms with position data.
func (a *Analyzer) AnalyzeWithPositions(text string) []TokenPosition {
	positions := a.tokenizer.TokenizeWithPositions(text)
	if a.stemmer == nil {
		return positions
	}

	result := make([]TokenPosition, 0, len(positions))
	for _, tp := range positions {
		result = append(result, TokenPosition{
			Token:    a.stemmer.Stem(tp.Token),
			Position: tp.Position,
		})
	}
	return result
}

// SimpleStemmer provides basic English suffix stripping. It handles
// common suffixes like -ing, -ed, -ly, -tion, -ness, -ment, -able.
// Not as accurate as Porter2 but has zero dependencies.
type SimpleStemmer struct{}

// Stem reduces a word to an approximate root form.
func (s SimpleStemmer) Stem(word string) string {
	if len(word) <= 3 {
		return word
	}

	// Map of suffixes to strip, ordered by length (longest first).
	suffixes := []struct {
		suffix string
		minLen int // minimum word length after stripping
	}{
		{"ation", 3},
		{"ness", 3},
		{"ment", 3},
		{"able", 3},
		{"ible", 3},
		{"ting", 3},
		{"ing", 3},
		{"ied", 3},
		{"ies", 3},
		{"ion", 3},
		{"ful", 3},
		{"ous", 3},
		{"ive", 3},
		{"ly", 3},
		{"ed", 3},
		{"er", 3},
		{"es", 3},
		{"al", 3},
		{"s", 3},
	}

	for _, sf := range suffixes {
		if strings.HasSuffix(word, sf.suffix) {
			stem := word[:len(word)-len(sf.suffix)]
			if len(stem) >= sf.minLen {
				return stem
			}
		}
	}

	return word
}
