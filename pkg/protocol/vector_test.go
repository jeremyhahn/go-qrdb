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

package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Vector op code constants verification
// ---------------------------------------------------------------------------

func TestVectorOpCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		op   byte
		want byte
	}{
		{"OpVectorUpsert", OpVectorUpsert, 70},
		{"OpVectorDelete", OpVectorDelete, 71},
		{"OpVectorBatchUpsert", OpVectorBatchUpsert, 72},
		{"OpVectorBatchDelete", OpVectorBatchDelete, 73},
		{"OpNamespaceCreate", OpNamespaceCreate, 74},
		{"OpNamespaceDelete", OpNamespaceDelete, 75},
		{"OpVectorUpdateMetadata", OpVectorUpdateMetadata, 76},
		{"OpVectorRebuildIndex", OpVectorRebuildIndex, 77},
		{"OpVectorCompact", OpVectorCompact, 78},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.op != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.op, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VectorUpsert roundtrip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeVectorUpsert(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        string
		vector    []float32
		metadata  []byte
	}{
		{
			name:      "standard upsert",
			namespace: "embeddings",
			id:        "vec-001",
			vector:    []float32{1.0, 2.5, -3.14, 0.0},
			metadata:  []byte(`{"label":"test"}`),
		},
		{
			name:      "high dimensional vector",
			namespace: "large-ns",
			id:        "vec-large",
			vector:    makeFloat32Slice(128, 0.5),
			metadata:  []byte(`{"dim":128}`),
		},
		{
			name:      "single dimension",
			namespace: "scalar",
			id:        "s1",
			vector:    []float32{42.0},
			metadata:  []byte("m"),
		},
		{
			name:      "special float values",
			namespace: "special",
			id:        "sp",
			vector:    []float32{math.Float32frombits(0x7FC00000), float32(math.Inf(1)), float32(math.Inf(-1)), 0.0},
			metadata:  []byte("nan-and-inf"),
		},
		{
			name:      "empty namespace and id",
			namespace: "",
			id:        "",
			vector:    []float32{1.0},
			metadata:  []byte("x"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeVectorUpsert(tt.namespace, tt.id, tt.vector, tt.metadata)

			if encoded[0] != OpVectorUpsert {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpVectorUpsert)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}
			if cmd.Op != OpVectorUpsert {
				t.Fatalf("Op = %d, want %d", cmd.Op, OpVectorUpsert)
			}

			ns, id, vec, meta, err := DecodeVectorUpsert(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeVectorUpsert: %v", err)
			}
			if ns != tt.namespace {
				t.Errorf("namespace = %q, want %q", ns, tt.namespace)
			}
			if id != tt.id {
				t.Errorf("id = %q, want %q", id, tt.id)
			}
			if len(vec) != len(tt.vector) {
				t.Fatalf("vector len = %d, want %d", len(vec), len(tt.vector))
			}
			for i := range vec {
				if math.Float32bits(vec[i]) != math.Float32bits(tt.vector[i]) {
					t.Errorf("vector[%d] bits = %08x, want %08x",
						i, math.Float32bits(vec[i]), math.Float32bits(tt.vector[i]))
				}
			}
			if !bytes.Equal(meta, tt.metadata) {
				t.Errorf("metadata = %q, want %q", meta, tt.metadata)
			}
		})
	}
}

func TestEncodeDecodeVectorUpsert_EmptyMetadata(t *testing.T) {
	encoded := EncodeVectorUpsert("ns", "id", []float32{1.0, 2.0}, nil)

	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	ns, id, vec, meta, err := DecodeVectorUpsert(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeVectorUpsert: %v", err)
	}
	if ns != "ns" {
		t.Errorf("namespace = %q, want %q", ns, "ns")
	}
	if id != "id" {
		t.Errorf("id = %q, want %q", id, "id")
	}
	if len(vec) != 2 {
		t.Fatalf("vector len = %d, want 2", len(vec))
	}
	if vec[0] != 1.0 || vec[1] != 2.0 {
		t.Errorf("vector = %v, want [1.0, 2.0]", vec)
	}
	if len(meta) != 0 {
		t.Errorf("metadata len = %d, want 0", len(meta))
	}
}

