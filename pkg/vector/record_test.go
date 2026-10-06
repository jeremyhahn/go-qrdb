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

package vector

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidDistanceMetric(t *testing.T) {
	for _, m := range []DistanceMetric{DistanceCosine, DistanceEuclidean, DistanceDotProduct} {
		if !ValidDistanceMetric(m) {
			t.Errorf("ValidDistanceMetric(%d) = false, want true", m)
		}
	}
	if ValidDistanceMetric(99) {
		t.Error("ValidDistanceMetric(99) = true, want false")
	}
}

func TestValidIndexType(t *testing.T) {
	for _, ty := range []IndexType{IndexHNSW, IndexFlat, IndexIVFFlat} {
		if !ValidIndexType(ty) {
			t.Errorf("ValidIndexType(%d) = false, want true", ty)
		}
	}
	if ValidIndexType(99) {
		t.Error("ValidIndexType(99) = true, want false")
	}
}

func TestEncodeDecodeNamespaceRecord_RoundTrips(t *testing.T) {
	want := NamespaceRecord{
		Dimension:          768,
		DistanceMetric:     DistanceCosine,
		IndexType:          IndexHNSW,
		IndexParams:        map[string]string{"m": "16"},
		Seq:                42,
		NextID:             43,
		TrainingGeneration: 1,
		CreatedAtUnixNano:  1700000000,
	}
	data, err := EncodeNamespaceRecord(want)
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	got, err := DecodeNamespaceRecord(data)
	if err != nil {
		t.Fatalf("DecodeNamespaceRecord: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeDecodeNamespaceRecord_NoParams(t *testing.T) {
	want := NamespaceRecord{Dimension: 3, DistanceMetric: DistanceEuclidean, IndexType: IndexFlat}
	data, err := EncodeNamespaceRecord(want)
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	got, err := DecodeNamespaceRecord(data)
	if err != nil {
		t.Fatalf("DecodeNamespaceRecord: %v", err)
	}
	if got.Dimension != want.Dimension || len(got.IndexParams) != 0 {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeNamespaceRecord_EmptyIsRefused(t *testing.T) {
	if _, err := DecodeNamespaceRecord(nil); err == nil {
		t.Fatal("decoding an empty namespace record succeeded")
	}
}

func TestDecodeNamespaceRecord_UnsupportedVersionIsRefused(t *testing.T) {
	data, err := EncodeNamespaceRecord(NamespaceRecord{Dimension: 1})
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	data[0] = recordFormatVersion + 1
	_, err = DecodeNamespaceRecord(data)
	if err == nil {
		t.Fatal("decoding a future format version succeeded")
	}
	var versionErr *UnsupportedRecordVersionError
	if !errors.As(err, &versionErr) {
		t.Fatalf("error = %T (%v), want *UnsupportedRecordVersionError", err, err)
	}
}

func TestDecodeNamespaceRecord_TruncatedIsRefused(t *testing.T) {
	data, err := EncodeNamespaceRecord(NamespaceRecord{
		Dimension: 1, IndexParams: map[string]string{"a": "b"},
	})
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	for n := 1; n < len(data); n++ {
		if _, err := DecodeNamespaceRecord(data[:n]); err == nil {
			t.Fatalf("decoding %d of %d bytes succeeded", n, len(data))
		}
	}
}

func TestEncodeDecodeVectorRecord_RoundTrips(t *testing.T) {
	want := VectorRecord{
		Embedding:         []float32{0.1, 0.2, -0.3},
		Metadata:          map[string]string{"source": "a"},
		CreatedAtUnixNano: 10,
		UpdatedAtUnixNano: 20,
	}
	data, err := EncodeVectorRecord(want)
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	got, err := DecodeVectorRecord(data)
	if err != nil {
		t.Fatalf("DecodeVectorRecord: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeDecodeVectorRecord_EmptyEmbeddingAndMetadata(t *testing.T) {
	want := VectorRecord{}
	data, err := EncodeVectorRecord(want)
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	got, err := DecodeVectorRecord(data)
	if err != nil {
		t.Fatalf("DecodeVectorRecord: %v", err)
	}
	if len(got.Embedding) != 0 || len(got.Metadata) != 0 {
		t.Fatalf("round trip = %+v, want empty embedding and metadata", got)
	}
}

func TestDecodeVectorRecord_EmptyIsRefused(t *testing.T) {
	if _, err := DecodeVectorRecord(nil); err == nil {
		t.Fatal("decoding an empty vector record succeeded")
	}
}

func TestDecodeVectorRecord_UnsupportedVersionIsRefused(t *testing.T) {
	data, err := EncodeVectorRecord(VectorRecord{Embedding: []float32{1}})
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	data[0] = recordFormatVersion + 1
	if _, err := DecodeVectorRecord(data); err == nil {
		t.Fatal("decoding a future format version succeeded")
	}
}

func TestDecodeVectorRecord_TruncatedIsRefused(t *testing.T) {
	data, err := EncodeVectorRecord(VectorRecord{
		Embedding: []float32{1, 2}, Metadata: map[string]string{"a": "b"},
	})
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	for n := 1; n < len(data); n++ {
		if _, err := DecodeVectorRecord(data[:n]); err == nil {
			t.Fatalf("decoding %d of %d bytes succeeded", n, len(data))
		}
	}
}

func TestEncodeDecodeMutationLogEntry_RoundTrips(t *testing.T) {
	for _, op := range []MutationOp{MutationUpsert, MutationDelete, MutationUpdate} {
		want := MutationLogEntry{Op: op, ID: 7}
		data := EncodeMutationLogEntry(want)
		got, err := DecodeMutationLogEntry(data)
		if err != nil {
			t.Fatalf("DecodeMutationLogEntry: %v", err)
		}
		if got != want {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}

func TestDecodeMutationLogEntry_WrongLengthIsRefused(t *testing.T) {
	for _, n := range []int{0, 1, 8, 10} {
		if _, err := DecodeMutationLogEntry(make([]byte, n)); err == nil {
			t.Fatalf("decoding a %d-byte mutation log entry succeeded", n)
		}
	}
}

func TestValidNamespaceName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"docs", true},
		{"my-namespace-1", true},
		{"a", true},
		{"a9", true},
		{"", false},
		{"a/b", false},
		{"a\x00b", false},
		{"my-namespace_1", false},        // underscore is not a DNS-label character
		{"Docs", false},                  // uppercase is not a DNS-label character
		{"-docs", false},                 // leading hyphen
		{"docs-", false},                 // trailing hyphen
		{strings.Repeat("a", 64), false}, // over the 63-character limit
		{strings.Repeat("a", 63), true},  // exactly at the limit
	} {
		if got := ValidNamespaceName(tc.name); got != tc.want {
			t.Errorf("ValidNamespaceName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParamBlockSize_RefusesOversizedKey(t *testing.T) {
	long := strings.Repeat("k", 1<<16)
	if _, err := paramBlockSize(map[string]string{long: "v"}); err == nil {
		t.Fatal("encoding an oversized param key succeeded")
	}
}
