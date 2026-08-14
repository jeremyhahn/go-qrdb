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
)

// ParagraphChunker splits text on double-newline boundaries. Small paragraphs
// are merged to approach ChunkSize; paragraphs that exceed ChunkSize are
// further split using a FixedChunker.
type ParagraphChunker struct {
	config *Config
}

// Compile-time assertion that ParagraphChunker implements Chunker.
var _ Chunker = (*ParagraphChunker)(nil)

// NewParagraphChunker constructs a ParagraphChunker and validates cfg.
func NewParagraphChunker(cfg *Config) (*ParagraphChunker, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.ChunkSize <= 0 {
		return nil, &InvalidConfigError{Reason: "chunk_size must be > 0"}
	}
	if cfg.MinSize < 0 {
		return nil, &InvalidConfigError{Reason: "min_size must be >= 0"}
	}
	return &ParagraphChunker{config: cfg}, nil
}

// Chunk splits text on double-newline boundaries. Paragraphs smaller than
// MinSize are merged with the following paragraph. Paragraphs larger than
// ChunkSize are split using fixed-size windows. Returns EmptyTextError when
// text is empty.
func (c *ParagraphChunker) Chunk(text string) ([]Chunk, error) {
	if text == "" {
		return nil, &EmptyTextError{}
	}

	// Split on double-newline, normalising \r\n sequences.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	rawParas := strings.Split(normalized, "\n\n")

	// Collect non-empty paragraphs, trimming trailing single newlines.
	paragraphs := make([]string, 0, len(rawParas))
	for _, p := range rawParas {
		trimmed := strings.TrimRight(p, "\n")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			paragraphs = append(paragraphs, trimmed)
		}
	}

	if len(paragraphs) == 0 {
		return []Chunk{{Text: strings.TrimSpace(text), Index: 0, Offset: 0, Length: len(strings.TrimSpace(text))}}, nil
	}

	// Merge small paragraphs: accumulate until the combined length >= MinSize
	// or there are no more paragraphs to merge with.
	merged := mergeParagraphs(paragraphs, c.config.MinSize)

	// Build chunks: split oversized paragraphs with a FixedChunker.
	chunks := make([]Chunk, 0, len(merged))
	byteOffset := 0

	for _, para := range merged {
		paraRunes := len([]rune(para))
		if paraRunes > c.config.ChunkSize {
			// Delegate to FixedChunker for oversized paragraphs.
			subCfg := &Config{
				Strategy:  "fixed",
				ChunkSize: c.config.ChunkSize,
				Overlap:   c.config.Overlap,
				MinSize:   0, // don't re-merge sub-chunks
			}
			fc, err := NewFixedChunker(subCfg)
			if err != nil {
				return nil, err
			}
			subChunks, err := fc.Chunk(para)
			if err != nil {
				return nil, err
			}
			for _, sc := range subChunks {
				sc.Offset += byteOffset
				sc.Index = len(chunks)
				chunks = append(chunks, sc)
			}
		} else {
			ch := Chunk{
				Text:   para,
				Index:  len(chunks),
				Offset: byteOffset,
				Length: len(para),
			}
			chunks = append(chunks, ch)
		}
		byteOffset += len(para) + 2 // +2 for the "\n\n" separator
	}

	return chunks, nil
}

// mergeParagraphs combines consecutive paragraphs that are individually
// smaller than minSize into a single unit, separated by a single space.
// When minSize <= 0, no merging is performed.
func mergeParagraphs(paragraphs []string, minSize int) []string {
	if minSize <= 0 {
		return paragraphs
	}

	merged := make([]string, 0, len(paragraphs))
	var accumulator strings.Builder

	for _, para := range paragraphs {
		paraLen := len([]rune(para))

		switch {
		case accumulator.Len() == 0:
			accumulator.WriteString(para)
		case paraLen < minSize:
			// The current paragraph is small; append it to the accumulator.
			accumulator.WriteString("\n\n")
			accumulator.WriteString(para)
		default:
			// Current accumulated block is ready; flush and start fresh.
			merged = append(merged, accumulator.String())
			accumulator.Reset()
			accumulator.WriteString(para)
		}

		// Flush when accumulated block has reached MinSize.
		if len([]rune(accumulator.String())) >= minSize {
			merged = append(merged, accumulator.String())
			accumulator.Reset()
		}
	}

	if accumulator.Len() > 0 {
		s := accumulator.String()
		if len(merged) > 0 && len([]rune(s)) < minSize {
			// Append remaining small block to the last merged entry.
			merged[len(merged)-1] = merged[len(merged)-1] + "\n\n" + s
		} else {
			merged = append(merged, s)
		}
	}

	return merged
}
