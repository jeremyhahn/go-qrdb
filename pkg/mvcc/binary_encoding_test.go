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

package mvcc

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func TestEncodePutBinary(t *testing.T) {
	key := []byte("test-key")
	value := []byte("test-value")

	encoded := encodePutBinary(key, value)

	// Verify opcode
	assert.Equal(t, protocol.OpFSMPut, encoded[0])

	// Verify key length
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(len(key)), keyLen)

	// Verify key
	actualKey := encoded[3 : 3+keyLen]
	assert.Equal(t, key, actualKey)

	// Verify value length
	valueLen := binary.BigEndian.Uint32(encoded[3+keyLen : 7+keyLen])
	assert.Equal(t, uint32(len(value)), valueLen)

	// Verify value
	actualValue := encoded[7+keyLen : 7+int(keyLen)+int(valueLen)]
	assert.Equal(t, value, actualValue)

	// Verify total length
	expectedLen := 1 + 2 + len(key) + 4 + len(value)
	assert.Equal(t, expectedLen, len(encoded))
}

func TestEncodeDeleteBinary(t *testing.T) {
	key := []byte("test-key")

	encoded := encodeDeleteBinary(key)

	// Verify opcode
	assert.Equal(t, protocol.OpFSMDelete, encoded[0])

	// Verify key length
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(len(key)), keyLen)

	// Verify key
	actualKey := encoded[3 : 3+keyLen]
	assert.Equal(t, key, actualKey)

	// Verify total length
	expectedLen := 1 + 2 + len(key)
	assert.Equal(t, expectedLen, len(encoded))
}

func TestEncodeBatchBinary(t *testing.T) {
	// Create some operations
	put1 := encodePutBinary([]byte("key1"), []byte("value1"))
	put2 := encodePutBinary([]byte("key2"), []byte("value2"))
	del1 := encodeDeleteBinary([]byte("key3"))

	operations := [][]byte{put1, put2, del1}

	encoded := encodeBatchBinary(operations)

	// Verify batch opcode
	assert.Equal(t, protocol.OpFSMBatch, encoded[0])

	// Verify total length
	expectedLen := 1 + len(put1) + len(put2) + len(del1)
	assert.Equal(t, expectedLen, len(encoded))

	// Verify operations are concatenated correctly
	offset := 1
	for _, op := range operations {
		assert.Equal(t, op, encoded[offset:offset+len(op)])
		offset += len(op)
	}
}

func TestEncodePutBinary_EmptyKey(t *testing.T) {
	key := []byte{}
	value := []byte("value")

	encoded := encodePutBinary(key, value)

	assert.Equal(t, protocol.OpFSMPut, encoded[0])
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(0), keyLen)
}

func TestEncodePutBinary_EmptyValue(t *testing.T) {
	key := []byte("key")
	value := []byte{}

	encoded := encodePutBinary(key, value)

	assert.Equal(t, protocol.OpFSMPut, encoded[0])

	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(len(key)), keyLen)

	valueLen := binary.BigEndian.Uint32(encoded[3+keyLen : 7+keyLen])
	assert.Equal(t, uint32(0), valueLen)
}

func TestEncodeDeleteBinary_EmptyKey(t *testing.T) {
	key := []byte{}

	encoded := encodeDeleteBinary(key)

	assert.Equal(t, protocol.OpFSMDelete, encoded[0])
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(0), keyLen)
}

func TestEncodeBatchBinary_EmptyOperations(t *testing.T) {
	operations := [][]byte{}

	encoded := encodeBatchBinary(operations)

	// Should just have batch opcode
	assert.Equal(t, 1, len(encoded))
	assert.Equal(t, protocol.OpFSMBatch, encoded[0])
}

func TestEncodeBatchBinary_SingleOperation(t *testing.T) {
	put := encodePutBinary([]byte("key"), []byte("value"))
	operations := [][]byte{put}

	encoded := encodeBatchBinary(operations)

	assert.Equal(t, protocol.OpFSMBatch, encoded[0])
	assert.Equal(t, put, encoded[1:])
}

func TestEncodePutBinary_LargeKey(t *testing.T) {
	// Test with large key
	key := make([]byte, 10000)
	for i := range key {
		key[i] = byte(i % 256)
	}
	value := []byte("value")

	encoded := encodePutBinary(key, value)

	assert.Equal(t, protocol.OpFSMPut, encoded[0])
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	assert.Equal(t, uint16(len(key)), keyLen)
	assert.Equal(t, key, encoded[3:3+keyLen])
}

