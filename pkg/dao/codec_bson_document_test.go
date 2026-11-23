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

package dao

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBSONCodec_DocumentDecodesPlainTypes proves a BSON document decodes
// arrays as []any and objects, including one inside an array, as
// map[string]any.
func TestBSONCodec_DocumentDecodesPlainTypes(t *testing.T) {
	c, err := NewCodec[map[string]any](CodecBSON)
	require.NoError(t, err)

	data, err := c.Marshal(map[string]any{
		"profile": map[string]any{"name": "a"},
		"items":   []any{map[string]any{"k": "v"}, []any{"x"}, 1.5},
	})
	require.NoError(t, err)

	check := func(t *testing.T, out map[string]any) {
		profile, ok := out["profile"].(map[string]any)
		require.True(t, ok, "nested object decoded as %T", out["profile"])
		assert.Equal(t, "a", profile["name"])

		items, ok := out["items"].([]any)
		require.True(t, ok, "array decoded as %T", out["items"])
		require.Len(t, items, 3)
		item, ok := items[0].(map[string]any)
		require.True(t, ok, "object in array decoded as %T", items[0])
		assert.Equal(t, "v", item["k"])
		assert.IsType(t, []any{}, items[1], "array in array")
		assert.InDelta(t, 1.5, items[2], 0)
	}

	var out map[string]any
	require.NoError(t, c.Unmarshal(data, &out))
	check(t, out)
}

// TestBSONCodec_TypedEntityUnchanged proves the document normalisation
// leaves a typed entity's decode alone.
func TestBSONCodec_TypedEntityUnchanged(t *testing.T) {
	type entity struct {
		Name string   `bson:"name"`
		Tags []string `bson:"tags"`
	}
	c, err := NewCodec[entity](CodecBSON)
	require.NoError(t, err)
	data, err := c.Marshal(entity{Name: "a", Tags: []string{"x", "y"}})
	require.NoError(t, err)
	var out entity
	require.NoError(t, c.Unmarshal(data, &out))
	assert.Equal(t, entity{Name: "a", Tags: []string{"x", "y"}}, out)
}
