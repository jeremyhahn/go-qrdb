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
	"errors"
	"strconv"
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

// nestedDocument returns a document whose maps nest levels deep, the
// top-level map included.
func nestedDocument(levels int) map[string]any {
	doc := map[string]any{"leaf": "v"}
	for i := 1; i < levels; i++ {
		doc = map[string]any{"n": doc}
	}
	return doc
}

// documentDepth counts the nested maps of a decoded nestedDocument.
func documentDepth(t *testing.T, doc map[string]any) int {
	t.Helper()
	depth := 1
	for {
		next, ok := doc["n"]
		if !ok {
			assert.Equal(t, "v", doc["leaf"])
			return depth
		}
		m, ok := next.(map[string]any)
		require.True(t, ok, "nested level decoded as %T", next)
		doc = m
		depth++
	}
}

func TestCBORDocumentCodec_NestingAtLimitRoundTrips(t *testing.T) {
	c, err := NewDocumentCodec(CodecCBOR)
	require.NoError(t, err)

	data, err := c.Marshal(nestedDocument(CBORMaxNestedLevels))
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, c.Unmarshal(data, &out))
	assert.Equal(t, CBORMaxNestedLevels, documentDepth(t, out))
}

func TestCBORDocumentCodec_NestingPastLimitRefusedOnEncode(t *testing.T) {
	c, err := NewDocumentCodec(CodecCBOR)
	require.NoError(t, err)

	data, err := c.Marshal(nestedDocument(CBORMaxNestedLevels + 1))
	require.Error(t, err)
	assert.Nil(t, data, "a refused document yields no bytes to store")
	var limitErr CBORLimitError
	assert.True(t, errors.As(err, &limitErr), "error is %T: %v", err, err)
}

// TestCBORDocumentCodec_NonStringNestedMapKeyRefusedOnEncode proves a
// document the document decoder cannot read back (a nested map with integer
// keys, which does not decode as map[string]any) is refused at encode time,
// while a string-keyed nested map round-trips.
func TestCBORDocumentCodec_NonStringNestedMapKeyRefusedOnEncode(t *testing.T) {
	c, err := NewDocumentCodec(CodecCBOR)
	require.NoError(t, err)

	data, err := c.Marshal(map[string]any{"nested": map[int]string{1: "one"}})
	require.Error(t, err)
	assert.Nil(t, data, "a refused document yields no bytes to store")
	var shapeErr CBORDecodeShapeError
	assert.True(t, errors.As(err, &shapeErr), "error is %T: %v", err, err)

	data, err = c.Marshal(map[string]any{"nested": map[string]any{"a": 1}})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, c.Unmarshal(data, &got))
	nested, ok := got["nested"].(map[string]any)
	require.True(t, ok, "nested is %T", got["nested"])
	assert.EqualValues(t, 1, nested["a"])
}

func TestCBORDocumentCodec_LargeArrayAndMapRoundTrip(t *testing.T) {
	c, err := NewDocumentCodec(CodecCBOR)
	require.NoError(t, err)

	// One past fxamacker's default limit of 131072 elements and pairs.
	const n = 128*1024 + 1
	items := make([]any, n)
	for i := range items {
		items[i] = uint64(i)
	}
	fields := make(map[string]any, n)
	for i := range n {
		fields["f"+strconv.Itoa(i)] = true
	}
	data, err := c.Marshal(map[string]any{"items": items, "fields": fields})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, c.Unmarshal(data, &out))
	gotItems, ok := out["items"].([]any)
	require.True(t, ok, "items decoded as %T", out["items"])
	assert.Len(t, gotItems, n)
	gotFields, ok := out["fields"].(map[string]any)
	require.True(t, ok, "fields decoded as %T", out["fields"])
	assert.Len(t, gotFields, n)
}

func TestCBORCodec_TypedEntitySharesLimits(t *testing.T) {
	c, err := NewCodec[cborAnyEntity](CodecCBOR)
	require.NoError(t, err)

	_, err = c.Marshal(cborAnyEntity{Extra: nestedDocument(CBORMaxNestedLevels + 1)})
	var limitErr CBORLimitError
	assert.True(t, errors.As(err, &limitErr), "error is %T: %v", err, err)
}
