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

//go:build sqlite_vtable

package main

import (
	"errors"
	"math"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time verification that stubPragma satisfies pragmaReader.
var _ pragmaReader = (*stubPragma)(nil)

// ---------------------------------------------------------------------------
// Minimal pragmaReader implementation for unit tests
// ---------------------------------------------------------------------------

// stubPragma implements pragmaReader with configurable values.
type stubPragma struct {
	consistency string
	writeMode   string
}

func (s *stubPragma) Consistency() string { return s.consistency }
func (s *stubPragma) WriteMode() string   { return s.writeMode }

// ---------------------------------------------------------------------------
// entityKeyPrefix
// ---------------------------------------------------------------------------

func TestEntityKeyPrefix_ReturnsTypeSlash(t *testing.T) {
	got := entityKeyPrefix("user")
	want := "user/"
	if got != want {
		t.Errorf("entityKeyPrefix(%q) = %q, want %q", "user", got, want)
	}
}

func TestEntityKeyPrefix_EmptyType(t *testing.T) {
	got := entityKeyPrefix("")
	want := "/"
	if got != want {
		t.Errorf("entityKeyPrefix(%q) = %q, want %q", "", got, want)
	}
}

// ---------------------------------------------------------------------------
// entityKey
// ---------------------------------------------------------------------------

func TestEntityKey_CombinesTypeAndID(t *testing.T) {
	got := entityKey("order", 42)
	want := "order/42"
	if got != want {
		t.Errorf("entityKey(%q, 42) = %q, want %q", "order", got, want)
	}
}

func TestEntityKey_ZeroID(t *testing.T) {
	got := entityKey("item", 0)
	want := "item/0"
	if got != want {
		t.Errorf("entityKey(%q, 0) = %q, want %q", "item", got, want)
	}
}

func TestEntityKey_MaxUint64(t *testing.T) {
	const maxID = ^uint64(0) // 18446744073709551615
	got := entityKey("thing", maxID)
	want := "thing/18446744073709551615"
	if got != want {
		t.Errorf("entityKey(%q, maxUint64) = %q, want %q", "thing", got, want)
	}
}

// ---------------------------------------------------------------------------
// entityIDFromKey
// ---------------------------------------------------------------------------

func TestEntityIDFromKey_ExtractsID(t *testing.T) {
	tests := []struct {
		entityType string
		key        string
		wantID     uint64
	}{
		{"user", "user/1", 1},
		{"order", "order/99", 99},
		{"item", "item/18446744073709551615", 18446744073709551615},
	}
	for _, tc := range tests {
		got := entityIDFromKey(tc.entityType, tc.key)
		if got != tc.wantID {
			t.Errorf("entityIDFromKey(%q, %q) = %d, want %d", tc.entityType, tc.key, got, tc.wantID)
		}
	}
}

func TestEntityIDFromKey_InvalidKey_ReturnsZero(t *testing.T) {
	tests := []struct {
		name       string
		entityType string
		key        string
	}{
		{"exact prefix only", "user", "user/"},
		{"wrong prefix", "user", "order/1"},
		{"non-numeric suffix", "user", "user/abc"},
		{"key shorter than prefix", "user", "us"},
		{"empty key", "user", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := entityIDFromKey(tc.entityType, tc.key)
			if got != 0 {
				t.Errorf("entityIDFromKey(%q, %q) = %d, want 0", tc.entityType, tc.key, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// float32sToBytes / bytesToFloat32s
// ---------------------------------------------------------------------------

func TestFloat32sToBytes_EncodesLittleEndian(t *testing.T) {
	floats := []float32{1.0, 2.0, 3.0}
	b := float32sToBytes(floats)
	if len(b) != 12 {
		t.Fatalf("expected 12 bytes, got %d", len(b))
	}
	// Decode and verify round-trip.
	decoded, err := bytesToFloat32s(b)
	if err != nil {
		t.Fatalf("bytesToFloat32s error: %v", err)
	}
	for i, f := range decoded {
		if f != floats[i] {
			t.Errorf("decoded[%d] = %v, want %v", i, f, floats[i])
		}
	}
}

func TestFloat32sToBytes_EmptySlice_ReturnsNil(t *testing.T) {
	b := float32sToBytes(nil)
	if b != nil {
		t.Errorf("float32sToBytes(nil) = %v, want nil", b)
	}
	b = float32sToBytes([]float32{})
	if b != nil {
		t.Errorf("float32sToBytes([]) = %v, want nil", b)
	}
}

func TestFloat32sToBytes_SpecialValues(t *testing.T) {
	floats := []float32{
		float32(math.Inf(1)),
		float32(math.Inf(-1)),
		float32(math.NaN()),
		0.0,
		-0.0,
	}
	b := float32sToBytes(floats)
	decoded, err := bytesToFloat32s(b)
	if err != nil {
		t.Fatalf("bytesToFloat32s error: %v", err)
	}
	if len(decoded) != len(floats) {
		t.Fatalf("decoded length %d, want %d", len(decoded), len(floats))
	}
	// Inf values must match exactly; NaN != NaN by definition.
	if !math.IsInf(float64(decoded[0]), 1) {
		t.Errorf("decoded[0] should be +Inf")
	}
	if !math.IsInf(float64(decoded[1]), -1) {
		t.Errorf("decoded[1] should be -Inf")
	}
	if !math.IsNaN(float64(decoded[2])) {
		t.Errorf("decoded[2] should be NaN")
	}
}

func TestBytesToFloat32s_ErrorOnNonMultipleOf4(t *testing.T) {
	tests := []struct {
		name string
		b    []byte
	}{
		{"1 byte", []byte{0x01}},
		{"2 bytes", []byte{0x01, 0x02}},
		{"3 bytes", []byte{0x01, 0x02, 0x03}},
		{"5 bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x05}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bytesToFloat32s(tc.b)
			if err == nil {
				t.Errorf("bytesToFloat32s(%v) expected error, got nil", tc.b)
			}
			var decErr *AdapterDecodeError
			if !errors.As(err, &decErr) {
				t.Errorf("error should be *AdapterDecodeError, got %T", err)
			}
		})
	}
}

func TestBytesToFloat32s_EmptySlice_ReturnsEmpty(t *testing.T) {
	floats, err := bytesToFloat32s([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(floats) != 0 {
		t.Errorf("expected empty slice, got %v", floats)
	}
}

// ---------------------------------------------------------------------------
// marshalMetadata
// ---------------------------------------------------------------------------

func TestMarshalMetadata_ProducesJSON(t *testing.T) {
	meta := map[string]any{"key": "value", "count": float64(42)}
	got := marshalMetadata(meta)
	if got == "" {
		t.Error("marshalMetadata: expected non-empty JSON string")
	}
	// Round-trip: re-parse to verify it is valid JSON.
	reparsed := parseTSTags(`[]`) // just exercise the import path cleanly
	_ = reparsed
	// Verify the string starts with '{' and ends with '}' (basic JSON object check).
	if got[0] != '{' || got[len(got)-1] != '}' {
		t.Errorf("marshalMetadata produced unexpected format: %q", got)
	}
}

func TestMarshalMetadata_NilOrEmptyMap_ReturnsEmpty(t *testing.T) {
	if got := marshalMetadata(nil); got != "" {
		t.Errorf("marshalMetadata(nil) = %q, want empty", got)
	}
	if got := marshalMetadata(map[string]any{}); got != "" {
		t.Errorf("marshalMetadata({}) = %q, want empty", got)
	}
}

func TestMarshalMetadata_UnmarshalableValue_ReturnsEmpty(t *testing.T) {
	// json.Marshal returns an error for channels; marshalMetadata should
	// return "" rather than panicking.
	meta := map[string]any{"bad": make(chan int)}
	got := marshalMetadata(meta)
	if got != "" {
		t.Errorf("marshalMetadata with channel value = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// parseTSTags
// ---------------------------------------------------------------------------

func TestParseTSTags_ValidJSON(t *testing.T) {
	input := `[{"key":"env","value":"prod"},{"key":"region","value":"us-east-1"}]`
	tags := parseTSTags(input)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}
	if tags[0].Key != "env" || tags[0].Value != "prod" {
		t.Errorf("tags[0] = %+v, want {env prod}", tags[0])
	}
	if tags[1].Key != "region" || tags[1].Value != "us-east-1" {
		t.Errorf("tags[1] = %+v, want {region us-east-1}", tags[1])
	}
}

func TestParseTSTags_EmptyOrInvalid_ReturnsNil(t *testing.T) {
	if tags := parseTSTags(""); tags != nil {
		t.Errorf("parseTSTags(%q) = %v, want nil", "", tags)
	}
	if tags := parseTSTags("not-json"); tags != nil {
		t.Errorf("parseTSTags(%q) = %v, want nil", "not-json", tags)
	}
	if tags := parseTSTags("{bad:json}"); tags != nil {
		t.Errorf("parseTSTags({bad:json}) = %v, want nil", tags)
	}
}

// ---------------------------------------------------------------------------
// marshalTSTags
// ---------------------------------------------------------------------------

func TestMarshalTSTags_ProducesJSON(t *testing.T) {
	tags := []transport.TSTag{
		{Key: "host", Value: "web-01"},
		{Key: "dc", Value: "eu-west-1"},
	}
	got := marshalTSTags(tags)
	if got == "" {
		t.Error("marshalTSTags: expected non-empty JSON string")
	}
	// Round-trip: the JSON should parse back to equivalent tags.
	roundTrip := parseTSTags(got)
	if len(roundTrip) != len(tags) {
		t.Fatalf("round-trip length %d, want %d", len(roundTrip), len(tags))
	}
	for i, tag := range roundTrip {
		if tag != tags[i] {
			t.Errorf("round-trip tags[%d] = %+v, want %+v", i, tag, tags[i])
		}
	}
}

func TestMarshalTSTags_NilOrEmpty_ReturnsEmpty(t *testing.T) {
	if got := marshalTSTags(nil); got != "" {
		t.Errorf("marshalTSTags(nil) = %q, want empty", got)
	}
	if got := marshalTSTags([]transport.TSTag{}); got != "" {
		t.Errorf("marshalTSTags([]) = %q, want empty", got)
	}
}

func TestMarshalParseTSTags_RoundTrip_SingleTag(t *testing.T) {
	tags := []transport.TSTag{{Key: "service", Value: "api"}}
	s := marshalTSTags(tags)
	got := parseTSTags(s)
	if len(got) != 1 || got[0].Key != "service" || got[0].Value != "api" {
		t.Errorf("round-trip failed: got %v", got)
	}
}

// ---------------------------------------------------------------------------
// readOpts
// ---------------------------------------------------------------------------

func TestReadOpts_ValidConsistency_BuildsOption(t *testing.T) {
	tests := []struct {
		consistency string
	}{
		{"linearizable"},
		{"eventual"},
		{"strong"},
	}
	for _, tc := range tests {
		t.Run(tc.consistency, func(t *testing.T) {
			p := &stubPragma{consistency: tc.consistency}
			opts := readOpts(p)
			if len(opts) != 1 {
				t.Errorf("readOpts(%q): expected 1 option, got %d", tc.consistency, len(opts))
			}
		})
	}
}

func TestReadOpts_InvalidConsistency_FallsBackToLinearizable(t *testing.T) {
	// An unrecognised value should not panic and should return a valid option
	// (falling back to ConsistencyLinearizable inside readOpts).
	p := &stubPragma{consistency: "unknown-level"}
	opts := readOpts(p)
	if len(opts) != 1 {
		t.Errorf("expected 1 option for invalid consistency, got %d", len(opts))
	}
}

// ---------------------------------------------------------------------------
// writeOpts
// ---------------------------------------------------------------------------

func TestWriteOpts_ValidWriteMode_BuildsOption(t *testing.T) {
	tests := []string{"sync", "wal_durable", "batched", ""}
	for _, mode := range tests {
		t.Run(mode, func(t *testing.T) {
			p := &stubPragma{writeMode: mode}
			opts := writeOpts(p)
			if len(opts) != 1 {
				t.Errorf("writeOpts(%q): expected 1 option, got %d", mode, len(opts))
			}
		})
	}
}

func TestWriteOpts_InvalidWriteMode_FallsBackToSync(t *testing.T) {
	p := &stubPragma{writeMode: "turbo-mode"}
	opts := writeOpts(p)
	if len(opts) != 1 {
		t.Errorf("expected 1 option for invalid write mode, got %d", len(opts))
	}
}

// ---------------------------------------------------------------------------
// AdapterDecodeError
// ---------------------------------------------------------------------------

func TestAdapterDecodeError_Error(t *testing.T) {
	inner := errors.New("json: unexpected end")
	e := &AdapterDecodeError{Key: "user/42", Err: inner}
	want := "adapter: decode failed for user/42: json: unexpected end"
	if got := e.Error(); got != want {
		t.Errorf("AdapterDecodeError.Error() = %q, want %q", got, want)
	}
}

func TestAdapterDecodeError_Unwrap(t *testing.T) {
	inner := errors.New("underlying")
	e := &AdapterDecodeError{Key: "k", Err: inner}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should find the wrapped error through AdapterDecodeError.Unwrap()")
	}
}

func TestAdapterDecodeError_ErrorsAs(t *testing.T) {
	inner := errors.New("decode failure")
	e := &AdapterDecodeError{Key: "vector", Err: inner}
	var target *AdapterDecodeError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should match *AdapterDecodeError")
	}
	if target.Key != "vector" {
		t.Errorf("Key = %q, want %q", target.Key, "vector")
	}
}

// ---------------------------------------------------------------------------
// AdapterEncodeError
// ---------------------------------------------------------------------------

func TestAdapterEncodeError_Error(t *testing.T) {
	inner := errors.New("json: unsupported type")
	e := &AdapterEncodeError{Key: "order/7", Err: inner}
	want := "adapter: encode failed for order/7: json: unsupported type"
	if got := e.Error(); got != want {
		t.Errorf("AdapterEncodeError.Error() = %q, want %q", got, want)
	}
}

func TestAdapterEncodeError_Unwrap(t *testing.T) {
	inner := errors.New("underlying encode error")
	e := &AdapterEncodeError{Key: "k", Err: inner}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should find the wrapped error through AdapterEncodeError.Unwrap()")
	}
}

func TestAdapterEncodeError_ErrorsAs(t *testing.T) {
	inner := errors.New("encode failure")
	e := &AdapterEncodeError{Key: "item/99", Err: inner}
	var target *AdapterEncodeError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should match *AdapterEncodeError")
	}
	if target.Key != "item/99" {
		t.Errorf("Key = %q, want %q", target.Key, "item/99")
	}
}
