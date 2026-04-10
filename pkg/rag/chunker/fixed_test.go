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

func TestFixedChunker_BasicSplit(t *testing.T) {
	c, err := NewFixedChunker(&Config{ChunkSize: 10, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("0123456789abcdefghij")
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, "0123456789", chunks[0].Text)
	assert.Equal(t, "abcdefghij", chunks[1].Text)
	assert.Equal(t, 0, chunks[0].Index)
	assert.Equal(t, 1, chunks[1].Index)
}

func TestFixedChunker_Overlap(t *testing.T) {
	c, err := NewFixedChunker(&Config{ChunkSize: 6, Overlap: 2, MinSize: 0})
	require.NoError(t, err)

	// "abcdefghij" with chunk=6, overlap=2 → step=4
	// chunk0: [0:6] = "abcdef"
	// chunk1: [4:10] = "efghij"
	chunks, err := c.Chunk("abcdefghij")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(chunks), 2)
	assert.True(t, strings.HasPrefix(chunks[1].Text, "ef"), "chunk1 should start with overlap")
}

func TestFixedChunker_MinSizeMerge(t *testing.T) {
	// chunk=10, overlap=0, min=5: last chunk of 3 chars merges into previous.
	c, err := NewFixedChunker(&Config{ChunkSize: 10, Overlap: 0, MinSize: 5})
	require.NoError(t, err)

	// 23 chars: chunk0=[0:10], chunk1=[10:20], tail=[20:23] (len=3 < minSize=5)
	chunks, err := c.Chunk("0123456789abcdefghijklm")
	require.NoError(t, err)
	// tail "klm" (3 chars) merges into chunk1
	require.Len(t, chunks, 2)
	assert.Equal(t, "abcdefghijklm", chunks[1].Text)
}

func TestFixedChunker_EmptyText(t *testing.T) {
	c, err := NewFixedChunker(DefaultConfig())
	require.NoError(t, err)

	_, err = c.Chunk("")
	require.Error(t, err)
	assert.IsType(t, &EmptyTextError{}, err)
}

func TestFixedChunker_Unicode(t *testing.T) {
	// Each Japanese character is one rune but 3 bytes; chunking must be rune-based.
	c, err := NewFixedChunker(&Config{ChunkSize: 3, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	text := "日本語テスト"
	chunks, err := c.Chunk(text)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, "日本語", chunks[0].Text)
	assert.Equal(t, "テスト", chunks[1].Text)
}

func TestFixedChunker_SingleChunk(t *testing.T) {
	c, err := NewFixedChunker(&Config{ChunkSize: 100, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("short")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "short", chunks[0].Text)
}

func TestFixedChunker_ExactBoundary(t *testing.T) {
	// Text length exactly equals ChunkSize; should produce exactly one chunk.
	c, err := NewFixedChunker(&Config{ChunkSize: 5, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("hello")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "hello", chunks[0].Text)
}

func TestFixedChunker_InvalidConfig_ZeroChunkSize(t *testing.T) {
	_, err := NewFixedChunker(&Config{ChunkSize: 0})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestFixedChunker_InvalidConfig_NegativeOverlap(t *testing.T) {
	_, err := NewFixedChunker(&Config{ChunkSize: 10, Overlap: -1})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestFixedChunker_InvalidConfig_OverlapGEChunkSize(t *testing.T) {
	_, err := NewFixedChunker(&Config{ChunkSize: 5, Overlap: 5})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestFixedChunker_InvalidConfig_NegativeMinSize(t *testing.T) {
	_, err := NewFixedChunker(&Config{ChunkSize: 10, Overlap: 0, MinSize: -1})
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}

func TestFixedChunker_NilConfig_UsesDefaults(t *testing.T) {
	c, err := NewFixedChunker(nil)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestFixedChunker_ChunkIndex(t *testing.T) {
	c, err := NewFixedChunker(&Config{ChunkSize: 4, Overlap: 0, MinSize: 0})
	require.NoError(t, err)

	chunks, err := c.Chunk("abcdefghijkl")
	require.NoError(t, err)
	for i, ch := range chunks {
		assert.Equal(t, i, ch.Index, "chunk index must be sequential")
	}
}
