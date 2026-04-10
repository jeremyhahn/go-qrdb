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

import (
	"strings"
	"unicode"
)

// Tokenizer splits text into individual tokens for indexing and search.
type Tokenizer struct {
	stopWords map[string]struct{}
}

// NewTokenizer creates a tokenizer with the default English stop words.
func NewTokenizer() *Tokenizer {
	return &Tokenizer{
		stopWords: defaultStopWords(),
	}
}

// NewTokenizerWithStopWords creates a tokenizer with custom stop words.
func NewTokenizerWithStopWords(words []string) *Tokenizer {
	sw := make(map[string]struct{}, len(words))
	for _, w := range words {
		sw[strings.ToLower(w)] = struct{}{}
	}
	return &Tokenizer{stopWords: sw}
}

// Tokenize splits text into lowercase tokens, removes stop words, and
// returns the unique tokens in order of first appearance.
func (t *Tokenizer) Tokenize(text string) []string {
	words := splitWords(text)
	var tokens []string
	seen := make(map[string]struct{})

	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) == 0 {
			continue
		}
		if _, stop := t.stopWords[lower]; stop {
			continue
		}
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		tokens = append(tokens, lower)
	}

	return tokens
}

// TokenizeWithPositions splits text into lowercase tokens with their
// positions (0-based word index). Stop words are excluded.
func (t *Tokenizer) TokenizeWithPositions(text string) []TokenPosition {
	words := splitWords(text)
	var tokens []TokenPosition
	pos := 0

	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) == 0 {
			pos++
			continue
		}
		if _, stop := t.stopWords[lower]; stop {
			pos++
			continue
		}
		tokens = append(tokens, TokenPosition{Token: lower, Position: pos})
		pos++
	}

	return tokens
}

// TokenPosition holds a token and its position in the source text.
type TokenPosition struct {
	Token    string
	Position int
}

// splitWords splits text on whitespace and punctuation boundaries,
// yielding individual words. Unicode-aware: handles CJK by treating
// each CJK character as a separate token.
func splitWords(text string) []string {
	var words []string
	var current []rune

	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = current[:0]
		}
	}

	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) {
			// CJK characters: each is its own token.
			flush()
			words = append(words, string(r))
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
		} else {
			// Whitespace, punctuation, etc.
			flush()
		}
	}
	flush()

	return words
}

// defaultStopWords returns a set of common English stop words.
func defaultStopWords() map[string]struct{} {
	words := []string{
		"a", "an", "and", "are", "as", "at", "be", "but", "by",
		"for", "if", "in", "into", "is", "it", "no", "not", "of",
		"on", "or", "such", "that", "the", "their", "then", "there",
		"these", "they", "this", "to", "was", "will", "with",
	}
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}
