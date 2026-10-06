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

//go:build qrdb_vector && !integration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock vector service (no-op, returns empty successes)
// ---------------------------------------------------------------------------

type noopVectorService struct{}

func (s *noopVectorService) CreateNamespace(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return &service.CreateNamespaceResponse{}, nil
}

func (s *noopVectorService) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return &service.DeleteNamespaceResponse{}, nil
}

func (s *noopVectorService) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return &service.ListNamespacesResponse{}, nil
}

func (s *noopVectorService) GetNamespaceInfo(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return &service.GetNamespaceInfoResponse{}, nil
}

func (s *noopVectorService) UpsertVector(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return &service.UpsertVectorResponse{}, nil
}

func (s *noopVectorService) GetVector(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return &service.GetVectorResponse{}, nil
}

func (s *noopVectorService) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return &service.DeleteVectorResponse{}, nil
}

func (s *noopVectorService) BatchUpsert(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return &service.BatchUpsertResponse{}, nil
}

func (s *noopVectorService) BatchDeleteVectors(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return &service.BatchDeleteVectorsResponse{}, nil
}

func (s *noopVectorService) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return &service.ListVectorsResponse{}, nil
}

func (s *noopVectorService) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return &service.UpdateVectorMetadataResponse{}, nil
}

func (s *noopVectorService) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return &service.SearchSimilarResponse{}, nil
}

func (s *noopVectorService) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return &service.SearchByMetadataResponse{}, nil
}

func (s *noopVectorService) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return &service.HybridSearchResponse{}, nil
}

func (s *noopVectorService) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return &service.SearchByIDResponse{}, nil
}

func (s *noopVectorService) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return &service.RebuildIndexResponse{}, nil
}

func (s *noopVectorService) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return &service.GetRebuildProgressResponse{}, nil
}

func (s *noopVectorService) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return &service.GetIndexStatsResponse{}, nil
}

func (s *noopVectorService) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return &service.CompactNamespaceResponse{}, nil
}

func (s *noopVectorService) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return &service.GetCompactionProgressResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestVectorBinding creates a VectorBinding backed by an embedded transport
// with the no-op vector service.
func newTestVectorBinding(t *testing.T) *VectorBinding {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	trans, err := embedded.New(
		kvSvc,
		adminSvc,
		embedded.WithVectorService(&noopVectorService{}),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	conn.SetClient(cl)
	return NewVectorBinding(log, conn)
}

// newVectorBindingNoConn returns a VectorBinding with no active client.
func newVectorBindingNoConn(t *testing.T) *VectorBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewVectorBinding(log, conn)
}

// ---------------------------------------------------------------------------
// CreateNamespace
// ---------------------------------------------------------------------------

