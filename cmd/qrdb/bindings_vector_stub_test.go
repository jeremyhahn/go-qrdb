//go:build !qrdb_vector && !integration

// Copyright 2026 Jeremy Hahn
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

package main

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newVectorStubBinding creates a VectorBinding backed by a no-connection
// ConnectionBinding (the stub always returns FeatureDisabledError).
func newVectorStubBinding(t *testing.T) *VectorBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewVectorBinding(log, conn)
}

func assertVectorFeatureDisabled(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected FeatureDisabledError, got nil")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestVectorStub_NewVectorBinding(t *testing.T) {
	b := newVectorStubBinding(t)
	if b == nil {
		t.Fatal("expected non-nil VectorBinding")
	}
}

func TestVectorStub_CreateNamespace(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.CreateNamespace(&transport.CreateNamespaceRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_DeleteNamespace(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.DeleteNamespace(&transport.DeleteNamespaceRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_ListNamespaces(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.ListNamespaces(&transport.ListNamespacesRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_GetNamespaceInfo(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.GetNamespaceInfo(&transport.GetNamespaceInfoRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_UpsertVector(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.UpsertVector(&transport.UpsertVectorRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_GetVector(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.GetVector(&transport.GetVectorRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_DeleteVector(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.DeleteVector(&transport.DeleteVectorRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_BatchUpsert(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.BatchUpsert(&transport.BatchUpsertRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_BatchDeleteVectors(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.BatchDeleteVectors(&transport.BatchDeleteVectorsRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_ListVectors(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.ListVectors(&transport.ListVectorsRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_UpdateMetadata(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.UpdateMetadata(&transport.UpdateVectorMetadataRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_SearchSimilar(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.SearchSimilar(&transport.SearchSimilarRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_SearchByMetadata(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.SearchByMetadata(&transport.SearchByMetadataRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_HybridSearch(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.HybridSearch(&transport.HybridSearchRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_SearchByID(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.SearchByID(&transport.SearchByIDRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_RebuildIndex(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.RebuildIndex(&transport.RebuildIndexRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_GetRebuildProgress(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.GetRebuildProgress(&transport.GetRebuildProgressRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_GetIndexStats(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.GetIndexStats(&transport.GetIndexStatsRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_CompactNamespace(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.CompactNamespace(&transport.CompactNamespaceRequest{})
	assertVectorFeatureDisabled(t, err)
}

func TestVectorStub_GetCompactionProgress(t *testing.T) {
	b := newVectorStubBinding(t)
	_, err := b.GetCompactionProgress(&transport.GetCompactionProgressRequest{})
	assertVectorFeatureDisabled(t, err)
}
