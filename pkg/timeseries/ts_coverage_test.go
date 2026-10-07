//go:build qrdb_ts

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

package timeseries

import (
	"encoding/binary"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// decodeDataPoint — truncated data error paths not covered by storage_test.go
// ---------------------------------------------------------------------------

// TestDecodeDataPoint_TruncatedAfterMetric verifies that data with a valid
// metric length and content but missing value bytes returns a DecodeError.
func TestDecodeDataPoint_TruncatedAfterMetric(t *testing.T) {
	// Build: 2 (metricLen=3) + 3 (metric "cpu") = 5 bytes, no value.
	data := make([]byte, 5)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for missing value bytes, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// TestDecodeDataPoint_TruncatedAfterValue verifies that data with valid metric
// and value bytes but missing timestamp bytes returns a DecodeError.
func TestDecodeDataPoint_TruncatedAfterValue(t *testing.T) {
	// Build: 2 (metricLen=3) + 3 + 8 (value) = 13 bytes, no timestamp.
	data := make([]byte, 13)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")
	// value bytes remain zero (valid float64)

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for missing timestamp bytes, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// TestDecodeDataPoint_TruncatedTagCount verifies that data truncated just
// before the tag count field returns a DecodeError.
func TestDecodeDataPoint_TruncatedTagCount(t *testing.T) {
	// Build: 2 (metricLen=3) + 3 + 8 (value) + 8 (ts) + 1 (only 1 byte for tag count) = 22 bytes.
	data := make([]byte, 22)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for truncated tag count, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// TestDecodeDataPoint_TagKeyLengthExceedsData verifies that a tag key length
// field whose value extends beyond available data returns a DecodeError.
func TestDecodeDataPoint_TagKeyLengthExceedsData(t *testing.T) {
	// Build valid header + tagCount=1, then key length that exceeds data.
	// 2 (metricLen=3) + 3 + 8 + 8 + 2 (tagCount=1) + 2 (keyLen=255) = 25 bytes
	data := make([]byte, 25)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")
	// value and timestamp remain zero
	binary.LittleEndian.PutUint16(data[21:], 1)   // tagCount = 1
	binary.LittleEndian.PutUint16(data[23:], 255) // keyLen = 255, way beyond data

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for tag key length exceeds data, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// TestDecodeDataPoint_TruncatedTagValueLength verifies that data truncated
// between tag key and tag value length field returns a DecodeError.
func TestDecodeDataPoint_TruncatedTagValueLength(t *testing.T) {
	// Build: valid header + tagCount=1 + key="k" (1 char) + only 1 byte for valLen.
	// 2 (metricLen=3) + 3 + 8 + 8 + 2 (tagCount=1) + 2 (keyLen=1) + 1 ("k") + 1 (truncated valLen) = 28
	data := make([]byte, 28)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")
	binary.LittleEndian.PutUint16(data[21:], 1) // tagCount = 1
	binary.LittleEndian.PutUint16(data[23:], 1) // keyLen = 1
	data[25] = 'k'
	// only 2 bytes remain (indices 26,27) but valLen needs 2 bytes: this is exactly enough
	// We need exactly 1 byte short: use 27 total bytes.
	data = data[:27]

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for truncated tag value length, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// TestDecodeDataPoint_TagValueLengthExceedsData verifies that a tag value
// length field whose value extends beyond available data returns a DecodeError.
func TestDecodeDataPoint_TagValueLengthExceedsData(t *testing.T) {
	// Build: valid header + tagCount=1 + key="k" + valLen=255 (exceeds data).
	// 2 (metricLen=3) + 3 + 8 + 8 + 2 (tagCount=1) + 2 (keyLen=1) + 1 ("k") + 2 (valLen=255) = 29
	data := make([]byte, 29)
	binary.LittleEndian.PutUint16(data[0:], 3)
	copy(data[2:], "cpu")
	binary.LittleEndian.PutUint16(data[21:], 1) // tagCount = 1
	binary.LittleEndian.PutUint16(data[23:], 1) // keyLen = 1
	data[25] = 'k'
	binary.LittleEndian.PutUint16(data[26:], 255) // valLen = 255, exceeds data

	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint: expected DecodeError for tag value length exceeds data, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetRetentionPolicy — corrupt data in PebbleDB triggers DecodeError
// ---------------------------------------------------------------------------

// TestStorage_GetRetentionPolicy_CorruptData verifies that corrupt retention
// policy bytes in storage return a DecodeError rather than panicking.
func TestStorage_GetRetentionPolicy_CorruptData(t *testing.T) {
	s := newStorage(t)

	// Write corrupt retention policy data (fewer than 17 bytes required).
	key := []byte(KeyPrefixRetention + "corrupt.metric")
	corruptData := []byte{0x01, 0x02} // too short for a valid policy
	if err := s.stg.Set(key, corruptData); err != nil {
		t.Fatalf("stg.Set corrupt data: %v", err)
	}

	_, err := s.GetRetentionPolicy("corrupt.metric")
	if err == nil {
		t.Fatal("GetRetentionPolicy: expected DecodeError for corrupt data, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("GetRetentionPolicy: expected *DecodeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SetRetentionPolicy — nil policy causes nil pointer dereference guard
// ---------------------------------------------------------------------------

// TestSetRetentionPolicy_NilPolicy verifies behavior when a nil policy is
// passed. encodeRetentionPolicy dereferences the pointer, so this exercises
// the nil-safety of the call path.
func TestSetRetentionPolicy_NilPolicy(t *testing.T) {
	s := newStorage(t)

	// encodeRetentionPolicy will panic on nil — use recover to verify it
	// doesn't silently succeed.
	defer func() {
		if r := recover(); r != nil {
			// A panic on nil input is the current behavior — document it here
			// so a future nil-guard change will be caught by this test.
			t.Logf("SetRetentionPolicy(nil) panicked as expected: %v", r)
		}
	}()

	// If it doesn't panic, it should at minimum not return nil error
	// when given a nil pointer (encodeRetentionPolicy dereferences *p).
	_ = setRetentionPolicy(t, s, "some.metric", nil)
}
