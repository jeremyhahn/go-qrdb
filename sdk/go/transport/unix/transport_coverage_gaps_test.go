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

// Targeted coverage tests that exercise rich branches in the Unix transport
// that the basic round-trip tests don't touch — primarily error paths in
// proto encoders, the TSRichQuery aggregated/grouped branches, and the
// per-method service error paths for MVCC, Batch, and Vector services.

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// TSRichQuery: aggregated value + grouped results
// ---------------------------------------------------------------------------

func TestUnixRT_TSRichQuery_AggregatedAndGrouped(t *testing.T) {
	aggValue := 99.9
	groupAgg := 42.0
	svc := &mockTimeSeriesSvc{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return &timeseries.QueryResult{
				Metric:          req.Metric,
				Tags:            map[string]string{"host": "h1"},
				AggregatedValue: &aggValue,
				GroupedResults: []*timeseries.GroupedResult{
					{
						Tags:            map[string]string{"region": "us-east"},
						AggregatedValue: &groupAgg,
					},
					{
						Tags: map[string]string{"region": "us-west"},
					},
				},
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		Aggregation: transport.AggAvg,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.AggregatedValue == nil || *resp.AggregatedValue != 99.9 {
		t.Errorf("got AggregatedValue=%v, want 99.9", resp.AggregatedValue)
	}
	if len(resp.GroupedResults) != 2 {
		t.Fatalf("got %d grouped results, want 2", len(resp.GroupedResults))
	}
	if resp.GroupedResults[0].AggregatedValue == nil || *resp.GroupedResults[0].AggregatedValue != 42.0 {
		t.Errorf("group[0] aggregation: %+v", resp.GroupedResults[0].AggregatedValue)
	}
	if resp.GroupedResults[1].AggregatedValue != nil {
		t.Errorf("group[1] should have no aggregation, got %+v", resp.GroupedResults[1].AggregatedValue)
	}
}

func TestUnixRT_TSRichQuery_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesSvc{
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return nil, errors.New("query failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// Vector: error injection on metadata-bearing methods so the JSON marshal
// branches are exercised and the service-error path is taken.
// ---------------------------------------------------------------------------

func TestUnixRT_UpsertVector_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		upsertFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, errors.New("upsert failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{0.1},
		Metadata:  map[string]interface{}{"a": "b"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_CreateNamespace_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		createNSFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, errors.New("create namespace failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace:   "ns",
		Dimension:   128,
		IndexConfig: map[string]string{"m": "16"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_BatchDeleteVectors_WithFailedIDs(t *testing.T) {
	svc := &mockVectorSvc{
		batchDeleteFn: func(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return &service.BatchDeleteVectorsResponse{
				DeletedCount: len(req.IDs) - 1,
				FailedIDs:    []uint64{99},
				Success:      false,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "ns",
		IDs:       []uint64{1, 2, 99},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if len(resp.FailedIDs) != 1 || resp.FailedIDs[0] != 99 {
		t.Errorf("got FailedIDs=%v, want [99]", resp.FailedIDs)
	}
}

func TestUnixRT_UpdateMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		updateMetaFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, errors.New("update metadata failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "ns",
		ID:        1,
		Metadata:  map[string]interface{}{"k": "v"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_SearchByMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		searchByMetaFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, errors.New("search by metadata failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "ns",
		Filters:   map[string]interface{}{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_HybridSearch_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, errors.New("hybrid search failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace:      "ns",
		MetadataFilter: map[string]interface{}{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_SearchByID_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, errors.New("search by id failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{Namespace: "ns", ID: 1})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_GetCompactionProgress_Completed(t *testing.T) {
	completedAt := int64(1_700_000_100)
	svc := &mockVectorSvc{
		compactProgFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return &service.GetCompactionProgressResponse{
				OperationID:     "comp-1",
				InProgress:      false,
				PercentComplete: 100.0,
				StartedAt:       1_700_000_000,
				CompletedAt:     &completedAt,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{
		Namespace:   "ns",
		OperationID: "comp-1",
	})
	if err != nil {
		t.Fatalf("GetCompactionProgress: %v", err)
	}
	if resp.InProgress {
		t.Error("expected InProgress=false")
	}
	if resp.CompletedAt == nil || *resp.CompletedAt != completedAt {
		t.Errorf("got CompletedAt=%v, want %d", resp.CompletedAt, completedAt)
	}
}

// ---------------------------------------------------------------------------
// Document operations: error paths on the round trip
// ---------------------------------------------------------------------------

func TestUnixRT_InsertDocument_ServiceError(t *testing.T) {
	svc := &mockCollectionSvc{
		insertFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, errors.New("insert failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	_, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{
		Database:   "main",
		Collection: "users",
		Document:   map[string]interface{}{"name": "alice"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_GetDocument_ServiceError(t *testing.T) {
	svc := &mockCollectionSvc{
		getDocFn: func(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return nil, errors.New("document not found")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	_, err := tr.GetDocument(context.Background(), "main", "users", 5)
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_UpdateDocument_ServiceError(t *testing.T) {
	svc := &mockCollectionSvc{
		updateFn: func(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			return nil, errors.New("update failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{
		Database:   "main",
		Collection: "users",
		ID:         5,
		Document:   map[string]interface{}{"name": "alice"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// applyWriteMetadata / applyReadMetadata: exercise non-default branches by
// invoking transport methods with options.
// ---------------------------------------------------------------------------

func TestUnixRT_Put_WithWriteOption(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	// WithWriteMode adds a metadata header; the server doesn't enforce it
	// but the SDK code path must be exercised.
	err := tr.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(transport.WriteModeSync))
	if err != nil {
		t.Fatalf("Put with mode: %v", err)
	}
}

func TestUnixRT_Get_WithReadOption(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := tr.Get(ctx, "k", transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("Get with consistency: %v", err)
	}
}

func TestUnixRT_Get_WithStaleRead(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := tr.Get(ctx, "k", transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get with stale: %v", err)
	}
}

func TestUnixRT_Get_WithReadTimeout(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := tr.Get(ctx, "k", transport.WithReadTimeout(5000))
	if err != nil {
		t.Fatalf("Get with timeout: %v", err)
	}
}
