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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// handleIndexList (0% coverage)
// ---------------------------------------------------------------------------

func TestHandleIndexList_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return []string{"email_idx", "name_idx"}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))

	resp, err := srv.handleIndexList(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleIndexList returned unexpected error: %v", err)
	}

	var out pb.IndexListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("failed to unmarshal IndexListResponse: %v", err)
	}
	if len(out.GetNames()) != 2 {
		t.Errorf("expected 2 names, got %d", len(out.GetNames()))
	}
}

func TestHandleIndexList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list indexes failed"}
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))

	_, err := srv.handleIndexList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from handleIndexList, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleIndexList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleIndexList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil index service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "index" {
		t.Errorf("expected service %q, got %q", "index", snc.Service)
	}
}

// ---------------------------------------------------------------------------
// handleAssignRole — missing error paths
// ---------------------------------------------------------------------------

func TestHandleAssignRole_UserNotFound(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("role-x", "admin", "t"), nil
		},
	}
	userErr := &testError{msg: "user not found"}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, userErr
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 9999, RoleID: "role-x"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for user not found, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleAssignRole_UpdateUserError(t *testing.T) {
	mockRole := &mockRoleService{
		getRoleFn: func(_ context.Context, _ string) (*role.Role, error) {
			return testRole("role-z", "viewer", "t"), nil
		},
	}
	updateErr := &testError{msg: "update user failed"}
	mockUser := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return testUser(99, "charlie", "t"), nil
		},
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, updateErr
		},
	}

	srv := newRoleServer(mockRole, mockUser)
	payload, _ := json.Marshal(assignRoleWireRequest{UserID: 99, RoleID: "role-z"})

	_, err := srv.handleAssignRole(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from UpdateUser, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorGet — getVectorResponseToProto error (unmarshalable metadata)
// ---------------------------------------------------------------------------

func TestHandleVectorGet_MetadataError(t *testing.T) {
	mock := &mockVectorService{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return &service.GetVectorResponse{
				ID:     1,
				Vector: []float32{0.1, 0.2},
				// channel value is unmarshalable by json.Marshal
				Metadata: map[string]interface{}{
					"ch": make(chan int),
				},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorGetRequest{
		Namespace: "ns",
		Id:        "1",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for unmarshalable metadata, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleVectorSearchSimilar — searchResultsToProto error path
// ---------------------------------------------------------------------------

func TestHandleVectorSearchSimilar_MetadataError(t *testing.T) {
	mock := &mockVectorService{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return &service.SearchSimilarResponse{
				Results: []*service.VectorSearchResult{
					{
						ID:     1,
						Score:  0.95,
						Vector: []float32{0.1},
						Metadata: map[string]interface{}{
							"bad": make(chan int),
						},
					},
				},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorSearchSimilarRequest{
		Namespace: "ns",
		Vector:    []float32{0.1, 0.2},
		K:         5,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorSearchSimilar(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for unmarshalable metadata, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleVectorHybridSearch — searchResultsToProto error path (77.8%)
// ---------------------------------------------------------------------------

func TestHandleVectorHybridSearch_MetadataError(t *testing.T) {
	mock := &mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return &service.HybridSearchResponse{
				Results: []*service.VectorSearchResult{
					{
						ID:     2,
						Score:  0.88,
						Vector: []float32{0.5},
						Metadata: map[string]interface{}{
							"fn": func() {},
						},
					},
				},
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorHybridSearchRequest{
		Namespace: "ns",
		Vector:    []float32{0.5},
		K:         3,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorHybridSearch(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for unmarshalable metadata, got nil")
	}
}

func TestHandleVectorHybridSearch_InvalidMetadataFilter(t *testing.T) {
	mock := &mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return &service.HybridSearchResponse{}, nil
		},
	}

	srv := newVectorServer(mock)
	// Invalid JSON for metadata_filters_json triggers unmarshalMetadata error.
	payload, err := proto.Marshal(&pb.VectorHybridSearchRequest{
		Namespace:           "ns",
		Vector:              []float32{0.1},
		MetadataFiltersJson: "{bad json",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorHybridSearch(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for invalid metadata filter JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleVectorList — NilService (86.7% → missing nil service test)
// ---------------------------------------------------------------------------

func TestHandleVectorList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorListRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorGetNamespaceInfo — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorGetNamespaceInfo_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorGetNamespaceInfo(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorBatchUpsert — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorBatchUpsert_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorBatchUpsertRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorBatchUpsert(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorSearchByID — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByID_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorSearchByIDRequest{
		Namespace: "ns",
		Id:        "1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorSearchByID(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorRebuildIndex — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorRebuildIndex_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorRebuildIndexRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorRebuildIndex(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorGetRebuildProgress — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorGetRebuildProgress_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorGetRebuildProgressRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorGetRebuildProgress(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorGetIndexStats — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorGetIndexStats_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorGetIndexStatsRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorGetIndexStats(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorCompactNamespace — NilService (83.3%)
// ---------------------------------------------------------------------------

func TestHandleVectorCompactNamespace_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorCompactNamespaceRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorCompactNamespace(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleVectorSearchByMetadata — vectorDataToProto error path (81.0%)
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByMetadata_MetadataError(t *testing.T) {
	mock := &mockVectorService{
		searchByMetadataFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return &service.SearchByMetadataResponse{
				Results: []*service.VectorData{
					{
						ID:     3,
						Vector: []float32{0.3},
						Metadata: map[string]interface{}{
							"fn": func() {},
						},
					},
				},
				TotalCount: 1,
			}, nil
		},
	}

	srv := newVectorServer(mock)
	payload, err := proto.Marshal(&pb.VectorSearchByMetadataRequest{
		Namespace: "ns",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleVectorSearchByMetadata(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for unmarshalable metadata, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleTSQuery — unmarshal error (87.5%)
// ---------------------------------------------------------------------------

func TestHandleTSQuery_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	_, err := srv.handleTSQuery(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleTSQuery_WithTags(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			resolved := timeseries.ApplyTSQueryOptions(opts)
			if len(resolved.Tags) == 0 {
				t.Error("expected tags to be set")
			}
			return []timeseries.DataPoint{}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 1000,
		EndNs:   2000,
		Tags:    []*pb.TSTag{{Key: "host", Value: "web-01"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleTSQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTSRichQuery — grouped results path (94.1%)
// ---------------------------------------------------------------------------

func TestHandleTSRichQuery_GroupedResults(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return &timeseries.QueryResult{
				Metric: "cpu",
				GroupedResults: []*timeseries.GroupedResult{
					{
						Tags:   map[string]string{"host": "web-01"},
						Points: []*timeseries.DataPoint{{Metric: "cpu", Value: 0.5}},
					},
					{
						Tags:   map[string]string{"host": "web-02"},
						Points: []*timeseries.DataPoint{{Metric: "cpu", Value: 0.7}},
					},
				},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	wireReq := tsRichQueryWireRequest{
		Metric:  "cpu",
		GroupBy: []string{"host"},
	}
	payload, _ := json.Marshal(wireReq)

	resp, err := srv.handleTSRichQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wireResp tsRichQueryWireResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(wireResp.GroupedResults) != 2 {
		t.Errorf("expected 2 grouped results, got %d", len(wireResp.GroupedResults))
	}
}

// ---------------------------------------------------------------------------
// handleVectorGetCompactionProgress — NilService (86.7%)
// ---------------------------------------------------------------------------

func TestHandleVectorGetCompactionProgress_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, err := proto.Marshal(&pb.VectorGetCompactionProgressRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, err = srv.handleVectorGetCompactionProgress(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil vector service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Server.Start — ListenAddr error path (92.3%)
// ---------------------------------------------------------------------------

// TestServer_Start_InvalidAddress verifies that Start returns a *StartError
// wrapping the bind error when ListenAddr cannot bind the address.
func TestServer_Start_InvalidAddress(t *testing.T) {
	// Provide a minimal TLS config so we pass the nil-TLS guard.
	// Use an address with a port that cannot be bound (port 0 succeeds on most
	// systems; use an explicitly invalid host:port instead).
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "256.256.256.256:9999",
		WithTLS(tlsCfg))

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error when binding invalid address")
	}

	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StartError, got %T: %v", err, err)
	}
}