func TestDecodeVectorUpsert_TruncatedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "namespace length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "namespace length exceeds payload",
		},
		{
			name: "missing id length",
			// nsLen=2, ns="ab" -- no room for idLen
			payload: []byte{0x02, 0x00, 'a', 'b'},
			wantMsg: "missing id length",
		},
		{
			name: "id length exceeds payload",
			// nsLen=1, ns="a", idLen=0xFF
			payload: []byte{0x01, 0x00, 'a', 0xFF, 0x00},
			wantMsg: "id length exceeds payload",
		},
		{
			name: "missing dimension",
			// nsLen=1, ns="a", idLen=1, id="b" -- no room for dim
			payload: []byte{0x01, 0x00, 'a', 0x01, 0x00, 'b'},
			wantMsg: "missing dimension",
		},
		{
			name: "vector data exceeds payload",
			// nsLen=1, ns="a", idLen=1, id="b", dim=100 -- only 0 bytes of vector
			payload: func() []byte {
				buf := make([]byte, 10)
				off := 0
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'a'
				off++
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'b'
				off++
				binary.LittleEndian.PutUint32(buf[off:], 100)
				return buf
			}(),
			wantMsg: "vector data exceeds payload",
		},
		{
			name: "missing metadata length",
			// nsLen=1, ns="a", idLen=1, id="b", dim=1, vec=[1.0] -- no metaLen
			payload: func() []byte {
				buf := make([]byte, 14)
				off := 0
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'a'
				off++
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'b'
				off++
				binary.LittleEndian.PutUint32(buf[off:], 1)
				off += 4
				binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(1.0))
				return buf
			}(),
			wantMsg: "missing metadata length",
		},
		{
			name: "metadata exceeds payload",
			// nsLen=1, ns="a", idLen=1, id="b", dim=1, vec=[1.0], metaLen=999
			payload: func() []byte {
				buf := make([]byte, 18)
				off := 0
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'a'
				off++
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'b'
				off++
				binary.LittleEndian.PutUint32(buf[off:], 1)
				off += 4
				binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(1.0))
				off += 4
				binary.LittleEndian.PutUint32(buf[off:], 999)
				return buf
			}(),
			wantMsg: "metadata exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := DecodeVectorUpsert(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if ice.Reason == "" {
				t.Fatal("expected non-empty reason")
			}
			if !containsSubstring(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VectorDelete roundtrip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeVectorDelete(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        string
	}{
		{
			name:      "standard delete",
			namespace: "embeddings",
			id:        "vec-001",
		},
		{
			name:      "empty namespace",
			namespace: "",
			id:        "some-id",
		},
		{
			name:      "empty id",
			namespace: "ns",
			id:        "",
		},
		{
			name:      "long values",
			namespace: string(bytes.Repeat([]byte("N"), 500)),
			id:        string(bytes.Repeat([]byte("I"), 500)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeVectorDelete(tt.namespace, tt.id)

			if encoded[0] != OpVectorDelete {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpVectorDelete)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ns, id, err := DecodeVectorDelete(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeVectorDelete: %v", err)
			}
			if ns != tt.namespace {
				t.Errorf("namespace = %q, want %q", ns, tt.namespace)
			}
			if id != tt.id {
				t.Errorf("id = %q, want %q", id, tt.id)
			}
		})
	}
}

