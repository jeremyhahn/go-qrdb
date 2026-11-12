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

package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

func newTypedMetadataService(t *testing.T) VectorService {
	t.Helper()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	if _, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "typed", Dimension: 2, DistanceMetric: "COSINE", IndexType: "HNSW",
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return svc
}

// TestUpsertVector_MetadataKeepsJSONTypes proves a number, a bool and an
// array stored as vector metadata come back with their JSON types.
func TestUpsertVector_MetadataKeepsJSONTypes(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	meta := map[string]interface{}{
		"score": float64(0.75),
		"live":  true,
		"tags":  []interface{}{"a", float64(2)},
		"name":  "doc",
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: meta}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	got, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "typed", ID: 1, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if !reflect.DeepEqual(got.Metadata, meta) {
		t.Fatalf("metadata = %#v, want %#v", got.Metadata, meta)
	}
}

// TestUpsertVector_ReportsCreated proves the first upsert of an ID reports
// created and a second upsert of the same ID does not.
func TestUpsertVector_ReportsCreated(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	req := &UpsertVectorRequest{Namespace: "typed", ID: 7, Vector: []float32{0, 1}}
	first, err := svc.UpsertVector(ctx, req)
	if err != nil {
		t.Fatalf("first UpsertVector: %v", err)
	}
	if !first.Created {
		t.Fatal("first upsert: created = false, want true")
	}
	second, err := svc.UpsertVector(ctx, req)
	if err != nil {
		t.Fatalf("second UpsertVector: %v", err)
	}
	if second.Created {
		t.Fatal("second upsert: created = true, want false")
	}
}
