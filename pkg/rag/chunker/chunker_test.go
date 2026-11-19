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

func TestNew_EmptyStrategy_DefaultsToFixed(t *testing.T) {
	cfg := &Config{Strategy: "", ChunkSize: 50, Overlap: 0, MinSize: 0}
	ch, err := New(cfg)
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestNew_SentenceStrategy(t *testing.T) {
	cfg := &Config{Strategy: "sentence", ChunkSize: 200, Overlap: 0, MinSize: 0}
	ch, err := New(cfg)
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestNew_ParagraphStrategy(t *testing.T) {
	cfg := &Config{Strategy: "paragraph", ChunkSize: 500, Overlap: 0, MinSize: 0}
	ch, err := New(cfg)
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestNew_NilConfig_UsesDefaults(t *testing.T) {
	ch, err := New(nil)
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestNew_UnknownStrategy_ReturnsError(t *testing.T) {
	cfg := &Config{Strategy: "unsupported", ChunkSize: 50, Overlap: 0, MinSize: 0}
	_, err := New(cfg)
	require.Error(t, err)
	assert.IsType(t, &InvalidConfigError{}, err)
}
