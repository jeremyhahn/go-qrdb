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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentenceChunker_BasicSentences(t *testing.T) {
	c, err := NewSentenceChunker(&Config{ChunkSize: 200, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "Hello world. This is a test. Another sentence here."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	// All text should be present across all chunks.
	all := ""
	for _, ch := range chunks {
		all += ch.Text + " "
	}
	assert.Contains(t, all, "Hello world")
	assert.Contains(t, all, "Another sentence here")
}

func TestSentenceChunker_Abbreviations(t *testing.T) {
	c, err := NewSentenceChunker(&Config{ChunkSize: 300, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	// "Dr." and "Mr." must not be treated as sentence boundaries.
	text := "Dr. Smith met Mr. Jones at the conference. They discussed AI."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	// The abbreviations should not split the first sentence.
	assert.Contains(t, chunks[0].Text, "Dr. Smith met Mr. Jones")
}

func TestSentenceChunker_EmptyText(t *testing.T) {
	c, err := NewSentenceChunker(DefaultConfig())
	require.NoError(t, err)

	_, err = c.Chunk("")
	require.Error(t, err)
	assert.IsType(t, &EmptyTextError{}, err)
}

func TestSentenceChunker_LongSentence(t *testing.T) {
	// A sentence longer than ChunkSize should be split by FixedChunker.
	c, err := NewSentenceChunker(&Config{ChunkSize: 20, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "This is a very long sentence that definitely exceeds the chunk size limit."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(chunks), 2)
}

func TestSentenceChunker_MixedPunctuation(t *testing.T) {
	c, err := NewSentenceChunker(&Config{ChunkSize: 300, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "Is this a question? Yes! It is. Absolutely."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	all := ""
	for _, ch := range chunks {
		all += ch.Text + " "
	}
	assert.Contains(t, all, "Is this a question?")
	assert.Contains(t, all, "Yes!")
}

func TestSentenceChunker_Unicode(t *testing.T) {
	c, err := NewSentenceChunker(&Config{ChunkSize: 200, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "Привет мир. Это тест. Ещё одно предложение."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
}

func TestSentenceChunker_ChunkSizeBoundary(t *testing.T) {
	// When two sentences together exceed ChunkSize they should end up in
	// separate chunks.
	c, err := NewSentenceChunker(&Config{ChunkSize: 30, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "First sentence here end. Second sentence here end."
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	// Both sentences are ~24 chars; together they exceed 30.
	require.GreaterOrEqual(t, len(chunks), 2)
}

func TestSentenceChunker_InvalidConfig_ZeroChunkSize(t *testing.T) {
	_, err := NewSentenceChunker(&Config{ChunkSize: 0})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestSentenceChunker_InvalidConfig_NegativeMinSize(t *testing.T) {
	_, err := NewSentenceChunker(&Config{ChunkSize: 100, MinSize: -1})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestSentenceChunker_NilConfig_UsesDefaults(t *testing.T) {
	c, err := NewSentenceChunker(nil)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestSentenceChunker_SingleSentence(t *testing.T) {
	c, err := NewSentenceChunker(&Config{ChunkSize: 500, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("Just one sentence.")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Just one sentence.", chunks[0].Text)
}
