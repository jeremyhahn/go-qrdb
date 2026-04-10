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

package index

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeCompoundValue_StringFields(t *testing.T) {
	values := [][]byte{[]byte("hello"), []byte("world")}
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 2)
	assert.Equal(t, []byte("hello"), decoded[0])
	assert.Equal(t, []byte("world"), decoded[1])
}

func TestEncodeDecodeCompoundValue_NumericFields(t *testing.T) {
	// Pre-encode numeric values as 8-byte big-endian.
	val1 := mustEncodeNumeric(t, "42")
	val2 := mustEncodeNumeric(t, "100")
	values := [][]byte{val1, val2}
	types := []IndexValueType{IndexValueTypeNumeric, IndexValueTypeNumeric}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 2)
	assert.Equal(t, val1, decoded[0])
	assert.Equal(t, val2, decoded[1])
}

func TestEncodeDecodeCompoundValue_MixedFields(t *testing.T) {
	strVal := []byte("conversation-123")
	numVal := mustEncodeNumeric(t, "1709000000")
	values := [][]byte{strVal, numVal}
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeNumeric}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 2)
	assert.Equal(t, strVal, decoded[0])
	assert.Equal(t, numVal, decoded[1])
}

func TestEncodeDecodeCompoundValue_EmptyString(t *testing.T) {
	values := [][]byte{[]byte(""), []byte("second")}
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 2)
	assert.Equal(t, []byte{}, decoded[0])
	assert.Equal(t, []byte("second"), decoded[1])
}

func TestEncodeDecodeCompoundValue_NullBytesInString(t *testing.T) {
	// String containing \x00 bytes must survive encoding/decoding.
	values := [][]byte{{0x41, 0x00, 0x42}, []byte("normal")}
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 2)
	assert.Equal(t, []byte{0x41, 0x00, 0x42}, decoded[0])
	assert.Equal(t, []byte("normal"), decoded[1])
}

func TestEncodeCompoundValue_LexicographicOrdering_Strings(t *testing.T) {
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}

	// ("alice", "2024") should sort before ("bob", "2023")
	enc1 := EncodeCompoundValue([][]byte{[]byte("alice"), []byte("2024")}, types)
	enc2 := EncodeCompoundValue([][]byte{[]byte("bob"), []byte("2023")}, types)
	assert.True(t, bytes.Compare(enc1, enc2) < 0, "alice should sort before bob")

	// Same first field, different second: ("alice", "aaa") < ("alice", "bbb")
	enc3 := EncodeCompoundValue([][]byte{[]byte("alice"), []byte("aaa")}, types)
	enc4 := EncodeCompoundValue([][]byte{[]byte("alice"), []byte("bbb")}, types)
	assert.True(t, bytes.Compare(enc3, enc4) < 0, "aaa should sort before bbb within alice")
}

func TestEncodeCompoundValue_LexicographicOrdering_Numeric(t *testing.T) {
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeNumeric}

	// ("conv-1", 100) < ("conv-1", 200)
	enc1 := EncodeCompoundValue([][]byte{[]byte("conv-1"), mustEncodeNumeric(t, "100")}, types)
	enc2 := EncodeCompoundValue([][]byte{[]byte("conv-1"), mustEncodeNumeric(t, "200")}, types)
	assert.True(t, bytes.Compare(enc1, enc2) < 0, "100 should sort before 200")

	// ("conv-1", 200) < ("conv-2", 100) -- first field takes precedence
	enc3 := EncodeCompoundValue([][]byte{[]byte("conv-1"), mustEncodeNumeric(t, "200")}, types)
	enc4 := EncodeCompoundValue([][]byte{[]byte("conv-2"), mustEncodeNumeric(t, "100")}, types)
	assert.True(t, bytes.Compare(enc3, enc4) < 0, "conv-1 should sort before conv-2")
}

func TestEncodeCompoundPrefix_IsPrefix(t *testing.T) {
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}

	// Encoding a prefix of the compound should be a byte-prefix of the full compound.
	prefix := EncodeCompoundPrefix([][]byte{[]byte("conv-1")}, types[:1])
	full := EncodeCompoundValue([][]byte{[]byte("conv-1"), []byte("msg-42")}, types)

	assert.True(t, bytes.HasPrefix(full, prefix), "full compound should start with prefix")
}

func TestEncodeDecodeCompoundValue_ThreeFields(t *testing.T) {
	values := [][]byte{[]byte("queue-send"), []byte("0"), mustEncodeNumeric(t, "1709000000")}
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString, IndexValueTypeNumeric}

	encoded := EncodeCompoundValue(values, types)
	decoded := DecodeCompoundValue(encoded, types)

	require.Len(t, decoded, 3)
	assert.Equal(t, []byte("queue-send"), decoded[0])
	assert.Equal(t, []byte("0"), decoded[1])
	assert.Equal(t, values[2], decoded[2])
}

func TestDecodeCompoundValue_TruncatedData(t *testing.T) {
	// Decoding truncated data should not panic; returns partial results.
	types := []IndexValueType{IndexValueTypeString, IndexValueTypeString}
	// Only encode one field but request two.
	partial := EncodeCompoundValue([][]byte{[]byte("only-one")}, types[:1])
	decoded := DecodeCompoundValue(partial, types)

	require.Len(t, decoded, 1)
	assert.Equal(t, []byte("only-one"), decoded[0])
}

// mustEncodeNumeric encodes a numeric string using EncodeNumericValue
// and fails the test on error.
func mustEncodeNumeric(t *testing.T, s string) []byte {
	t.Helper()
	b, err := EncodeNumericValue(s)
	require.NoError(t, err)
	return b
}