func TestVecBinding_CreateNamespace_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.CreateNamespace(&transport.CreateNamespaceRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_CreateNamespace_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.CreateNamespace(&transport.CreateNamespaceRequest{
		Namespace: "embeddings",
		Dimension: 128,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// DeleteNamespace
// ---------------------------------------------------------------------------

func TestVecBinding_DeleteNamespace_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.DeleteNamespace(&transport.DeleteNamespaceRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_DeleteNamespace_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.DeleteNamespace(&transport.DeleteNamespaceRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// ListNamespaces
// ---------------------------------------------------------------------------

func TestVecBinding_ListNamespaces_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.ListNamespaces(&transport.ListNamespacesRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_ListNamespaces_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.ListNamespaces(&transport.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetNamespaceInfo
// ---------------------------------------------------------------------------

func TestVecBinding_GetNamespaceInfo_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.GetNamespaceInfo(&transport.GetNamespaceInfoRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_GetNamespaceInfo_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.GetNamespaceInfo(&transport.GetNamespaceInfoRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// UpsertVector
// ---------------------------------------------------------------------------

func TestVecBinding_UpsertVector_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.UpsertVector(&transport.UpsertVectorRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_UpsertVector_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.UpsertVector(&transport.UpsertVectorRequest{
		Namespace: "embeddings",
		ID:        1,
		Vector:    []float32{0.1, 0.2, 0.3},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetVector
// ---------------------------------------------------------------------------

func TestVecBinding_GetVector_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.GetVector(&transport.GetVectorRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_GetVector_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.GetVector(&transport.GetVectorRequest{
		Namespace: "embeddings",
		ID:        1,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// DeleteVector
// ---------------------------------------------------------------------------

func TestVecBinding_DeleteVector_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.DeleteVector(&transport.DeleteVectorRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_DeleteVector_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.DeleteVector(&transport.DeleteVectorRequest{
		Namespace: "embeddings",
		ID:        1,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// BatchUpsert
// ---------------------------------------------------------------------------

func TestVecBinding_BatchUpsert_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.BatchUpsert(&transport.BatchUpsertRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_BatchUpsert_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.BatchUpsert(&transport.BatchUpsertRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// BatchDeleteVectors
// ---------------------------------------------------------------------------

func TestVecBinding_BatchDeleteVectors_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.BatchDeleteVectors(&transport.BatchDeleteVectorsRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_BatchDeleteVectors_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.BatchDeleteVectors(&transport.BatchDeleteVectorsRequest{
		Namespace: "embeddings",
		IDs:       []uint64{1, 2},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// ListVectors
// ---------------------------------------------------------------------------

func TestVecBinding_ListVectors_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.ListVectors(&transport.ListVectorsRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_ListVectors_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.ListVectors(&transport.ListVectorsRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// UpdateMetadata
// ---------------------------------------------------------------------------

func TestVecBinding_UpdateMetadata_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.UpdateMetadata(&transport.UpdateVectorMetadataRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_UpdateMetadata_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.UpdateMetadata(&transport.UpdateVectorMetadataRequest{
		Namespace: "embeddings",
		ID:        1,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// SearchSimilar
// ---------------------------------------------------------------------------

func TestVecBinding_SearchSimilar_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.SearchSimilar(&transport.SearchSimilarRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_SearchSimilar_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.SearchSimilar(&transport.SearchSimilarRequest{
		Namespace:   "embeddings",
		QueryVector: []float32{0.1, 0.2, 0.3},
		K:           5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// SearchByMetadata
// ---------------------------------------------------------------------------

func TestVecBinding_SearchByMetadata_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.SearchByMetadata(&transport.SearchByMetadataRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_SearchByMetadata_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.SearchByMetadata(&transport.SearchByMetadataRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// HybridSearch
// ---------------------------------------------------------------------------

func TestVecBinding_HybridSearch_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.HybridSearch(&transport.HybridSearchRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_HybridSearch_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.HybridSearch(&transport.HybridSearchRequest{
		Namespace:   "embeddings",
		QueryVector: []float32{0.1, 0.2, 0.3},
		K:           5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// SearchByID
// ---------------------------------------------------------------------------

func TestVecBinding_SearchByID_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.SearchByID(&transport.SearchByIDRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_SearchByID_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.SearchByID(&transport.SearchByIDRequest{
		Namespace: "embeddings",
		ID:        1,
		K:         5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// RebuildIndex
// ---------------------------------------------------------------------------

func TestVecBinding_RebuildIndex_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.RebuildIndex(&transport.RebuildIndexRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_RebuildIndex_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.RebuildIndex(&transport.RebuildIndexRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetRebuildProgress
// ---------------------------------------------------------------------------

func TestVecBinding_GetRebuildProgress_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.GetRebuildProgress(&transport.GetRebuildProgressRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_GetRebuildProgress_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.GetRebuildProgress(&transport.GetRebuildProgressRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetIndexStats
// ---------------------------------------------------------------------------

func TestVecBinding_GetIndexStats_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.GetIndexStats(&transport.GetIndexStatsRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_GetIndexStats_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.GetIndexStats(&transport.GetIndexStatsRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// CompactNamespace
// ---------------------------------------------------------------------------

func TestVecBinding_CompactNamespace_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.CompactNamespace(&transport.CompactNamespaceRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_CompactNamespace_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.CompactNamespace(&transport.CompactNamespaceRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetCompactionProgress
// ---------------------------------------------------------------------------

func TestVecBinding_GetCompactionProgress_NoClient(t *testing.T) {
	b := newVectorBindingNoConn(t)
	resp, err := b.GetCompactionProgress(&transport.GetCompactionProgressRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestVecBinding_GetCompactionProgress_Connected(t *testing.T) {
	b := newTestVectorBinding(t)
	resp, err := b.GetCompactionProgress(&transport.GetCompactionProgressRequest{
		Namespace: "embeddings",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}
