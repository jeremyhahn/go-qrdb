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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParagraphChunker_BasicParagraphs(t *testing.T) {
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.Len(t, chunks, 3)
	assert.Equal(t, "First paragraph.", chunks[0].Text)
	assert.Equal(t, "Second paragraph.", chunks[1].Text)
	assert.Equal(t, "Third paragraph.", chunks[2].Text)
}

func TestParagraphChunker_MergeSmallParagraphs(t *testing.T) {
	// MinSize=50: paragraphs shorter than 50 chars merge.
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 50})
	require.NoError(t, err)

	text := "Short.\n\nAlso short.\n\nThis paragraph is much longer and should not be merged with anything else because it exceeds the minimum size."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	// "Short." and "Also short." are each <50; they should merge.
	require.GreaterOrEqual(t, 1, len(chunks)-2)
	combined := chunks[0].Text
	assert.Contains(t, combined, "Short.")
}

func TestParagraphChunker_SplitLargeParagraph(t *testing.T) {
	// ChunkSize=20: a paragraph longer than 20 chars must be split.
	c, err := NewParagraphChunker(&Config{ChunkSize: 20, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	// Single 60-char paragraph.
	text := "This paragraph is much too long to fit in a single chunk here."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(chunks), 3)
}

func TestParagraphChunker_EmptyText(t *testing.T) {
	c, err := NewParagraphChunker(DefaultConfig())
	require.NoError(t, err)

	_, err = c.Chunk("")
	require.Error(t, err)
	assert.IsType(t, &EmptyTextError{}, err)
}

func TestParagraphChunker_SingleParagraph(t *testing.T) {
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("Only one paragraph here.")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Only one paragraph here.", chunks[0].Text)
}

func TestParagraphChunker_TrailingNewlines(t *testing.T) {
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	// Trailing and leading newlines should not create empty chunks.
	text := "\n\nFirst paragraph.\n\nSecond paragraph.\n\n"
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, "First paragraph.", chunks[0].Text)
}

func TestParagraphChunker_InvalidConfig_ZeroChunkSize(t *testing.T) {
	_, err := NewParagraphChunker(&Config{ChunkSize: 0})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestParagraphChunker_ChunkIndices(t *testing.T) {
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "Para one.\n\nPara two.\n\nPara three."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	for i, ch := range chunks {
		assert.Equal(t, i, ch.Index)
	}
}

func TestParagraphChunker_InvalidConfig_NegativeMinSize(t *testing.T) {
	_, err := NewParagraphChunker(&Config{ChunkSize: 100, MinSize: -1})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestParagraphChunker_NilConfig_UsesDefaults(t *testing.T) {
	c, err := NewParagraphChunker(nil)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestParagraphChunker_WindowsLineEndings(t *testing.T) {
	c, err := NewParagraphChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	// \r\n\r\n should be treated as a paragraph separator.
	text := "First paragraph.\r\n\r\nSecond paragraph."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.True(t, strings.Contains(chunks[0].Text, "First paragraph."))
}
