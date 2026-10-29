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

package grpc

import (
	"context"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeVectorService
// ===========================================================================

type fakeVectorService struct{}

var _ service.VectorServicer = (*fakeVectorService)(nil)

func (f *fakeVectorService) CreateNamespace(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return &service.CreateNamespaceResponse{Namespace: req.Namespace, Dimension: req.Dimension, Success: true, Timestamp: 1}, nil
}

func (f *fakeVectorService) UpdateNamespace(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: len(req.IndexConfig) > 0, Success: true, Timestamp: 1}, nil
}

func (f *fakeVectorService) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return &service.DeleteNamespaceResponse{Success: true, VectorsDeleted: 5, Timestamp: 1}, nil
}

func (f *fakeVectorService) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{{Namespace: "ns", Dimension: 128}},
		TotalCount: 1,
	}, nil
}

func (f *fakeVectorService) GetNamespaceInfo(_ context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return &service.GetNamespaceInfoResponse{Namespace: req.Namespace, Dimension: 128, VectorCount: 100}, nil
}

func (f *fakeVectorService) UpsertVector(_ context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Success: true}, nil
}

func (f *fakeVectorService) GetVector(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return &service.GetVectorResponse{ID: req.ID, Vector: []float32{0.1, 0.2}, Metadata: map[string]interface{}{"k": "v"}, Version: 1, Found: true}, nil
}

func (f *fakeVectorService) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return &service.DeleteVectorResponse{Success: true}, nil
}

func (f *fakeVectorService) BatchUpsert(_ context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return &service.BatchUpsertResponse{IDs: []uint64{1}, Versions: []uint64{1}, Success: true, InsertedCount: len(req.Vectors)}, nil
}

func (f *fakeVectorService) BatchDeleteVectors(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return &service.BatchDeleteVectorsResponse{DeletedCount: len(req.IDs), Success: true}, nil
}

func (f *fakeVectorService) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return &service.ListVectorsResponse{Vectors: []*service.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1}}, TotalCount: 1}, nil
}

func (f *fakeVectorService) UpdateMetadata(_ context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Success: true}, nil
}

func (f *fakeVectorService) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return &service.SearchSimilarResponse{Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95, Vector: []float32{0.1}, Rank: 1}}}, nil
}

func (f *fakeVectorService) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return &service.SearchByMetadataResponse{Results: []*service.VectorData{{ID: 1, Vector: []float32{0.1}, Version: 1}}, TotalCount: 1}, nil
}

func (f *fakeVectorService) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return &service.HybridSearchResponse{Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95}}}, nil
}

func (f *fakeVectorService) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return &service.SearchByIDResponse{Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95}}}, nil
}

func (f *fakeVectorService) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return &service.RebuildIndexResponse{JobID: "j1", Status: "running", Success: true}, nil
}

func (f *fakeVectorService) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return &service.GetRebuildProgressResponse{JobID: "j1", Status: "running", PercentComplete: 50.0}, nil
}

func (f *fakeVectorService) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return &service.GetIndexStatsResponse{Namespace: "ns", IndexType: "HNSW", VectorCount: 100, Dimension: 128}, nil
}

func (f *fakeVectorService) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return &service.CompactNamespaceResponse{OperationID: "op1", Success: true}, nil
}

func (f *fakeVectorService) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return &service.GetCompactionProgressResponse{OperationID: "op1", InProgress: true, PercentComplete: 50}, nil
}

// ===========================================================================
// Server setup
// ===========================================================================

func startVectorServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterVector(&fakeVectorService{}))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectVectorTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

// ===========================================================================
// Vector success tests
// ===========================================================================

func TestGRPCVector_AllMethods_Success(t *testing.T) {
	addr, stop := startVectorServer(t)
	defer stop()
	tr, cleanup := connectVectorTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateNamespace", func(t *testing.T) {
		if _, err := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 128}); err != nil {
			t.Fatalf("CreateNamespace: %v", err)
		}
	})
	t.Run("DeleteNamespace", func(t *testing.T) {
		if _, err := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("DeleteNamespace: %v", err)
		}
	})
	t.Run("ListNamespaces", func(t *testing.T) {
		if _, err := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); err != nil {
			t.Fatalf("ListNamespaces: %v", err)
		}
	})
	t.Run("GetNamespaceInfo", func(t *testing.T) {
		if _, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetNamespaceInfo: %v", err)
		}
	})
	t.Run("UpsertVector", func(t *testing.T) {
		if _, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{0.1, 0.2}, Metadata: map[string]interface{}{"k": "v"}}); err != nil {
			t.Fatalf("UpsertVector: %v", err)
		}
	})
	t.Run("GetVector", func(t *testing.T) {
		if _, err := tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1, IncludeVector: true, IncludeMetadata: true}); err != nil {
			t.Fatalf("GetVector: %v", err)
		}
	})
	t.Run("DeleteVector", func(t *testing.T) {
		if _, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
			t.Fatalf("DeleteVector: %v", err)
		}
	})
	t.Run("BatchUpsert", func(t *testing.T) {
		if _, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{
			Namespace: "ns",
			Vectors:   []*transport.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}}},
		}); err != nil {
			t.Fatalf("BatchUpsert: %v", err)
		}
	})
	t.Run("BatchDeleteVectors", func(t *testing.T) {
		if _, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1, 2}}); err != nil {
			t.Fatalf("BatchDeleteVectors: %v", err)
		}
	})
	t.Run("ListVectors", func(t *testing.T) {
		if _, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns", Limit: 10}); err != nil {
			t.Fatalf("ListVectors: %v", err)
		}
	})
	t.Run("UpdateMetadata", func(t *testing.T) {
		if _, err := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1, Metadata: map[string]interface{}{"k": "v2"}}); err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
	})
	t.Run("SearchSimilar", func(t *testing.T) {
		if _, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{0.1}, K: 5}); err != nil {
			t.Fatalf("SearchSimilar: %v", err)
		}
	})
	t.Run("SearchByMetadata", func(t *testing.T) {
		if _, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns", Filters: map[string]interface{}{"k": "v"}}); err != nil {
			t.Fatalf("SearchByMetadata: %v", err)
		}
	})
	t.Run("HybridSearch", func(t *testing.T) {
		if _, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns", QueryVector: []float32{0.1}, K: 5, MetadataFilter: map[string]interface{}{"k": "v"}}); err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}
	})
	t.Run("SearchByID", func(t *testing.T) {
		if _, err := tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 5}); err != nil {
			t.Fatalf("SearchByID: %v", err)
		}
	})
	t.Run("RebuildIndex", func(t *testing.T) {
		if _, err := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("RebuildIndex: %v", err)
		}
	})
	t.Run("GetRebuildProgress", func(t *testing.T) {
		if _, err := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{JobID: "j1"}); err != nil {
			t.Fatalf("GetRebuildProgress: %v", err)
		}
	})
	t.Run("GetIndexStats", func(t *testing.T) {
		if _, err := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetIndexStats: %v", err)
		}
	})
	t.Run("CompactNamespace", func(t *testing.T) {
		if _, err := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("CompactNamespace: %v", err)
		}
	})
	t.Run("GetCompactionProgress", func(t *testing.T) {
		if _, err := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{OperationID: "op1"}); err != nil {
			t.Fatalf("GetCompactionProgress: %v", err)
		}
	})
}
