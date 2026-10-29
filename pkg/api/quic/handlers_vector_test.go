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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errMockVector is a sentinel error used by mock vector service failures.
var errMockVector = errors.New("mock vector error")

// newVectorServer creates a Server with the given vector service mock and
// nil KV/admin services (unused by vector handlers).
func newVectorServer(vs service.VectorServicer) *Server {
	return NewServer(nil, nil, "localhost:0", WithVector(vs))
}

// ---------------------------------------------------------------------------
// Namespace handlers
// ---------------------------------------------------------------------------

func TestHandleVectorCreateNamespace_Success(t *testing.T) {
	mock := &mockVectorService{
		createNamespaceFn: func(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			if req.Namespace != "test-ns" {
				t.Fatalf("expected namespace 'test-ns', got %q", req.Namespace)
			}
			if req.Dimension != 128 {
				t.Fatalf("expected dimension 128, got %d", req.Dimension)
			}
			return &service.CreateNamespaceResponse{
				Namespace: "test-ns",
				Dimension: 128,
				Timestamp: 1000,
				Success:   true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorCreateNamespaceRequest{
		Namespace:      "test-ns",
		Dimension:      128,
		DistanceMetric: pb.DistanceMetric_DISTANCE_COSINE,
		IndexType:      pb.VectorIndexType_INDEX_HNSW,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := srv.handleVectorCreateNamespace(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorCreateNamespaceResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !out.Created {
		t.Fatal("expected Created=true")
	}
	if out.Timestamp != 1000 {
		t.Fatalf("expected timestamp 1000, got %d", out.Timestamp)
	}
}

func TestHandleVectorCreateNamespace_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		createNamespaceFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorCreateNamespaceRequest{
		Namespace: "test-ns",
		Dimension: 128,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorCreateNamespace(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorCreateNamespace_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.VectorCreateNamespaceRequest{Namespace: "ns"})

	_, err := srv.handleVectorCreateNamespace(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleVectorCreateNamespace_WithIndexParams(t *testing.T) {
	mock := &mockVectorService{
		createNamespaceFn: func(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			if req.IndexConfig == nil {
				t.Fatal("expected non-nil IndexConfig")
			}
			if req.IndexConfig["M"] != "16" {
				t.Fatalf("expected M=16, got %q", req.IndexConfig["M"])
			}
			return &service.CreateNamespaceResponse{Success: true, Timestamp: 2000}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorCreateNamespaceRequest{
		Namespace:       "ns-with-params",
		Dimension:       256,
		IndexParamsJson: `{"M":"16","efConstruction":"200"}`,
	})

	resp, err := srv.handleVectorCreateNamespace(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorCreateNamespaceResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !out.Created {
		t.Fatal("expected Created=true")
	}
}

func TestHandleVectorDeleteNamespace_Success(t *testing.T) {
	mock := &mockVectorService{
		deleteNamespaceFn: func(_ context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
			if req.Namespace != "drop-ns" {
				t.Fatalf("expected namespace 'drop-ns', got %q", req.Namespace)
			}
			return &service.DeleteNamespaceResponse{Success: true}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorDeleteNamespaceRequest{Namespace: "drop-ns"})

	resp, err := srv.handleVectorDeleteNamespace(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleVectorDeleteNamespace_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		deleteNamespaceFn: func(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorDeleteNamespaceRequest{Namespace: "ns"})

	_, err := srv.handleVectorDeleteNamespace(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorListNamespaces_Success(t *testing.T) {
	mock := &mockVectorService{
		listNamespacesFn: func(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
			return &service.ListNamespacesResponse{
				Namespaces: []*service.NamespaceInfo{
					{Namespace: "ns-a"},
					{Namespace: "ns-b"},
				},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorListNamespacesRequest{})

	resp, err := srv.handleVectorListNamespaces(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorListNamespacesResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(out.Namespaces))
	}
	if out.Namespaces[0] != "ns-a" || out.Namespaces[1] != "ns-b" {
		t.Fatalf("unexpected namespace names: %v", out.Namespaces)
	}
}

func TestHandleVectorListNamespaces_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		listNamespacesFn: func(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorListNamespacesRequest{})

	_, err := srv.handleVectorListNamespaces(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorGetNamespaceInfo_Success(t *testing.T) {
	mock := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			if req.Namespace != "info-ns" {
				t.Fatalf("expected namespace 'info-ns', got %q", req.Namespace)
			}
			return &service.GetNamespaceInfoResponse{
				Namespace:      "info-ns",
				Dimension:      384,
				VectorCount:    5000,
				DistanceMetric: "COSINE",
				IndexType:      "HNSW",
				CreatedAt:      1000,
				UpdatedAt:      2000,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{Namespace: "info-ns"})

	resp, err := srv.handleVectorGetNamespaceInfo(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorNamespaceInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Namespace != "info-ns" {
		t.Fatalf("expected namespace 'info-ns', got %q", out.Namespace)
	}
	if out.Dimension != 384 {
		t.Fatalf("expected dimension 384, got %d", out.Dimension)
	}
	if out.VectorCount != 5000 {
		t.Fatalf("expected vector count 5000, got %d", out.VectorCount)
	}
	if out.DistanceMetric != pb.DistanceMetric_DISTANCE_COSINE {
		t.Fatalf("expected DISTANCE_COSINE, got %v", out.DistanceMetric)
	}
	if out.IndexType != pb.VectorIndexType_INDEX_HNSW {
		t.Fatalf("expected INDEX_HNSW, got %v", out.IndexType)
	}
}

func TestHandleVectorGetNamespaceInfo_IndexConfigAndMetadata(t *testing.T) {
	mock := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return &service.GetNamespaceInfoResponse{
				Namespace:   "info-ns",
				Dimension:   4,
				IndexConfig: map[string]string{"ef_construction": "321"},
				Metadata:    map[string]interface{}{"owner": "team-a"},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{Namespace: "info-ns"})

	resp, err := srv.handleVectorGetNamespaceInfo(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.VectorNamespaceInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.GetIndexConfig()["ef_construction"] != "321" {
		t.Fatalf("index_config = %v, want ef_construction=321", out.GetIndexConfig())
	}
	if out.GetMetadataJson() != `{"owner":"team-a"}` {
		t.Fatalf("metadata_json = %q, want {\"owner\":\"team-a\"}", out.GetMetadataJson())
	}
}

func TestHandleVectorGetNamespaceInfo_MetadataMarshalError(t *testing.T) {
	mock := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return &service.GetNamespaceInfoResponse{
				Namespace: "info-ns",
				Metadata:  map[string]interface{}{"bad": make(chan int)},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{Namespace: "info-ns"})

	_, err := srv.handleVectorGetNamespaceInfo(context.Background(), payload)
	var mErr *MetadataMarshalError
	if !errors.As(err, &mErr) {
		t.Fatalf("expected *MetadataMarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGetNamespaceInfo_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{Namespace: "ns"})

	_, err := srv.handleVectorGetNamespaceInfo(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Vector CRUD handlers
// ---------------------------------------------------------------------------

func TestHandleVectorUpsert_Success(t *testing.T) {
	mock := &mockVectorService{
		upsertVectorFn: func(_ context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			if req.Namespace != "ns" {
				t.Fatalf("expected namespace 'ns', got %q", req.Namespace)
			}
			if req.ID != 42 {
				t.Fatalf("expected ID 42, got %d", req.ID)
			}
			if len(req.Vector) != 3 {
				t.Fatalf("expected 3 dims, got %d", len(req.Vector))
			}
			return &service.UpsertVectorResponse{
				ID:        42,
				Version:   1,
				Timestamp: 3000,
				Success:   true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpsertRequest{
		Namespace:    "ns",
		Id:           "42",
		Vector:       []float32{0.1, 0.2, 0.3},
		MetadataJson: `{"category":"test"}`,
	})

	resp, err := srv.handleVectorUpsert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorUpsertResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Version != 1 {
		t.Fatalf("expected version 1, got %d", out.Version)
	}
	if out.Timestamp != 3000 {
		t.Fatalf("expected timestamp 3000, got %d", out.Timestamp)
	}
	if !out.Created {
		t.Fatal("expected Created=true")
	}
}

func TestHandleVectorUpsert_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpsertRequest{
		Namespace: "ns",
		Id:        "1",
		Vector:    []float32{0.1},
	})

	_, err := srv.handleVectorUpsert(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorUpsert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.VectorUpsertRequest{Namespace: "ns", Id: "1"})

	_, err := srv.handleVectorUpsert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleVectorUpsert_InvalidID(t *testing.T) {
	mock := &mockVectorService{
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			t.Fatal("should not reach service")
			return nil, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpsertRequest{
		Namespace: "ns",
		Id:        "not-a-number",
		Vector:    []float32{0.1},
	})

	_, err := srv.handleVectorUpsert(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

func TestHandleVectorGet_Success(t *testing.T) {
	mock := &mockVectorService{
		getVectorFn: func(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			if req.ID != 100 {
				t.Fatalf("expected ID 100, got %d", req.ID)
			}
			return &service.GetVectorResponse{
				ID:        100,
				Vector:    []float32{0.5, 0.6},
				Metadata:  map[string]interface{}{"label": "test"},
				Version:   3,
				CreatedAt: 1000,
				UpdatedAt: 2000,
				Found:     true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetRequest{
		Namespace: "ns",
		Id:        "100",
	})

	resp, err := srv.handleVectorGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorDataProto
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Id != "100" {
		t.Fatalf("expected ID '100', got %q", out.Id)
	}
	if len(out.Vector) != 2 {
		t.Fatalf("expected 2 dims, got %d", len(out.Vector))
	}
	if out.Version != 3 {
		t.Fatalf("expected version 3, got %d", out.Version)
	}
}

func TestHandleVectorGet_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetRequest{Namespace: "ns", Id: "1"})

	_, err := srv.handleVectorGet(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorGet_InvalidID(t *testing.T) {
	mock := &mockVectorService{}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetRequest{Namespace: "ns", Id: "abc"})

	_, err := srv.handleVectorGet(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

func TestHandleVectorDelete_Success(t *testing.T) {
	mock := &mockVectorService{
		deleteVectorFn: func(_ context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
			if req.ID != 77 {
				t.Fatalf("expected ID 77, got %d", req.ID)
			}
			return &service.DeleteVectorResponse{Success: true}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorDeleteRequest{Namespace: "ns", Id: "77"})

	resp, err := srv.handleVectorDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleVectorDelete_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		deleteVectorFn: func(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorDeleteRequest{Namespace: "ns", Id: "1"})

	_, err := srv.handleVectorDelete(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorDelete_InvalidID(t *testing.T) {
	mock := &mockVectorService{}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorDeleteRequest{Namespace: "ns", Id: "bad"})

	_, err := srv.handleVectorDelete(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

func TestHandleVectorBatchUpsert_Success(t *testing.T) {
	mock := &mockVectorService{
		batchUpsertFn: func(_ context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
			if len(req.Vectors) != 2 {
				t.Fatalf("expected 2 vectors, got %d", len(req.Vectors))
			}
			return &service.BatchUpsertResponse{
				InsertedCount: 2,
				Success:       true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorBatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*pb.VectorDataProto{
			{Id: "1", Vector: []float32{0.1, 0.2}},
			{Id: "2", Vector: []float32{0.3, 0.4}},
		},
	})

	resp, err := srv.handleVectorBatchUpsert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorBatchUpsertResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Succeeded != 2 {
		t.Fatalf("expected 2 succeeded, got %d", out.Succeeded)
	}
	if out.Failed != 0 {
		t.Fatalf("expected 0 failed, got %d", out.Failed)
	}
}

func TestHandleVectorBatchUpsert_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		batchUpsertFn: func(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorBatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*pb.VectorDataProto{{Id: "1", Vector: []float32{0.1}}},
	})

	_, err := srv.handleVectorBatchUpsert(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorBatchDelete_Success(t *testing.T) {
	mock := &mockVectorService{
		batchDeleteVectorsFn: func(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			if len(req.IDs) != 3 {
				t.Fatalf("expected 3 IDs, got %d", len(req.IDs))
			}
			return &service.BatchDeleteVectorsResponse{
				DeletedCount: 2,
				FailedIDs:    []uint64{3},
				Success:      true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorBatchDeleteRequest{
		Namespace: "ns",
		Ids:       []string{"1", "2", "3"},
	})

	resp, err := srv.handleVectorBatchDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorBatchDeleteResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Deleted != 2 {
		t.Fatalf("expected 2 deleted, got %d", out.Deleted)
	}
	if out.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", out.Failed)
	}
	if len(out.Errors) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(out.Errors))
	}
	if out.Errors[0].Id != "3" {
		t.Fatalf("expected failed ID '3', got %q", out.Errors[0].Id)
	}
}

func TestHandleVectorBatchDelete_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		batchDeleteVectorsFn: func(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorBatchDeleteRequest{Namespace: "ns", Ids: []string{"1"}})

	_, err := srv.handleVectorBatchDelete(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorBatchDelete_InvalidID(t *testing.T) {
	mock := &mockVectorService{}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorBatchDeleteRequest{Namespace: "ns", Ids: []string{"1", "bad"}})

	_, err := srv.handleVectorBatchDelete(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

func TestHandleVectorList_Success(t *testing.T) {
	mock := &mockVectorService{
		listVectorsFn: func(_ context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			if req.Limit != 10 {
				t.Fatalf("expected limit 10, got %d", req.Limit)
			}
			return &service.ListVectorsResponse{
				Vectors: []*service.VectorData{
					{ID: 1, Vector: []float32{0.1}, Version: 1, CreatedAt: 100, UpdatedAt: 200},
					{ID: 2, Vector: []float32{0.2}, Version: 2, CreatedAt: 300, UpdatedAt: 400},
				},
				NextCursor: "cursor-2",
				TotalCount: 100,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorListRequest{
		Namespace: "ns",
		Limit:     10,
	})

	resp, err := srv.handleVectorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Vectors) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(out.Vectors))
	}
	if out.NextCursor != "cursor-2" {
		t.Fatalf("expected cursor 'cursor-2', got %q", out.NextCursor)
	}
	if out.Total != 100 {
		t.Fatalf("expected total 100, got %d", out.Total)
	}
}

func TestHandleVectorList_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		listVectorsFn: func(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorListRequest{Namespace: "ns"})

	_, err := srv.handleVectorList(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorUpdateMetadata_Success(t *testing.T) {
	mock := &mockVectorService{
		updateMetadataFn: func(_ context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			if req.ID != 55 {
				t.Fatalf("expected ID 55, got %d", req.ID)
			}
			if req.Metadata["tag"] != "updated" {
				t.Fatalf("expected tag=updated, got %v", req.Metadata["tag"])
			}
			return &service.UpdateVectorMetadataResponse{Success: true}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpdateMetadataRequest{
		Namespace:    "ns",
		Id:           "55",
		MetadataJson: `{"tag":"updated"}`,
	})

	resp, err := srv.handleVectorUpdateMetadata(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleVectorUpdateMetadata_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		updateMetadataFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpdateMetadataRequest{
		Namespace: "ns",
		Id:        "1",
	})

	_, err := srv.handleVectorUpdateMetadata(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorUpdateMetadata_InvalidID(t *testing.T) {
	mock := &mockVectorService{}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorUpdateMetadataRequest{Namespace: "ns", Id: "xyz"})

	_, err := srv.handleVectorUpdateMetadata(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Search handlers
// ---------------------------------------------------------------------------

func TestHandleVectorSearchSimilar_Success(t *testing.T) {
	mock := &mockVectorService{
		searchSimilarFn: func(_ context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			if req.K != 5 {
				t.Fatalf("expected K=5, got %d", req.K)
			}
			return &service.SearchSimilarResponse{
				Results: []*service.VectorSearchResult{
					{ID: 10, Score: 0.95, Vector: []float32{0.1}},
					{ID: 20, Score: 0.85, Vector: []float32{0.2}},
				},
				QueryDuration: 1500,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchSimilarRequest{
		Namespace: "ns",
		Vector:    []float32{0.5, 0.5},
		K:         5,
	})

	resp, err := srv.handleVectorSearchSimilar(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorSearchSimilarResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out.Results))
	}
	if out.Results[0].Id != "10" {
		t.Fatalf("expected first result ID '10', got %q", out.Results[0].Id)
	}
	if out.Took != 1500 {
		t.Fatalf("expected took=1500, got %d", out.Took)
	}
}

func TestHandleVectorSearchSimilar_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchSimilarRequest{Namespace: "ns", Vector: []float32{0.1}, K: 1})

	_, err := srv.handleVectorSearchSimilar(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorSearchByMetadata_Success(t *testing.T) {
	mock := &mockVectorService{
		searchByMetadataFn: func(_ context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			if req.Limit != 20 {
				t.Fatalf("expected limit=20, got %d", req.Limit)
			}
			return &service.SearchByMetadataResponse{
				Results: []*service.VectorData{
					{ID: 5, Vector: []float32{0.3}, Version: 1, CreatedAt: 100, UpdatedAt: 200},
				},
				TotalCount: 1,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchByMetadataRequest{
		Namespace:   "ns",
		FiltersJson: `{"category":"doc"}`,
		Limit:       20,
	})

	resp, err := srv.handleVectorSearchByMetadata(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorSearchByMetadataResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	if out.Total != 1 {
		t.Fatalf("expected total=1, got %d", out.Total)
	}
}

func TestHandleVectorSearchByMetadata_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		searchByMetadataFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchByMetadataRequest{Namespace: "ns"})

	_, err := srv.handleVectorSearchByMetadata(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorHybridSearch_Success(t *testing.T) {
	mock := &mockVectorService{
		hybridSearchFn: func(_ context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			if req.K != 10 {
				t.Fatalf("expected K=10, got %d", req.K)
			}
			if req.MetadataFilter["type"] != "image" {
				t.Fatalf("expected filter type=image, got %v", req.MetadataFilter["type"])
			}
			return &service.HybridSearchResponse{
				Results: []*service.VectorSearchResult{
					{ID: 7, Score: 0.9},
				},
				QueryDuration: 2500,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorHybridSearchRequest{
		Namespace:           "ns",
		Vector:              []float32{0.1, 0.2},
		K:                   10,
		MetadataFiltersJson: `{"type":"image"}`,
	})

	resp, err := srv.handleVectorHybridSearch(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorHybridSearchResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	if out.Took != 2500 {
		t.Fatalf("expected took=2500, got %d", out.Took)
	}
}

func TestHandleVectorHybridSearch_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorHybridSearchRequest{Namespace: "ns", Vector: []float32{0.1}, K: 1})

	_, err := srv.handleVectorHybridSearch(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorSearchByID_Success(t *testing.T) {
	mock := &mockVectorService{
		searchByIDFn: func(_ context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			if req.ID != 33 {
				t.Fatalf("expected ID=33, got %d", req.ID)
			}
			if req.K != 3 {
				t.Fatalf("expected K=3, got %d", req.K)
			}
			return &service.SearchByIDResponse{
				SourceID: 33,
				Results: []*service.VectorSearchResult{
					{ID: 34, Score: 0.98},
					{ID: 35, Score: 0.92},
				},
				QueryDuration: 800,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchByIDRequest{
		Namespace: "ns",
		Id:        "33",
		K:         3,
	})

	resp, err := srv.handleVectorSearchByID(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorSearchSimilarResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out.Results))
	}
	if out.Took != 800 {
		t.Fatalf("expected took=800, got %d", out.Took)
	}
}

func TestHandleVectorSearchByID_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchByIDRequest{Namespace: "ns", Id: "1", K: 1})

	_, err := srv.handleVectorSearchByID(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorSearchByID_InvalidID(t *testing.T) {
	mock := &mockVectorService{}
	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorSearchByIDRequest{Namespace: "ns", Id: "not-int", K: 1})

	_, err := srv.handleVectorSearchByID(context.Background(), payload)
	var idErr *VectorIDParseError
	if !errors.As(err, &idErr) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Index management handlers
// ---------------------------------------------------------------------------

func TestHandleVectorRebuildIndex_Success(t *testing.T) {
	mock := &mockVectorService{
		rebuildIndexFn: func(_ context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
			if req.Namespace != "rebuild-ns" {
				t.Fatalf("expected namespace 'rebuild-ns', got %q", req.Namespace)
			}
			return &service.RebuildIndexResponse{
				JobID:     "job-123",
				Timestamp: 5000,
				Success:   true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorRebuildIndexRequest{Namespace: "rebuild-ns"})

	resp, err := srv.handleVectorRebuildIndex(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorRebuildIndexResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.JobId != "job-123" {
		t.Fatalf("expected job ID 'job-123', got %q", out.JobId)
	}
	if !out.Started {
		t.Fatal("expected Started=true")
	}
	if out.Timestamp != 5000 {
		t.Fatalf("expected timestamp 5000, got %d", out.Timestamp)
	}
}

func TestHandleVectorRebuildIndex_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		rebuildIndexFn: func(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorRebuildIndexRequest{Namespace: "ns"})

	_, err := srv.handleVectorRebuildIndex(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorGetRebuildProgress_Success(t *testing.T) {
	mock := &mockVectorService{
		getRebuildProgressFn: func(_ context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
			if req.JobID != "job-456" {
				t.Fatalf("expected job ID 'job-456', got %q", req.JobID)
			}
			return &service.GetRebuildProgressResponse{
				JobID:           "job-456",
				Status:          "running",
				PercentComplete: 65.5,
				StartedAt:       1000,
				CompletedAt:     0,
				Error:           "",
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetRebuildProgressRequest{
		Namespace: "ns",
		JobId:     "job-456",
	})

	resp, err := srv.handleVectorGetRebuildProgress(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorRebuildProgress
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.JobId != "job-456" {
		t.Fatalf("expected job ID 'job-456', got %q", out.JobId)
	}
	if out.Status != "running" {
		t.Fatalf("expected status 'running', got %q", out.Status)
	}
	if out.Progress != 65.5 {
		t.Fatalf("expected progress 65.5, got %f", out.Progress)
	}
}

func TestHandleVectorGetRebuildProgress_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		getRebuildProgressFn: func(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetRebuildProgressRequest{Namespace: "ns"})

	_, err := srv.handleVectorGetRebuildProgress(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorGetIndexStats_Success(t *testing.T) {
	mock := &mockVectorService{
		getIndexStatsFn: func(_ context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
			if req.Namespace != "stats-ns" {
				t.Fatalf("expected namespace 'stats-ns', got %q", req.Namespace)
			}
			return &service.GetIndexStatsResponse{
				Namespace:        "stats-ns",
				IndexType:        "HNSW",
				VectorCount:      10000,
				Dimension:        128,
				IndexSizeBytes:   1048576,
				MemoryUsageBytes: 524288,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetIndexStatsRequest{Namespace: "stats-ns"})

	resp, err := srv.handleVectorGetIndexStats(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorIndexStats
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.VectorCount != 10000 {
		t.Fatalf("expected vector count 10000, got %d", out.VectorCount)
	}
	if out.Dimension != 128 {
		t.Fatalf("expected dimension 128, got %d", out.Dimension)
	}
	if out.MemoryUsage != 524288 {
		t.Fatalf("expected memory usage 524288, got %d", out.MemoryUsage)
	}
	if out.DiskUsage != 1048576 {
		t.Fatalf("expected disk usage 1048576, got %d", out.DiskUsage)
	}
}

func TestHandleVectorGetIndexStats_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		getIndexStatsFn: func(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetIndexStatsRequest{Namespace: "ns"})

	_, err := srv.handleVectorGetIndexStats(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Compaction handlers
// ---------------------------------------------------------------------------

func TestHandleVectorCompactNamespace_Success(t *testing.T) {
	mock := &mockVectorService{
		compactNamespaceFn: func(_ context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
			if req.Namespace != "compact-ns" {
				t.Fatalf("expected namespace 'compact-ns', got %q", req.Namespace)
			}
			return &service.CompactNamespaceResponse{
				OperationID: "op-789",
				Success:     true,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorCompactNamespaceRequest{Namespace: "compact-ns"})

	resp, err := srv.handleVectorCompactNamespace(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorCompactNamespaceResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.JobId != "op-789" {
		t.Fatalf("expected job ID 'op-789', got %q", out.JobId)
	}
	if !out.Started {
		t.Fatal("expected Started=true")
	}
}

func TestHandleVectorCompactNamespace_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		compactNamespaceFn: func(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorCompactNamespaceRequest{Namespace: "ns"})

	_, err := srv.handleVectorCompactNamespace(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

func TestHandleVectorGetCompactionProgress_Success(t *testing.T) {
	completedAt := int64(2000)
	mock := &mockVectorService{
		getCompactionProgressFn: func(_ context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			if req.OperationID != "op-999" {
				t.Fatalf("expected operation ID 'op-999', got %q", req.OperationID)
			}
			return &service.GetCompactionProgressResponse{
				OperationID:     "op-999",
				Namespace:       "ns",
				PercentComplete: 100.0,
				InProgress:      false,
				StartedAt:       1000,
				CompletedAt:     &completedAt,
				Error:           "",
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetCompactionProgressRequest{
		Namespace: "ns",
		JobId:     "op-999",
	})

	resp, err := srv.handleVectorGetCompactionProgress(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorCompactionProgress
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.JobId != "op-999" {
		t.Fatalf("expected job ID 'op-999', got %q", out.JobId)
	}
	if out.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", out.Status)
	}
	if out.Progress != 100.0 {
		t.Fatalf("expected progress 100.0, got %f", out.Progress)
	}
	if out.FinishedAt != 2000 {
		t.Fatalf("expected finished_at 2000, got %d", out.FinishedAt)
	}
}

func TestHandleVectorGetCompactionProgress_InProgress(t *testing.T) {
	mock := &mockVectorService{
		getCompactionProgressFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return &service.GetCompactionProgressResponse{
				OperationID:     "op-111",
				Namespace:       "ns",
				PercentComplete: 50.0,
				InProgress:      true,
				StartedAt:       500,
				CompletedAt:     nil,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetCompactionProgressRequest{
		Namespace: "ns",
		JobId:     "op-111",
	})

	resp, err := srv.handleVectorGetCompactionProgress(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.VectorCompactionProgress
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "running" {
		t.Fatalf("expected status 'running', got %q", out.Status)
	}
	if out.FinishedAt != 0 {
		t.Fatalf("expected finished_at 0 for in-progress, got %d", out.FinishedAt)
	}
}

func TestHandleVectorGetCompactionProgress_ServiceError(t *testing.T) {
	mock := &mockVectorService{
		getCompactionProgressFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return nil, errMockVector
		},
	}

	srv := newVectorServer(mock)
	payload, _ := proto.Marshal(&pb.VectorGetCompactionProgressRequest{Namespace: "ns"})

	_, err := srv.handleVectorGetCompactionProgress(context.Background(), payload)
	if !errors.Is(err, errMockVector) {
		t.Fatalf("expected errMockVector, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Vector error types: VectorIDParseError
// ---------------------------------------------------------------------------

func TestVectorIDParseError_Error(t *testing.T) {
	inner := errors.New("strconv: parsing \"abc\"")
	e := &VectorIDParseError{ID: "abc", Err: inner}
	msg := e.Error()
	if msg != "quic: invalid vector ID abc: strconv: parsing \"abc\"" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestVectorIDParseError_Unwrap(t *testing.T) {
	inner := errors.New("parse error")
	e := &VectorIDParseError{ID: "bad", Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestVectorIDParseError_ErrorsAs(t *testing.T) {
	inner := errors.New("not a number")
	e := &VectorIDParseError{ID: "xyz", Err: inner}
	var target *VectorIDParseError
	if !errors.As(e, &target) {
		t.Fatal("errors.As failed to match *VectorIDParseError")
	}
	if target.ID != "xyz" {
		t.Fatalf("expected ID 'xyz', got %q", target.ID)
	}
}

// ---------------------------------------------------------------------------
// Vector error types: MetadataMarshalError
// ---------------------------------------------------------------------------

func TestMetadataMarshalError_Error(t *testing.T) {
	inner := errors.New("json: unsupported type")
	e := &MetadataMarshalError{Err: inner}
	msg := e.Error()
	if msg != "quic: failed to marshal metadata: json: unsupported type" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestMetadataMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("marshal fail")
	e := &MetadataMarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestMetadataMarshalError_ErrorsAs(t *testing.T) {
	inner := errors.New("encoding issue")
	e := &MetadataMarshalError{Err: inner}
	var target *MetadataMarshalError
	if !errors.As(e, &target) {
		t.Fatal("errors.As failed to match *MetadataMarshalError")
	}
}

// ---------------------------------------------------------------------------
// Vector error types: MetadataUnmarshalError
// ---------------------------------------------------------------------------

func TestMetadataUnmarshalError_Error(t *testing.T) {
	inner := errors.New("invalid character")
	e := &MetadataUnmarshalError{Err: inner}
	msg := e.Error()
	if msg != "quic: failed to unmarshal metadata: invalid character" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestMetadataUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("unmarshal fail")
	e := &MetadataUnmarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestMetadataUnmarshalError_ErrorsAs(t *testing.T) {
	inner := errors.New("bad json")
	e := &MetadataUnmarshalError{Err: inner}
	var target *MetadataUnmarshalError
	if !errors.As(e, &target) {
		t.Fatal("errors.As failed to match *MetadataUnmarshalError")
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: vectorDataToProto
// ---------------------------------------------------------------------------

func TestVectorDataToProto_NilInput(t *testing.T) {
	result, err := vectorDataToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestVectorDataToProto_WithMetadata(t *testing.T) {
	v := &service.VectorData{
		ID:        42,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"color": "blue"},
		Version:   5,
		CreatedAt: 1000,
		UpdatedAt: 2000,
	}
	result, err := vectorDataToProto(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Id != "42" {
		t.Fatalf("expected ID '42', got %q", result.Id)
	}
	if result.MetadataJson == "" {
		t.Fatal("expected non-empty metadata JSON")
	}
	if result.Version != 5 {
		t.Fatalf("expected version 5, got %d", result.Version)
	}
}

func TestVectorDataToProto_EmptyMetadata(t *testing.T) {
	v := &service.VectorData{
		ID:     1,
		Vector: []float32{0.5},
	}
	result, err := vectorDataToProto(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MetadataJson != "" {
		t.Fatalf("expected empty metadata JSON for nil metadata, got %q", result.MetadataJson)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: getVectorResponseToProto
// ---------------------------------------------------------------------------

func TestGetVectorResponseToProto_NilInput(t *testing.T) {
	result, err := getVectorResponseToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestGetVectorResponseToProto_WithMetadata(t *testing.T) {
	v := &service.GetVectorResponse{
		ID:        10,
		Vector:    []float32{0.1, 0.2},
		Metadata:  map[string]interface{}{"key": "val"},
		Version:   3,
		CreatedAt: 100,
		UpdatedAt: 200,
	}
	result, err := getVectorResponseToProto(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Id != "10" {
		t.Fatalf("expected ID '10', got %q", result.Id)
	}
	if result.MetadataJson == "" {
		t.Fatal("expected non-empty metadata JSON")
	}
}

func TestGetVectorResponseToProto_EmptyMetadata(t *testing.T) {
	v := &service.GetVectorResponse{
		ID:     7,
		Vector: []float32{0.5},
	}
	result, err := getVectorResponseToProto(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MetadataJson != "" {
		t.Fatalf("expected empty metadata JSON, got %q", result.MetadataJson)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: protoToVectorData
// ---------------------------------------------------------------------------

func TestProtoToVectorData_InvalidMetadata(t *testing.T) {
	pbV := &pb.VectorDataProto{
		Id:           "1",
		MetadataJson: "{bad-json",
	}
	_, err := protoToVectorData(pbV)
	if err == nil {
		t.Fatal("expected error for invalid metadata JSON")
	}
	var target *MetadataUnmarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataUnmarshalError, got %T: %v", err, err)
	}
}

func TestProtoToVectorData_InvalidID(t *testing.T) {
	pbV := &pb.VectorDataProto{
		Id:           "not-a-number",
		MetadataJson: "",
	}
	_, err := protoToVectorData(pbV)
	if err == nil {
		t.Fatal("expected error for invalid vector ID")
	}
	var target *VectorIDParseError
	if !errors.As(err, &target) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

func TestProtoToVectorData_EmptyID(t *testing.T) {
	pbV := &pb.VectorDataProto{
		Id:     "",
		Vector: []float32{1.0},
	}
	result, err := protoToVectorData(pbV)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != 0 {
		t.Fatalf("expected ID 0 for empty string, got %d", result.ID)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: searchResultToProto
// ---------------------------------------------------------------------------

func TestSearchResultToProto_NilInput(t *testing.T) {
	result, err := searchResultToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestSearchResultToProto_WithMetadata(t *testing.T) {
	r := &service.VectorSearchResult{
		ID:       99,
		Score:    0.95,
		Vector:   []float32{0.1, 0.2},
		Metadata: map[string]interface{}{"tag": "value"},
	}
	result, err := searchResultToProto(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Id != "99" {
		t.Fatalf("expected ID '99', got %q", result.Id)
	}
	if result.MetadataJson == "" {
		t.Fatal("expected non-empty metadata JSON")
	}
}

func TestSearchResultToProto_EmptyMetadata(t *testing.T) {
	r := &service.VectorSearchResult{
		ID:    5,
		Score: 0.5,
	}
	result, err := searchResultToProto(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MetadataJson != "" {
		t.Fatalf("expected empty metadata JSON, got %q", result.MetadataJson)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: marshalMetadata / unmarshalMetadata
// ---------------------------------------------------------------------------

func TestMarshalMetadata_EmptyMap(t *testing.T) {
	result, err := marshalMetadata(map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string for empty map, got %q", result)
	}
}

func TestMarshalMetadata_NilMap(t *testing.T) {
	result, err := marshalMetadata(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string for nil map, got %q", result)
	}
}

func TestMarshalMetadata_ValidMap(t *testing.T) {
	m := map[string]interface{}{"key": "value"}
	result, err := marshalMetadata(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result for valid map")
	}
}

func TestUnmarshalMetadata_EmptyString(t *testing.T) {
	result, err := unmarshalMetadata("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for empty string, got %v", result)
	}
}

func TestUnmarshalMetadata_ValidJSON(t *testing.T) {
	result, err := unmarshalMetadata(`{"key":"value"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "value" {
		t.Fatalf("expected key='value', got %v", result["key"])
	}
}

func TestUnmarshalMetadata_InvalidJSON(t *testing.T) {
	_, err := unmarshalMetadata("{bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var target *MetadataUnmarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataUnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: indexParamsToStringMap
// ---------------------------------------------------------------------------

func TestIndexParamsToStringMap_NilInput(t *testing.T) {
	result := indexParamsToStringMap(nil)
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
	}
}

func TestIndexParamsToStringMap_EmptyInput(t *testing.T) {
	result := indexParamsToStringMap(map[string]interface{}{})
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestIndexParamsToStringMap_WithValues(t *testing.T) {
	params := map[string]interface{}{
		"M":            16,
		"efConstruct":  200,
		"float_param":  3.14,
		"string_param": "hello",
	}
	result := indexParamsToStringMap(params)
	if len(result) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(result))
	}
	if result["M"] != "16" {
		t.Fatalf("expected M='16', got %q", result["M"])
	}
	if result["string_param"] != "hello" {
		t.Fatalf("expected string_param='hello', got %q", result["string_param"])
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: parseStringIDs
// ---------------------------------------------------------------------------

func TestParseStringIDs_ValidIDs(t *testing.T) {
	result, err := parseStringIDs([]string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(result))
	}
	if result[0] != 1 || result[1] != 2 || result[2] != 3 {
		t.Fatalf("unexpected IDs: %v", result)
	}
}

func TestParseStringIDs_InvalidID(t *testing.T) {
	_, err := parseStringIDs([]string{"1", "bad", "3"})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	var target *VectorIDParseError
	if !errors.As(err, &target) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: progressStatusString
// ---------------------------------------------------------------------------

func TestProgressStatusString_Running(t *testing.T) {
	if got := progressStatusString(true); got != "running" {
		t.Fatalf("expected 'running', got %q", got)
	}
}

func TestProgressStatusString_Completed(t *testing.T) {
	if got := progressStatusString(false); got != "completed" {
		t.Fatalf("expected 'completed', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: parseVectorID
// ---------------------------------------------------------------------------

func TestParseVectorID_EmptyString(t *testing.T) {
	id, err := parseVectorID("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 0 {
		t.Fatalf("expected 0, got %d", id)
	}
}

func TestParseVectorID_ValidID(t *testing.T) {
	id, err := parseVectorID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 12345 {
		t.Fatalf("expected 12345, got %d", id)
	}
}

func TestParseVectorID_InvalidID(t *testing.T) {
	_, err := parseVectorID("not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	var target *VectorIDParseError
	if !errors.As(err, &target) {
		t.Fatalf("expected VectorIDParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: formatVectorID
// ---------------------------------------------------------------------------

func TestFormatVectorID_Zero(t *testing.T) {
	if got := formatVectorID(0); got != "0" {
		t.Fatalf("expected '0', got %q", got)
	}
}

func TestFormatVectorID_LargeValue(t *testing.T) {
	if got := formatVectorID(18446744073709551615); got != "18446744073709551615" {
		t.Fatalf("expected max uint64 string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Converter helpers: searchResultsToProto
// ---------------------------------------------------------------------------

func TestSearchResultsToProto_Empty(t *testing.T) {
	result, err := searchResultsToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result))
	}
}

func TestSearchResultsToProto_MultipleResults(t *testing.T) {
	results := []*service.VectorSearchResult{
		{ID: 1, Score: 0.9, Vector: []float32{0.1}},
		{ID: 2, Score: 0.8, Vector: []float32{0.2}},
	}
	pbResults, err := searchResultsToProto(results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pbResults) != 2 {
		t.Fatalf("expected 2 results, got %d", len(pbResults))
	}
	if pbResults[0].Id != "1" {
		t.Fatalf("expected first ID '1', got %q", pbResults[0].Id)
	}
}

// ---------------------------------------------------------------------------
// marshalMetadata error branch (unmarshallable value)
// ---------------------------------------------------------------------------

func TestMarshalMetadata_UnmarshalableValue(t *testing.T) {
	// Channels cannot be marshaled to JSON.
	m := map[string]interface{}{"bad": make(chan int)}
	_, err := marshalMetadata(m)
	if err == nil {
		t.Fatal("expected error for un-marshalable value")
	}
	var target *MetadataMarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// vectorDataToProto metadata error propagation
// ---------------------------------------------------------------------------

func TestVectorDataToProto_MetadataError(t *testing.T) {
	v := &service.VectorData{
		ID:       1,
		Vector:   []float32{0.1},
		Metadata: map[string]interface{}{"bad": make(chan int)},
	}
	_, err := vectorDataToProto(v)
	if err == nil {
		t.Fatal("expected error for un-marshalable metadata")
	}
	var target *MetadataMarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// getVectorResponseToProto metadata error propagation
// ---------------------------------------------------------------------------

func TestGetVectorResponseToProto_MetadataError(t *testing.T) {
	v := &service.GetVectorResponse{
		ID:       1,
		Vector:   []float32{0.1},
		Metadata: map[string]interface{}{"bad": make(chan int)},
	}
	_, err := getVectorResponseToProto(v)
	if err == nil {
		t.Fatal("expected error for un-marshalable metadata")
	}
	var target *MetadataMarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// searchResultToProto metadata error propagation
// ---------------------------------------------------------------------------

func TestSearchResultToProto_MetadataError(t *testing.T) {
	r := &service.VectorSearchResult{
		ID:       1,
		Score:    0.9,
		Metadata: map[string]interface{}{"bad": make(chan int)},
	}
	_, err := searchResultToProto(r)
	if err == nil {
		t.Fatal("expected error for un-marshalable metadata")
	}
	var target *MetadataMarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// searchResultsToProto error propagation
// ---------------------------------------------------------------------------

func TestSearchResultsToProto_ErrorPropagation(t *testing.T) {
	results := []*service.VectorSearchResult{
		{ID: 1, Score: 0.9, Metadata: map[string]interface{}{"bad": make(chan int)}},
	}
	_, err := searchResultsToProto(results)
	if err == nil {
		t.Fatal("expected error to propagate from searchResultToProto")
	}
	var target *MetadataMarshalError
	if !errors.As(err, &target) {
		t.Fatalf("expected MetadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Unmarshal error tests for vector handlers
// ---------------------------------------------------------------------------

func TestHandleVectorCreateNamespace_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		createNamespaceFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorCreateNamespace(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorDeleteNamespace_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		deleteNamespaceFn: func(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorDeleteNamespace(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorListNamespaces_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		listNamespacesFn: func(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorListNamespaces(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGetNamespaceInfo_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorGetNamespaceInfo(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorUpsert_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorUpsert(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGet_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorGet(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorDelete_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		deleteVectorFn: func(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorDelete(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorBatchUpsert_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		batchUpsertFn: func(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorBatchUpsert(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorBatchDelete_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		batchDeleteVectorsFn: func(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorBatchDelete(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorList_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		listVectorsFn: func(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorList(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorUpdateMetadata_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		updateMetadataFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorUpdateMetadata(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorSearchSimilar_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorSearchSimilar(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorSearchByMetadata_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		searchByMetadataFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorSearchByMetadata(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorHybridSearch_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorHybridSearch(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorSearchByID_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorSearchByID(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorRebuildIndex_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		rebuildIndexFn: func(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorRebuildIndex(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGetRebuildProgress_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		getRebuildProgressFn: func(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorGetRebuildProgress(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGetIndexStats_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		getIndexStatsFn: func(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorGetIndexStats(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorCompactNamespace_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		compactNamespaceFn: func(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorCompactNamespace(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleVectorGetCompactionProgress_UnmarshalError(t *testing.T) {
	srv := newVectorServer(&mockVectorService{
		getCompactionProgressFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleVectorGetCompactionProgress(context.Background(), []byte("bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}
