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

//go:build codec_cbor

package dao

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCBORCodec_NestedObjectsDecodeAsStringKeyedMaps proves a CBOR document
// decodes every nested object, including one inside an array, as
// map[string]any, so it encodes back to JSON like any other document.
func TestCBORCodec_NestedObjectsDecodeAsStringKeyedMaps(t *testing.T) {
	c, err := NewDocumentCodec(CodecCBOR)
	require.NoError(t, err)

	data, err := c.Marshal(map[string]any{
		"profile": map[string]any{"name": "a"},
		"items":   []any{map[string]any{"k": "v"}, 1.5},
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, c.Unmarshal(data, &out))

	profile, ok := out["profile"].(map[string]any)
	require.True(t, ok, "nested object decoded as %T", out["profile"])
	assert.Equal(t, "a", profile["name"])

	items, ok := out["items"].([]any)
	require.True(t, ok, "array decoded as %T", out["items"])
	require.Len(t, items, 2)
	item, ok := items[0].(map[string]any)
	require.True(t, ok, "object in array decoded as %T", items[0])
	assert.Equal(t, "v", item["k"])
}

// cborAnyEntity is a typed entity whose any field may hold any CBOR value.
type cborAnyEntity struct {
	Name  string `cbor:"name"`
	Extra any    `cbor:"extra"`
}

// TestCBORCodec_TypedEntityKeepsDefaultMapType proves a typed DAO keeps
// fxamacker's default map type: an integer-keyed map in an any field round
// trips as map[any]any instead of failing to decode into map[string]any.
func TestCBORCodec_TypedEntityKeepsDefaultMapType(t *testing.T) {
	c, err := NewCodec[cborAnyEntity](CodecCBOR)
	require.NoError(t, err)

	data, err := c.Marshal(cborAnyEntity{Name: "a", Extra: map[any]any{uint64(1): "one", uint64(2): "two"}})
	require.NoError(t, err)

	var out cborAnyEntity
	require.NoError(t, c.Unmarshal(data, &out))
	assert.Equal(t, "a", out.Name)
	extra, ok := out.Extra.(map[any]any)
	require.True(t, ok, "integer-keyed map decoded as %T", out.Extra)
	assert.Equal(t, "one", extra[uint64(1)])
	assert.Equal(t, "two", extra[uint64(2)])
}