func TestDecodeVectorDelete_TruncatedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "namespace length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "namespace length exceeds payload",
		},
		{
			name: "missing id length",
			// nsLen=1, ns="a" -- no room for idLen
			payload: []byte{0x01, 0x00, 'a'},
			wantMsg: "missing id length",
		},
		{
			name: "id length exceeds payload",
			// nsLen=1, ns="a", idLen=0xFF
			payload: []byte{0x01, 0x00, 'a', 0xFF, 0x00},
			wantMsg: "id length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeVectorDelete(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !containsSubstring(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NamespaceCreate roundtrip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeNamespaceCreate(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		dimension  uint32
		distMetric byte
		indexType  byte
	}{
		{
			name:       "cosine similarity HNSW",
			namespace:  "embeddings",
			dimension:  768,
			distMetric: 1,
			indexType:  1,
		},
		{
			name:       "euclidean flat",
			namespace:  "images",
			dimension:  512,
			distMetric: 0,
			indexType:  0,
		},
		{
			name:       "max dimension",
			namespace:  "huge",
			dimension:  math.MaxUint32,
			distMetric: 255,
			indexType:  255,
		},
		{
			name:       "empty namespace",
			namespace:  "",
			dimension:  3,
			distMetric: 2,
			indexType:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeNamespaceCreate(tt.namespace, tt.dimension, tt.distMetric, tt.indexType)

			if encoded[0] != OpNamespaceCreate {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpNamespaceCreate)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ns, dim, metric, idxType, err := DecodeNamespaceCreate(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeNamespaceCreate: %v", err)
			}
			if ns != tt.namespace {
				t.Errorf("namespace = %q, want %q", ns, tt.namespace)
			}
			if dim != tt.dimension {
				t.Errorf("dimension = %d, want %d", dim, tt.dimension)
			}
			if metric != tt.distMetric {
				t.Errorf("distMetric = %d, want %d", metric, tt.distMetric)
			}
			if idxType != tt.indexType {
				t.Errorf("indexType = %d, want %d", idxType, tt.indexType)
			}
		})
	}
}

func TestDecodeNamespaceCreate_TruncatedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "namespace length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "namespace length exceeds payload",
		},
		{
			name: "missing dimension and flags",
			// nsLen=1, ns="a" -- no room for dimension/metric/type (need 6 more bytes)
			payload: []byte{0x01, 0x00, 'a'},
			wantMsg: "missing dimension/metric/type",
		},
		{
			name: "partial dimension",
			// nsLen=1, ns="a", then only 3 bytes (need 6: 4 dim + 1 metric + 1 type)
			payload: []byte{0x01, 0x00, 'a', 0x00, 0x01, 0x00},
			wantMsg: "missing dimension/metric/type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := DecodeNamespaceCreate(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !containsSubstring(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NamespaceDelete roundtrip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeNamespaceDelete(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
	}{
		{
			name:      "standard namespace",
			namespace: "embeddings",
		},
		{
			name:      "empty namespace",
			namespace: "",
		},
		{
			name:      "long namespace",
			namespace: string(bytes.Repeat([]byte("Z"), 1000)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeNamespaceDelete(tt.namespace)

			if encoded[0] != OpNamespaceDelete {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpNamespaceDelete)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ns, err := DecodeNamespaceDelete(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeNamespaceDelete: %v", err)
			}
			if ns != tt.namespace {
				t.Errorf("namespace = %q, want %q", ns, tt.namespace)
			}
		})
	}
}

