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

package quic

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_CreateNamespace_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.CreateNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorCreateNamespaceResponse{
			Created: true, Timestamp: 100,
		})
	})
	resp, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, DistanceMetric: transport.DistanceCosine, IndexType: transport.IndexHNSW,
	})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteNamespace_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.DeleteNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorDeleteNamespaceRequest{})
	})
	resp, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ListNamespaces_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.ListNamespaces", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorListNamespacesResponse{
			Namespaces: []string{"a", "b"},
		})
	})
	resp, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if resp.TotalCount != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetNamespaceInfo_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetNamespaceInfo", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorNamespaceInfo{
			Namespace: "ns", Dimension: 4, VectorCount: 1,
			DistanceMetric: pb.DistanceMetric_DISTANCE_COSINE, IndexType: pb.VectorIndexType_INDEX_HNSW,
		})
	})
	resp, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if resp.Dimension != 4 || resp.IndexType != transport.IndexHNSW {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpsertVector_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Upsert", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorUpsertResponse{Created: true, Version: 1, Timestamp: 100})
	})
	resp, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns", ID: 7, Vector: []float32{0.1, 0.2, 0.3, 0.4},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if resp.Version != 1 || !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetVector_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorDataProto{
			Id: "7", Vector: []float32{0.1, 0.2}, Version: 3, CreatedAt: 100, UpdatedAt: 200,
		})
	})
	resp, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{Namespace: "ns", ID: 7})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if resp.Version != 3 || !resp.Found {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteVector_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	resp, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{Namespace: "ns", ID: 7})
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BatchUpsert_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.BatchUpsert", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorBatchUpsertResponse{Succeeded: 2, Failed: 0})
	})
	resp, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*transport.VectorData{
			{ID: 1, Vector: []float32{0.1}},
			{ID: 2, Vector: []float32{0.2}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	if resp.InsertedCount != 2 || !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_BatchDeleteVectors_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.BatchDelete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorBatchDeleteResponse{Deleted: 2, Failed: 0})
	})
	resp, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "ns", IDs: []uint64{1, 2},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if resp.DeletedCount != 2 || !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ListVectors_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorListResponse{
			Vectors: []*pb.VectorDataProto{
				{Id: "1", Vector: []float32{0.1}},
			},
			Total:      1,
			NextCursor: "next",
		})
	})
	resp, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{Namespace: "ns", Limit: 10})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if resp.TotalCount != 1 || resp.NextCursor != "next" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateMetadata_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.UpdateMetadata", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	resp, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "ns", ID: 7, Metadata: map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_SearchSimilar_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchSimilar", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorSearchSimilarResponse{
			Results: []*pb.VectorSearchResultProto{
				{Id: "1", Score: 0.9},
			},
			Took: 5,
		})
	})
	resp, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{0.1, 0.2}, K: 5,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_SearchByMetadata_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchByMetadata", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorSearchByMetadataResponse{
			Results: []*pb.VectorDataProto{
				{Id: "1", Vector: []float32{0.1}},
			},
			Total: 1,
		})
	})
	resp, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "ns", Filters: map[string]any{"k": "v"}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	if resp.TotalCount != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_HybridSearch_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.HybridSearch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorHybridSearchResponse{
			Results: []*pb.VectorSearchResultProto{{Id: "1", Score: 0.95}},
			Took:    7,
		})
	})
	resp, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace: "ns", QueryVector: []float32{0.1}, K: 5,
		MetadataFilter: map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_SearchByID_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchByID", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorSearchSimilarResponse{
			Results: []*pb.VectorSearchResultProto{{Id: "2", Score: 0.85}},
		})
	})
	resp, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{
		Namespace: "ns", ID: 1, K: 5,
	})
	if err != nil {
		t.Fatalf("SearchByID: %v", err)
	}
	if resp.SourceID != 1 || len(resp.Results) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_RebuildIndex_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.RebuildIndex", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorRebuildIndexResponse{JobId: "j1", Started: true, Timestamp: 100})
	})
	resp, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if resp.JobID != "j1" || !resp.Success {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetRebuildProgress_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetRebuildProgress", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorRebuildProgress{
			JobId: "j1", Status: "running", Progress: 0.5,
		})
	})
	resp, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{
		Namespace: "ns", JobID: "j1",
	})
	if err != nil {
		t.Fatalf("GetRebuildProgress: %v", err)
	}
	if resp.Status != "running" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetIndexStats_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetIndexStats", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorIndexStats{
			IndexType: pb.VectorIndexType_INDEX_HNSW, VectorCount: 100, Dimension: 4,
		})
	})
	resp, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if resp.VectorCount != 100 || resp.Dimension != 4 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_CompactNamespace_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.CompactNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorCompactNamespaceResponse{JobId: "c1", Started: true})
	})
	resp, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	if resp.OperationID != "c1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetCompactionProgress_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetCompactionProgress", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.VectorCompactionProgress{
			JobId: "c1", Status: "running", Progress: 0.7,
		})
	})
	resp, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{
		Namespace: "ns", OperationID: "c1",
	})
	if err != nil {
		t.Fatalf("GetCompactionProgress: %v", err)
	}
	if !resp.InProgress {
		t.Fatalf("resp = %+v", resp)
	}
}
