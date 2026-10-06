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
	"reflect"
	"testing"
)

func TestVectorOpCodes_AreDistinctAndInTheReservedRange(t *testing.T) {
	ops := map[string]byte{
		"OpVectorNamespaceCreate":  OpVectorNamespaceCreate,
		"OpVectorNamespaceDelete":  OpVectorNamespaceDelete,
		"OpVectorUpsert":           OpVectorUpsert,
		"OpVectorBatchUpsert":      OpVectorBatchUpsert,
		"OpVectorDelete":           OpVectorDelete,
		"OpVectorBatchDelete":      OpVectorBatchDelete,
		"OpVectorUpdateMetadata":   OpVectorUpdateMetadata,
		"OpVectorIndexReconfigure": OpVectorIndexReconfigure,
	}
	seen := make(map[byte]string, len(ops))
	for name, op := range ops {
		if op < 70 || op > 79 {
			t.Errorf("%s = %d, want a byte in the reserved vector range [70,79]", name, op)
		}
		if other, dup := seen[op]; dup {
			t.Errorf("%s and %s share opcode %d", name, other, op)
		}
		seen[op] = name
	}
}

func TestEncodeDecodeVectorNamespaceCreate_RoundTrips(t *testing.T) {
	want := VectorNamespaceCreateParams{
		Name:              "docs",
		Dimension:         768,
		DistanceMetric:    1,
		IndexType:         2,
		CreatedAtUnixNano: 1234567890,
		IndexParams:       map[string]string{"m": "16", "ef_construction": "200"},
	}
	cmd, err := EncodeVectorNamespaceCreate(want)
	if err != nil {
		t.Fatalf("EncodeVectorNamespaceCreate: %v", err)
	}
	if cmd[0] != OpVectorNamespaceCreate {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorNamespaceCreate)
	}
	got, err := DecodeVectorNamespaceCreate(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorNamespaceCreate: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeDecodeVectorNamespaceCreate_EmptyParams(t *testing.T) {
	want := VectorNamespaceCreateParams{Name: "ns", Dimension: 3}
	cmd, err := EncodeVectorNamespaceCreate(want)
	if err != nil {
		t.Fatalf("EncodeVectorNamespaceCreate: %v", err)
	}
	got, err := DecodeVectorNamespaceCreate(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorNamespaceCreate: %v", err)
	}
	if got.Name != want.Name || got.Dimension != want.Dimension || len(got.IndexParams) != 0 {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeVectorNamespaceCreate_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorNamespaceCreate(VectorNamespaceCreateParams{Name: "ns", Dimension: 3})
	if err != nil {
		t.Fatalf("EncodeVectorNamespaceCreate: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, err := DecodeVectorNamespaceCreate(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorNamespaceDelete_RoundTrips(t *testing.T) {
	cmd, err := EncodeVectorNamespaceDelete("docs")
	if err != nil {
		t.Fatalf("EncodeVectorNamespaceDelete: %v", err)
	}
	if cmd[0] != OpVectorNamespaceDelete {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorNamespaceDelete)
	}
	name, err := DecodeVectorNamespaceDelete(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorNamespaceDelete: %v", err)
	}
	if name != "docs" {
		t.Fatalf("name = %q, want %q", name, "docs")
	}
}

func TestDecodeVectorNamespaceDelete_TruncatedPayloadIsRefused(t *testing.T) {
	if _, err := DecodeVectorNamespaceDelete([]byte{1, 0}); err == nil {
		t.Fatal("decoding a truncated name succeeded")
	}
}

func TestEncodeDecodeVectorUpsert_RoundTrips(t *testing.T) {
	item := VectorUpsertParams{
		HasID:     true,
		ID:        42,
		Embedding: []float32{0.1, -0.2, 3.5},
		Metadata:  map[string]string{"source": "doc-1"},
	}
	cmd, err := EncodeVectorUpsert("docs", 1700000000, item)
	if err != nil {
		t.Fatalf("EncodeVectorUpsert: %v", err)
	}
	if cmd[0] != OpVectorUpsert {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorUpsert)
	}
	name, ts, got, err := DecodeVectorUpsert(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorUpsert: %v", err)
	}
	if name != "docs" || ts != 1700000000 {
		t.Fatalf("name,ts = %q,%d, want %q,%d", name, ts, "docs", 1700000000)
	}
	if !reflect.DeepEqual(item, got) {
		t.Fatalf("round trip = %+v, want %+v", got, item)
	}
}

func TestEncodeDecodeVectorUpsert_OmittedIDAndEmptyMetadata(t *testing.T) {
	item := VectorUpsertParams{Embedding: []float32{1, 2}}
	cmd, err := EncodeVectorUpsert("ns", 0, item)
	if err != nil {
		t.Fatalf("EncodeVectorUpsert: %v", err)
	}
	_, _, got, err := DecodeVectorUpsert(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorUpsert: %v", err)
	}
	if got.HasID {
		t.Fatal("HasID = true, want false for an omitted ID")
	}
	if len(got.Metadata) != 0 {
		t.Fatalf("Metadata = %v, want empty", got.Metadata)
	}
	if !reflect.DeepEqual(got.Embedding, item.Embedding) {
		t.Fatalf("Embedding = %v, want %v", got.Embedding, item.Embedding)
	}
}

func TestDecodeVectorUpsert_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorUpsert("ns", 5, VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 2}})
	if err != nil {
		t.Fatalf("EncodeVectorUpsert: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, _, _, err := DecodeVectorUpsert(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorBatchUpsert_RoundTrips(t *testing.T) {
	want := VectorBatchUpsertParams{
		Name:              "docs",
		TimestampUnixNano: 99,
		Vectors: []VectorUpsertParams{
			{HasID: true, ID: 1, Embedding: []float32{1, 2}, Metadata: map[string]string{"a": "1"}},
			{Embedding: []float32{3, 4}},
		},
	}
	cmd, err := EncodeVectorBatchUpsert(want)
	if err != nil {
		t.Fatalf("EncodeVectorBatchUpsert: %v", err)
	}
	if cmd[0] != OpVectorBatchUpsert {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorBatchUpsert)
	}
	got, err := DecodeVectorBatchUpsert(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorBatchUpsert: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeDecodeVectorBatchUpsert_Empty(t *testing.T) {
	want := VectorBatchUpsertParams{Name: "ns"}
	cmd, err := EncodeVectorBatchUpsert(want)
	if err != nil {
		t.Fatalf("EncodeVectorBatchUpsert: %v", err)
	}
	got, err := DecodeVectorBatchUpsert(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorBatchUpsert: %v", err)
	}
	if got.Name != "ns" || len(got.Vectors) != 0 {
		t.Fatalf("round trip = %+v, want name=ns, no vectors", got)
	}
}

func TestDecodeVectorBatchUpsert_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorBatchUpsert(VectorBatchUpsertParams{
		Name:    "ns",
		Vectors: []VectorUpsertParams{{Embedding: []float32{1, 2}}},
	})
	if err != nil {
		t.Fatalf("EncodeVectorBatchUpsert: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, err := DecodeVectorBatchUpsert(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorDelete_RoundTrips(t *testing.T) {
	cmd, err := EncodeVectorDelete("docs", 7)
	if err != nil {
		t.Fatalf("EncodeVectorDelete: %v", err)
	}
	if cmd[0] != OpVectorDelete {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorDelete)
	}
	name, id, err := DecodeVectorDelete(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorDelete: %v", err)
	}
	if name != "docs" || id != 7 {
		t.Fatalf("name,id = %q,%d, want %q,%d", name, id, "docs", 7)
	}
}

func TestDecodeVectorDelete_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorDelete("ns", 1)
	if err != nil {
		t.Fatalf("EncodeVectorDelete: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, _, err := DecodeVectorDelete(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorBatchDelete_RoundTrips(t *testing.T) {
	want := []uint64{1, 2, 3}
	cmd, err := EncodeVectorBatchDelete("docs", want)
	if err != nil {
		t.Fatalf("EncodeVectorBatchDelete: %v", err)
	}
	if cmd[0] != OpVectorBatchDelete {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorBatchDelete)
	}
	name, ids, err := DecodeVectorBatchDelete(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorBatchDelete: %v", err)
	}
	if name != "docs" || !reflect.DeepEqual(ids, want) {
		t.Fatalf("round trip = %q,%v, want %q,%v", name, ids, "docs", want)
	}
}

func TestEncodeDecodeVectorBatchDelete_Empty(t *testing.T) {
	cmd, err := EncodeVectorBatchDelete("ns", nil)
	if err != nil {
		t.Fatalf("EncodeVectorBatchDelete: %v", err)
	}
	name, ids, err := DecodeVectorBatchDelete(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorBatchDelete: %v", err)
	}
	if name != "ns" || len(ids) != 0 {
		t.Fatalf("round trip = %q,%v, want ns, empty", name, ids)
	}
}

func TestDecodeVectorBatchDelete_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorBatchDelete("ns", []uint64{1, 2})
	if err != nil {
		t.Fatalf("EncodeVectorBatchDelete: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, _, err := DecodeVectorBatchDelete(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorUpdateMetadata_RoundTrips(t *testing.T) {
	want := VectorUpdateMetadataParams{
		Name:              "docs",
		ID:                9,
		TimestampUnixNano: 123,
		Metadata:          map[string]string{"k": "v"},
	}
	cmd, err := EncodeVectorUpdateMetadata(want)
	if err != nil {
		t.Fatalf("EncodeVectorUpdateMetadata: %v", err)
	}
	if cmd[0] != OpVectorUpdateMetadata {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorUpdateMetadata)
	}
	got, err := DecodeVectorUpdateMetadata(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorUpdateMetadata: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeVectorUpdateMetadata_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorUpdateMetadata(VectorUpdateMetadataParams{Name: "ns", ID: 1, Metadata: map[string]string{"a": "b"}})
	if err != nil {
		t.Fatalf("EncodeVectorUpdateMetadata: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, err := DecodeVectorUpdateMetadata(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

func TestEncodeDecodeVectorIndexReconfigure_RoundTrips(t *testing.T) {
	want := VectorIndexReconfigureParams{
		Name:        "docs",
		IndexType:   2,
		IndexParams: map[string]string{"nlist": "128"},
	}
	cmd, err := EncodeVectorIndexReconfigure(want)
	if err != nil {
		t.Fatalf("EncodeVectorIndexReconfigure: %v", err)
	}
	if cmd[0] != OpVectorIndexReconfigure {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpVectorIndexReconfigure)
	}
	got, err := DecodeVectorIndexReconfigure(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVectorIndexReconfigure: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeVectorIndexReconfigure_TruncatedPayloadIsRefused(t *testing.T) {
	cmd, err := EncodeVectorIndexReconfigure(VectorIndexReconfigureParams{Name: "ns", IndexType: 1})
	if err != nil {
		t.Fatalf("EncodeVectorIndexReconfigure: %v", err)
	}
	for n := 0; n < len(cmd)-1; n++ {
		if _, err := DecodeVectorIndexReconfigure(cmd[1 : 1+n]); err == nil {
			t.Fatalf("decoding %d of %d payload bytes succeeded", n, len(cmd)-1)
		}
	}
}

// Every vector command must decode through the package's single dispatch
// point (DecodeCommand), not merely through its own Decode* function, so a
// committed entry reaches the right handler.
func TestDecodeCommand_RecognisesEveryVectorOpcode(t *testing.T) {
	cmds := map[byte][]byte{}
	var err error
	if cmds[OpVectorNamespaceCreate], err = EncodeVectorNamespaceCreate(VectorNamespaceCreateParams{Name: "ns", Dimension: 1}); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorNamespaceDelete], err = EncodeVectorNamespaceDelete("ns"); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorUpsert], err = EncodeVectorUpsert("ns", 0, VectorUpsertParams{Embedding: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorBatchUpsert], err = EncodeVectorBatchUpsert(VectorBatchUpsertParams{Name: "ns"}); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorDelete], err = EncodeVectorDelete("ns", 1); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorBatchDelete], err = EncodeVectorBatchDelete("ns", nil); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorUpdateMetadata], err = EncodeVectorUpdateMetadata(VectorUpdateMetadataParams{Name: "ns", ID: 1}); err != nil {
		t.Fatal(err)
	}
	if cmds[OpVectorIndexReconfigure], err = EncodeVectorIndexReconfigure(VectorIndexReconfigureParams{Name: "ns"}); err != nil {
		t.Fatal(err)
	}

	for op, raw := range cmds {
		decoded, err := DecodeCommand(raw)
		if err != nil {
			t.Fatalf("opcode %d: DecodeCommand: %v", op, err)
		}
		if decoded.Op != op {
			t.Fatalf("decoded.Op = %d, want %d", decoded.Op, op)
		}
	}
}