func TestEncodePutBinary_LargeValue(t *testing.T) {
	key := []byte("key")
	// Test with large value
	value := make([]byte, 100000)
	for i := range value {
		value[i] = byte(i % 256)
	}

	encoded := encodePutBinary(key, value)

	assert.Equal(t, protocol.OpFSMPut, encoded[0])
	keyLen := binary.BigEndian.Uint16(encoded[1:3])
	valueLen := binary.BigEndian.Uint32(encoded[3+keyLen : 7+keyLen])
	assert.Equal(t, uint32(len(value)), valueLen)
	assert.Equal(t, value, encoded[7+int(keyLen):7+int(keyLen)+int(valueLen)])
}

func TestGetBuffer(t *testing.T) {
	// Test small buffer (should use pool)
	buf := getBuffer(1024)
	assert.NotNil(t, buf)
	assert.Equal(t, 1024, len(buf))
	assert.GreaterOrEqual(t, cap(buf), 1024)

	// Test buffer at pool size
	buf2 := getBuffer(4096)
	assert.NotNil(t, buf2)
	assert.Equal(t, 4096, len(buf2))

	// Test large buffer (should allocate directly)
	buf3 := getBuffer(10000)
	assert.NotNil(t, buf3)
	assert.Equal(t, 10000, len(buf3))
}

func TestPutBuffer(t *testing.T) {
	// Get and return small buffer
	buf := getBuffer(1024)
	assert.NotNil(t, buf)
	putBuffer(buf) // Should return to pool

	// Get another buffer - might be the same one
	buf2 := getBuffer(1024)
	assert.NotNil(t, buf2)
	putBuffer(buf2)

	// Test with buffer that's too large for pool
	largeBuf := make([]byte, 10000)
	putBuffer(largeBuf) // Should not panic, just not pool it
}

func TestBufferPoolRoundTrip(t *testing.T) {
	// Get buffer, use it, return it
	buf := getBuffer(2048)
	require.NotNil(t, buf)

	// Write some data
	copy(buf, []byte("test data"))

	// Return to pool
	putBuffer(buf)

	// Get another buffer
	buf2 := getBuffer(2048)
	require.NotNil(t, buf2)

	// Should be valid buffer
	assert.GreaterOrEqual(t, cap(buf2), 2048)
}

func TestEncodeBatchBinary_MixedOperations(t *testing.T) {
	// Create mixed operations
	operations := [][]byte{
		encodePutBinary([]byte("key1"), []byte("value1")),
		encodeDeleteBinary([]byte("key2")),
		encodePutBinary([]byte("key3"), []byte("value3")),
		encodeDeleteBinary([]byte("key4")),
		encodePutBinary([]byte("key5"), []byte("value5")),
	}

	encoded := encodeBatchBinary(operations)

	// Verify batch opcode
	assert.Equal(t, protocol.OpFSMBatch, encoded[0])

	// Verify each operation is present
	offset := 1
	for _, op := range operations {
		opData := encoded[offset : offset+len(op)]
		assert.Equal(t, op, opData)
		offset += len(op)
	}

	// Verify we consumed the entire buffer
	assert.Equal(t, len(encoded), offset)
}

func TestOpcodeConstants(t *testing.T) {
	// Verify opcode values haven't changed
	assert.Equal(t, byte(0x01), protocol.OpFSMPut)
	assert.Equal(t, byte(0x02), protocol.OpFSMDelete)
	assert.Equal(t, byte(0x03), protocol.OpFSMBatch)
}

func TestGetBuffer_PooledBufferTooSmall(t *testing.T) {
	// Inject a tiny buffer into the pool so the next getBuffer call
	// retrieves it but finds cap(buf) < requested size.
	tinyBuf := make([]byte, 10)
	smallBufferPool.Put(&tinyBuf)

	// Request a buffer larger than 10 bytes but within pool range (<= 4096).
	// getBuffer will get our tiny buffer, find cap(10) < 100, put it back,
	// and allocate a new buffer.
	result := getBuffer(100)
	assert.NotNil(t, result)
	assert.Len(t, result, 100)
}

func BenchmarkEncodePutBinary(b *testing.B) {
	key := []byte("benchmark-key")
	value := []byte("benchmark-value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = encodePutBinary(key, value)
	}
}

func BenchmarkEncodeDeleteBinary(b *testing.B) {
	key := []byte("benchmark-key")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = encodeDeleteBinary(key)
	}
}

func BenchmarkEncodeBatchBinary(b *testing.B) {
	operations := [][]byte{
		encodePutBinary([]byte("key1"), []byte("value1")),
		encodePutBinary([]byte("key2"), []byte("value2")),
		encodeDeleteBinary([]byte("key3")),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = encodeBatchBinary(operations)
	}
}
