//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package chunker

import (
	"strings"
	"unicode"
)

// commonAbbreviations contains lowercase tokens that end with a period but
// are not sentence terminators.
var commonAbbreviations = map[string]struct{}{
	"mr.":   {},
	"mrs.":  {},
	"ms.":   {},
	"dr.":   {},
	"prof.": {},
	"sr.":   {},
	"jr.":   {},
	"vs.":   {},
	"etc.":  {},
	"e.g.":  {},
	"i.e.":  {},
	"no.":   {},
	"st.":   {},
	"lt.":   {},
	"gov.":  {},
	"rep.":  {},
	"sen.":  {},
	"dept.": {},
}

// SentenceChunker accumulates sentences until approaching ChunkSize, then
// starts a new chunk. Sentence boundaries are detected using terminal
// punctuation (. ! ?) while skipping common abbreviations.
type SentenceChunker struct {
	config *Config
}

// Compile-time assertion that SentenceChunker implements Chunker.
var _ Chunker = (*SentenceChunker)(nil)

// NewSentenceChunker constructs a SentenceChunker and validates cfg.
func NewSentenceChunker(cfg *Config) (*SentenceChunker, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.ChunkSize <= 0 {
		return nil, &InvalidConfigError{Reason: "chunk_size must be > 0"}
	}
	if cfg.MinSize < 0 {
		return nil, &InvalidConfigError{Reason: "min_size must be >= 0"}
	}
	return &SentenceChunker{config: cfg}, nil
}

// Chunk splits text on sentence boundaries. Sentences are accumulated until
// the combined length approaches ChunkSize, at which point a new chunk is
// started. Returns EmptyTextError when text is empty.
func (c *SentenceChunker) Chunk(text string) ([]Chunk, error) {
	if text == "" {
		return nil, &EmptyTextError{}
	}

	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return []Chunk{{Text: text, Index: 0, Offset: 0, Length: len(text)}}, nil
	}

	chunks := make([]Chunk, 0, len(sentences)/3+1)
	var current strings.Builder
	currentOffset := 0
	bytePos := 0

	flush := func() {
		if current.Len() == 0 {
			return
		}
		s := strings.TrimSpace(current.String())
		if s == "" {
			return
		}
		ch := Chunk{
			Text:   s,
			Index:  len(chunks),
			Offset: currentOffset,
			Length: len(s),
		}
		chunks = append(chunks, ch)
		current.Reset()
	}

	firstInChunk := true
	for _, sentence := range sentences {
		sentLen := len([]rune(sentence))
		curLen := len([]rune(current.String()))

		// Start a new chunk when adding this sentence would exceed ChunkSize
		// and we already have content.
		if curLen > 0 && curLen+sentLen > c.config.ChunkSize {
			flush()
			currentOffset = bytePos
			firstInChunk = true
		}

		if !firstInChunk {
			current.WriteRune(' ')
		}
		current.WriteString(sentence)
		firstInChunk = false
		bytePos += len(sentence) + 1 // +1 for separator
	}
	flush()

	// Handle a single oversized sentence by delegating to fixed chunking.
	result := make([]Chunk, 0, len(chunks))
	for _, ch := range chunks {
		if len([]rune(ch.Text)) > c.config.ChunkSize {
			subCfg := &Config{
				Strategy:  "fixed",
				ChunkSize: c.config.ChunkSize,
				Overlap:   c.config.Overlap,
				MinSize:   c.config.MinSize,
			}
			fc, err := NewFixedChunker(subCfg)
			if err != nil {
				return nil, err
			}
			subChunks, err := fc.Chunk(ch.Text)
			if err != nil {
				return nil, err
			}
			for _, sc := range subChunks {
				sc.Offset += ch.Offset
				sc.Index = len(result)
				result = append(result, sc)
			}
		} else {
			ch.Index = len(result)
			result = append(result, ch)
		}
	}

	return result, nil
}

// splitSentences splits text into individual sentences using terminal
// punctuation (.!?) as delimiters, while respecting common abbreviations.
func splitSentences(text string) []string {
	runes := []rune(text)
	total := len(runes)
	sentences := make([]string, 0, 16)
	start := 0

	for i := 0; i < total; i++ {
		r := runes[i]
		if r != '.' && r != '!' && r != '?' {
			continue
		}

		// Look ahead: a sentence boundary requires whitespace or end-of-text
		// after the terminal punctuation.
		next := i + 1
		for next < total && runes[next] == '\'' || next < total && runes[next] == '"' {
			next++
		}
		if next < total && !unicode.IsSpace(runes[next]) {
			continue
		}

		// Check for common abbreviations when the terminator is '.'.
		if r == '.' {
			// Extract the word ending at index i (inclusive).
			wordStart := i
			for wordStart > start && !unicode.IsSpace(runes[wordStart-1]) {
				wordStart--
			}
			token := strings.ToLower(string(runes[wordStart : i+1]))
			if _, isAbbrev := commonAbbreviations[token]; isAbbrev {
				continue
			}
		}

		sentence := strings.TrimSpace(string(runes[start : i+1]))
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
		start = next
		// Skip leading whitespace for the next sentence.
		for start < total && unicode.IsSpace(runes[start]) {
			start++
		}
		i = start - 1
	}

	// Capture any remaining text after the last terminal punctuation.
	if start < total {
		tail := strings.TrimSpace(string(runes[start:]))
		if tail != "" {
			sentences = append(sentences, tail)
		}
	}

	return sentences
}
