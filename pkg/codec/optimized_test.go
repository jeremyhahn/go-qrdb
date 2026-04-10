// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build codec_msgpack

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptimizedCodec_RoundTrip_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	assert.Equal(t, TypeJSON, oc.Type())

	original := samplePayload()
	data, err := oc.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = oc.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_RoundTrip_MsgPack(t *testing.T) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	assert.Equal(t, TypeMsgPack, oc.Type())

	original := samplePayload()
	data, err := oc.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = oc.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_MarshalError(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	_, err := oc.Marshal(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
}

func TestOptimizedCodec_UnmarshalError(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	var out testPayload
	err := oc.Unmarshal([]byte("bad"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}

func TestOptimizedCodec_UnmarshalNilTarget(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	err := oc.Unmarshal([]byte(`{}`), nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestOptimizedCodec_UnmarshalEmptyData(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	var out testPayload
	err := oc.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

func TestOptimizedCodec_MarshalTo(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	buf := make([]byte, 0, 1024)
	result, err := oc.MarshalTo(original, buf)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var decoded testPayload
	err = oc.Unmarshal(result, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_MarshalTo_GrowsBuffer(t *testing.T) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	original := samplePayload()

	// Start with a tiny buffer that will need to grow.
	buf := make([]byte, 0, 1)
	result, err := oc.MarshalTo(original, buf)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var decoded testPayload
	err = oc.Unmarshal(result, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_MarshalTo_Error(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	buf := make([]byte, 0, 64)
	result, err := oc.MarshalTo(make(chan int), buf)
	require.Error(t, err)
	// Buffer should be returned unchanged on error.
	assert.Empty(t, result)
}

func TestOptimizedCodec_Inner(t *testing.T) {
	inner := &MsgPackCodec{}
	oc := NewOptimizedCodec(inner)
	assert.Equal(t, inner, oc.Inner())
}

func TestOptimizedCodec_PoolReuse(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	// Marshal multiple times to exercise pool put/get paths.
	for i := 0; i < 100; i++ {
		data, err := oc.Marshal(original)
		require.NoError(t, err)

		var decoded testPayload
		err = oc.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, original, decoded)
	}
}

func TestOptimizedCodec_LargePayload(t *testing.T) {
	oc := NewOptimizedCodec(&MsgPackCodec{})

	// Create a payload larger than the default pool buffer size.
	tags := make([]string, 500)
	for i := range tags {
		tags[i] = "long-tag-name-for-testing-buffer-growth"
	}
	original := testPayload{
		Name:  "large-optimized",
		Value: 999999,
		Tags:  tags,
	}

	data, err := oc.Marshal(original)
	require.NoError(t, err)

	var decoded testPayload
	err = oc.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original.Name, decoded.Name)
	assert.Len(t, decoded.Tags, 500)
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkJSONCodec_Marshal(b *testing.B) {
	c := &JSONCodec{}
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = c.Marshal(payload)
	}
}

func BenchmarkMsgPackCodec_Marshal(b *testing.B) {
	c := &MsgPackCodec{}
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = c.Marshal(payload)
	}
}

func BenchmarkOptimizedJSON_Marshal(b *testing.B) {
	oc := NewOptimizedCodec(&JSONCodec{})
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = oc.Marshal(payload)
	}
}

func BenchmarkOptimizedMsgPack_Marshal(b *testing.B) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = oc.Marshal(payload)
	}
}

func BenchmarkJSONCodec_Unmarshal(b *testing.B) {
	c := &JSONCodec{}
	payload := samplePayload()
	data, _ := c.Marshal(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var out testPayload
		_ = c.Unmarshal(data, &out)
	}
}

func BenchmarkMsgPackCodec_Unmarshal(b *testing.B) {
	c := &MsgPackCodec{}
	payload := samplePayload()
	data, _ := c.Marshal(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var out testPayload
		_ = c.Unmarshal(data, &out)
	}
}

func BenchmarkOptimizedMsgPack_MarshalTo(b *testing.B) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	payload := samplePayload()
	buf := make([]byte, 0, 1024)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf, _ = oc.MarshalTo(payload, buf[:0])
	}
}

func BenchmarkManager_Transcode(b *testing.B) {
	m := NewManager(
		NewOptimizedCodec(&MsgPackCodec{}),
		NewOptimizedCodec(&JSONCodec{}),
	)
	payload := samplePayload()
	data, _ := m.EncodeForStorage(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var intermediate testPayload
		_, _ = m.Transcode(data, &intermediate)
	}
}