func TestDecodeNamespaceDelete_TruncatedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "namespace length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "namespace length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeNamespaceDelete(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !containsSubstring(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VectorBatchUpsert roundtrip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeVectorBatchUpsert(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		vectors   []VectorUpsertParams
	}{
		{
			name:      "multiple vectors",
			namespace: "embeddings",
			vectors: []VectorUpsertParams{
				{
					ID:       "vec-001",
					Vector:   []float32{1.0, 2.0, 3.0},
					Metadata: []byte(`{"a":1}`),
				},
				{
					ID:       "vec-002",
					Vector:   []float32{4.0, 5.0, 6.0},
					Metadata: []byte(`{"b":2}`),
				},
				{
					ID:       "vec-003",
					Vector:   []float32{7.0, 8.0, 9.0},
					Metadata: nil,
				},
			},
		},
		{
			name:      "single vector",
			namespace: "single",
			vectors: []VectorUpsertParams{
				{
					ID:       "only",
					Vector:   []float32{0.5},
					Metadata: []byte("meta"),
				},
			},
		},
		{
			name:      "different dimensions",
			namespace: "mixed-dim",
			vectors: []VectorUpsertParams{
				{
					ID:       "a",
					Vector:   []float32{1.0, 2.0},
					Metadata: []byte("aa"),
				},
				{
					ID:       "b",
					Vector:   []float32{3.0, 4.0, 5.0, 6.0},
					Metadata: []byte("bb"),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeVectorBatchUpsert(tt.namespace, tt.vectors)

			if encoded[0] != OpVectorBatchUpsert {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpVectorBatchUpsert)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ns, vecs, err := DecodeVectorBatchUpsert(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeVectorBatchUpsert: %v", err)
			}
			if ns != tt.namespace {
				t.Errorf("namespace = %q, want %q", ns, tt.namespace)
			}
			if len(vecs) != len(tt.vectors) {
				t.Fatalf("vector count = %d, want %d", len(vecs), len(tt.vectors))
			}
			for i, got := range vecs {
				want := tt.vectors[i]
				if got.ID != want.ID {
					t.Errorf("vectors[%d].ID = %q, want %q", i, got.ID, want.ID)
				}
				if len(got.Vector) != len(want.Vector) {
					t.Fatalf("vectors[%d].Vector len = %d, want %d", i, len(got.Vector), len(want.Vector))
				}
				for j := range got.Vector {
					if math.Float32bits(got.Vector[j]) != math.Float32bits(want.Vector[j]) {
						t.Errorf("vectors[%d].Vector[%d] bits = %08x, want %08x",
							i, j, math.Float32bits(got.Vector[j]), math.Float32bits(want.Vector[j]))
					}
				}
				wantMeta := want.Metadata
				if wantMeta == nil {
					wantMeta = []byte{}
				}
				gotMeta := got.Metadata
				if gotMeta == nil {
					gotMeta = []byte{}
				}
				if !bytes.Equal(gotMeta, wantMeta) {
					t.Errorf("vectors[%d].Metadata = %q, want %q", i, gotMeta, wantMeta)
				}
			}
		})
	}
}

func TestDecodeVectorBatchUpsert_Empty(t *testing.T) {
	encoded := EncodeVectorBatchUpsert("ns", nil)

	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	ns, vecs, err := DecodeVectorBatchUpsert(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeVectorBatchUpsert: %v", err)
	}
	if ns != "ns" {
		t.Errorf("namespace = %q, want %q", ns, "ns")
	}
	if len(vecs) != 0 {
		t.Errorf("vector count = %d, want 0", len(vecs))
	}
}

func TestDecodeVectorBatchUpsert_TruncatedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "namespace length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "namespace length exceeds payload",
		},
		{
			name: "missing vector count",
			// nsLen=1, ns="a" -- no room for count
			payload: []byte{0x01, 0x00, 'a'},
			wantMsg: "missing vector count",
		},
		{
			name: "truncated vector id length",
			// nsLen=1, ns="a", count=1 -- no room for first vector's idLen
			payload: []byte{0x01, 0x00, 'a', 0x01, 0x00},
			wantMsg: "truncated vector id length",
		},
		{
			name: "id length exceeds payload",
			// nsLen=1, ns="a", count=1, idLen=0xFF
			payload: []byte{0x01, 0x00, 'a', 0x01, 0x00, 0xFF, 0x00},
			wantMsg: "id length exceeds payload",
		},
		{
			name: "missing dimension in batch vector",
			// nsLen=1, ns="a", count=1, idLen=1, id="b" -- no dim
			payload: []byte{0x01, 0x00, 'a', 0x01, 0x00, 0x01, 0x00, 'b'},
			wantMsg: "missing dimension",
		},
		{
			name: "vector data exceeds payload in batch",
			// nsLen=1, ns="a", count=1, idLen=1, id="b", dim=100
			payload: func() []byte {
				buf := make([]byte, 12)
				off := 0
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'a'
				off++
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				binary.LittleEndian.PutUint16(buf[off:], 1)
				off += 2
				buf[off] = 'b'
				off++
				binary.LittleEndian.PutUint32(buf[off:], 100)
				return buf
			}(),
			wantMsg: "vector data exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeVectorBatchUpsert(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !containsSubstring(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeFloat32Slice creates a []float32 of the given length with all elements
// set to the given value.
func makeFloat32Slice(n int, val float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = val
	}
	return s
}

// containsSubstring reports whether s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (substr == "" || bytes.Contains([]byte(s), []byte(substr)))
}
