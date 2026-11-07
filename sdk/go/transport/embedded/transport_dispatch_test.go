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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedDispatch_HandlersBadRequest checks that every dispatch handler
// with a typed request refuses a request of the wrong type with the typed
// error naming its method.
func TestEmbeddedDispatch_HandlersBadRequest(t *testing.T) {
	tr := newExtTransport(t,
		WithLeaseService(&mockLeaseService{}),
		WithTimeSeriesService(&mockTSService{}),
		WithWatchService(&mockWatchService{}),
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	ctx := context.Background()
	bad := "not-the-right-type"
	cases := []string{
		"PutWithLease", "GetRevision", "GetAtRevision", "Range", "Compact",
		"ExecuteTransaction", "ExecuteBatch", "Watch", "WatchPrefix",
		"LeaseGrant", "LeaseRevoke", "LeaseKeepAlive", "LeaseTimeToLive",
		"TSWrite", "TSWriteBatch", "TSQuery", "TSAggregate", "TSWritePoint",
		"TSRichQuery", "TSDeleteSeries", "TSGetRetention", "TSSetRetention",
		"TSListMetrics",
		"CreateNamespace", "DeleteNamespace", "ListNamespaces", "GetNamespaceInfo",
		"UpsertVector", "GetVector", "DeleteVector", "BatchUpsert", "BatchDeleteVectors",
		"ListVectors", "UpdateMetadata", "SearchSimilar", "SearchByMetadata",
		"HybridSearch", "SearchByID", "RebuildIndex", "GetRebuildProgress",
		"GetIndexStats", "CompactNamespace", "GetCompactionProgress",
		"RetireJoinIdentity",
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			err := tr.Request(ctx, m, bad, nil)
			var ie *InvalidRequestError
			if !errors.As(err, &ie) {
				t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
			}
			if ie.Method != m || !strings.Contains(err.Error(), m) {
				t.Errorf("error = %q (method %q), want it to name %q", err, ie.Method, m)
			}
		})
	}
}

// TestEmbeddedDispatch_UnknownMethod checks that Request refuses a method it
// has no handler for with the typed error naming that method.
func TestEmbeddedDispatch_UnknownMethod(t *testing.T) {
	tr := newExtTransport(t)
	const m = "TotallyMadeUpMethod"
	err := tr.Request(context.Background(), m, nil, nil)
	var ue *UnknownMethodError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnknownMethodError, got %T: %v", err, err)
	}
	if ue.Method != m || !strings.Contains(err.Error(), m) {
		t.Errorf("error = %q (method %q), want it to name %q", err, ue.Method, m)
	}
}

// dispatchRow is one Request call: the method, its request, and the
// response pointer the handler fills (nil when the method has none).
type dispatchRow struct {
	method string
	req    any
	out    any
}

// TestEmbeddedDispatch_ServiceOutcomes drives handlers through Request
// against a service that answers, one that fails, and one that is absent,
// and checks each outcome reaches the caller: the answer in out, the
// service's own error, or a NilServiceError.
func TestEmbeddedDispatch_ServiceOutcomes(t *testing.T) {
	ctx := context.Background()

	kv := newMockKVService()
	kv.store["testkey"] = []byte("testval")
	answering, err := New(kv, &mockAdminService{}, WithVectorService(&mockVectorService{}), WithLeaseService(&mockLeaseService{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	failing, err := New(&errorKVService{*newMockKVService()}, &errorAdminService{},
		WithVectorService(&errVectorService{}),
		WithTimeSeriesService(&errTSSvc{}),
		WithLeaseService(failingLeaseService(errSentinel)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	absent := newExtTransport(t)

	answers := []dispatchRow{
		{"Get", &getRequest{Key: "testkey"}, new([]byte)},
		{"List", &listRequest{}, new([]string)},
		{"GetShardState", uint64(1), new(*transport.ShardStateResponse)},
		{"GetTopology", nil, new(*transport.TopologyResponse)},
		{"LeaseGrant", &leaseGrantRequest{TTL: 60}, new(*transport.LeaseInfo)},
		{"LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}, new(*transport.LeaseInfo)},
		{"LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}, new(*transport.LeaseInfo)},
		{"LeaseList", nil, new([]*transport.LeaseInfo)},
		{"CreateNamespace", &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 128}, new(*transport.CreateNamespaceResponse)},
		{"DeleteNamespace", &transport.DeleteNamespaceRequest{Namespace: "ns"}, new(*transport.DeleteNamespaceResponse)},
		{"ListNamespaces", &transport.ListNamespacesRequest{}, new(*transport.ListNamespacesResponse)},
		{"GetNamespaceInfo", &transport.GetNamespaceInfoRequest{Namespace: "ns"}, new(*transport.GetNamespaceInfoResponse)},
		{"UpsertVector", &transport.UpsertVectorRequest{Namespace: "ns", Vector: []float32{0.1}}, new(*transport.UpsertVectorResponse)},
		{"GetVector", &transport.GetVectorRequest{Namespace: "ns", ID: 1}, new(*transport.GetVectorResponse)},
		{"DeleteVector", &transport.DeleteVectorRequest{Namespace: "ns", ID: 1}, new(*transport.DeleteVectorResponse)},
		{"BatchUpsert", &transport.BatchUpsertRequest{Namespace: "ns"}, new(*transport.BatchUpsertResponse)},
		{"BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{Namespace: "ns"}, new(*transport.BatchDeleteVectorsResponse)},
		{"ListVectors", &transport.ListVectorsRequest{Namespace: "ns"}, new(*transport.ListVectorsResponse)},
		{"UpdateMetadata", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}, new(*transport.UpdateVectorMetadataResponse)},
		{"SearchSimilar", &transport.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{0.1}}, new(*transport.SearchSimilarResponse)},
		{"SearchByMetadata", &transport.SearchByMetadataRequest{Namespace: "ns"}, new(*transport.SearchByMetadataResponse)},
		{"HybridSearch", &transport.HybridSearchRequest{Namespace: "ns", QueryVector: []float32{0.1}}, new(*transport.HybridSearchResponse)},
		{"SearchByID", &transport.SearchByIDRequest{Namespace: "ns", ID: 1}, new(*transport.SearchByIDResponse)},
		{"GetRebuildProgress", &transport.GetRebuildProgressRequest{Namespace: "ns"}, new(*transport.GetRebuildProgressResponse)},
		{"GetIndexStats", &transport.GetIndexStatsRequest{Namespace: "ns"}, new(*transport.GetIndexStatsResponse)},
		{"GetCompactionProgress", &transport.GetCompactionProgressRequest{Namespace: "ns"}, new(*transport.GetCompactionProgressResponse)},
	}
	failures := []dispatchRow{
		{"Get", &getRequest{Key: "k"}, nil},
		{"List", &listRequest{}, nil},
		{"Health", nil, nil},
		{"Status", nil, nil},
		{"Capabilities", nil, nil},
		{"StartShard", &transport.StartShardRequest{}, nil},
		{"StopShard", &transport.StopShardRequest{ShardID: 1}, nil},
		{"GetShardState", uint64(1), nil},
		{"GetTopology", nil, nil},
		{"TSDeleteSeries", &transport.TSDeleteRequest{Metric: "cpu"}, nil},
		{"TSGetRetention", &transport.TSGetRetentionRequest{Metric: "cpu"}, nil},
		{"TSSetRetention", &transport.TSSetRetentionRequest{Metric: "cpu"}, nil},
		{"TSListMetrics", &transport.TSListMetricsRequest{}, nil},
		{"LeaseGrant", &leaseGrantRequest{TTL: 60}, nil},
		{"LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}, nil},
		{"LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}, nil},
		{"LeaseRevoke", &leaseRevokeRequest{LeaseID: 1}, nil},
		{"LeaseList", nil, nil},
		{"CreateNamespace", &transport.CreateNamespaceRequest{}, nil},
		{"DeleteNamespace", &transport.DeleteNamespaceRequest{}, nil},
		{"ListNamespaces", &transport.ListNamespacesRequest{}, nil},
		{"GetNamespaceInfo", &transport.GetNamespaceInfoRequest{}, nil},
		{"UpsertVector", &transport.UpsertVectorRequest{}, nil},
		{"GetVector", &transport.GetVectorRequest{}, nil},
		{"DeleteVector", &transport.DeleteVectorRequest{}, nil},
		{"BatchUpsert", &transport.BatchUpsertRequest{}, nil},
		{"BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{}, nil},
		{"ListVectors", &transport.ListVectorsRequest{}, nil},
		{"UpdateMetadata", &transport.UpdateVectorMetadataRequest{}, nil},
		{"SearchSimilar", &transport.SearchSimilarRequest{}, nil},
		{"SearchByMetadata", &transport.SearchByMetadataRequest{}, nil},
		{"HybridSearch", &transport.HybridSearchRequest{}, nil},
		{"SearchByID", &transport.SearchByIDRequest{}, nil},
		{"RebuildIndex", &transport.RebuildIndexRequest{}, nil},
		{"GetRebuildProgress", &transport.GetRebuildProgressRequest{}, nil},
		{"GetIndexStats", &transport.GetIndexStatsRequest{}, nil},
		{"CompactNamespace", &transport.CompactNamespaceRequest{}, nil},
		{"GetCompactionProgress", &transport.GetCompactionProgressRequest{}, nil},
	}
	absences := []dispatchRow{
		{"CredentialList", &transport.CredentialListRequest{}, nil},
		{"CredentialRotate", &transport.CredentialRotateRequest{Name: "k", NewValue: []byte("v")}, nil},
		{"CredentialListVersions", &transport.CredentialListVersionsRequest{Name: "k"}, nil},
		{"CredentialGCExpired", &transport.CredentialGCRequest{Name: "k"}, nil},
		{"TSQuery", &transport.TSQueryRequest{Metric: "cpu"}, nil},
		{"TSAggregate", &transport.TSAggregateRequest{Metric: "cpu"}, nil},
		{"TSRichQuery", &transport.TSRichQueryRequest{Metric: "cpu"}, nil},
		{"TSDeleteSeries", &transport.TSDeleteRequest{Metric: "cpu"}, nil},
		{"TSGetRetention", &transport.TSGetRetentionRequest{Metric: "cpu"}, nil},
		{"TSSetRetention", &transport.TSSetRetentionRequest{Metric: "cpu"}, nil},
		{"TSListMetrics", &transport.TSListMetricsRequest{}, nil},
		{"LeaseGrant", &leaseGrantRequest{TTL: 60}, nil},
		{"LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}, nil},
		{"LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}, nil},
		{"LeaseRevoke", &leaseRevokeRequest{LeaseID: 1}, nil},
		{"PutWithLease", &putWithLeaseRequest{Key: "k", Value: []byte("v"), LeaseID: 1}, nil},
		{"GetRevision", &getRevisionRequest{Key: "k"}, nil},
		{"GetAtRevision", &getAtRevisionRequest{Key: "k", Revision: 1}, nil},
		{"Range", &rangeRequest{Start: []byte("a"), End: []byte("z")}, nil},
		{"IndexQuery", &indexQueryRequest{IndexName: "idx", Value: "v"}, nil},
		{"IndexScan", &indexScanRequest{IndexName: "idx", Start: spIdx("a"), End: spIdx("z")}, nil},
		{"ExecuteTransaction", &transport.TransactionRequest{}, nil},
		{"ExecuteBatch", &transport.BatchRequest{}, nil},
		{"Watch", &watchRequest{Key: "k"}, nil},
		{"WatchPrefix", &watchPrefixRequest{Prefix: "p"}, nil},
	}

	for _, tc := range answers {
		t.Run("answers/"+tc.method, func(t *testing.T) {
			if err := answering.Request(ctx, tc.method, tc.req, tc.out); err != nil {
				t.Fatalf("Request: %v", err)
			}
			if reflect.ValueOf(tc.out).Elem().IsZero() {
				t.Fatalf("Request left %T unset", tc.out)
			}
		})
	}
	for _, tc := range failures {
		t.Run("fails/"+tc.method, func(t *testing.T) {
			if err := failing.Request(ctx, tc.method, tc.req, nil); !errors.Is(err, errSentinel) {
				t.Fatalf("Request error = %v, want the service's error", err)
			}
		})
	}
	// Exists has no Request route, so its consistent read fails on the client.
	t.Run("fails/Exists", func(t *testing.T) {
		_, err := failing.Exists(ctx, "k", transport.WithConsistency(transport.ConsistencyLinearizable))
		if !errors.Is(err, errSentinel) {
			t.Fatalf("Exists error = %v, want the service's error", err)
		}
	})
	for _, tc := range absences {
		t.Run("absent/"+tc.method, func(t *testing.T) {
			var nse *NilServiceError
			if err := absent.Request(ctx, tc.method, tc.req, nil); !errors.As(err, &nse) {
				t.Fatalf("Request error = %T %v, want NilServiceError", err, err)
			}
		})
	}
}

// TestEmbeddedLease_Delegates drives every lease verb through a service that
// answers and through one that fails: the transport converts the answer and
// passes the failure through.
func TestEmbeddedLease_Delegates(t *testing.T) {
	ctx := t.Context()
	tests := []struct {
		name string
		call func(t *testing.T, tr *Transport) error
	}{
		{"LeaseGrant", func(t *testing.T, tr *Transport) error {
			info, err := tr.LeaseGrant(ctx, 60)
			if err == nil && (info.ID != 42 || info.TTL != 60) {
				t.Errorf("info = %+v", info)
			}
			return err
		}},
		{"LeaseRevoke", func(_ *testing.T, tr *Transport) error {
			return tr.LeaseRevoke(ctx, 1)
		}},
		{"LeaseKeepAlive", func(t *testing.T, tr *Transport) error {
			info, err := tr.LeaseKeepAlive(ctx, 7)
			if err == nil && info.ID != 7 {
				t.Errorf("ID = %d", info.ID)
			}
			return err
		}},
		{"LeaseTimeToLive", func(t *testing.T, tr *Transport) error {
			info, err := tr.LeaseTimeToLive(ctx, 7)
			if err == nil && info.ID != 7 {
				t.Errorf("ID = %d", info.ID)
			}
			return err
		}},
		{"LeaseList", func(t *testing.T, tr *Transport) error {
			leases, err := tr.LeaseList(ctx)
			if err == nil && len(leases) != 2 {
				t.Errorf("len = %d, want 2", len(leases))
			}
			return err
		}},
	}
	ok := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	bad := newExtTransport(t, WithLeaseService(failingLeaseService(errSentinel)))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(t, ok); err != nil {
				t.Fatalf("answering service: %v", err)
			}
			if err := tt.call(t, bad); !errors.Is(err, errSentinel) {
				t.Fatalf("failing service: err = %v, want the service's error", err)
			}
		})
	}
}

// mockLeaseService answers every lease call with a fixed result; a non-nil
// Fn field replaces the answer.
type mockLeaseService struct {
	grantFn     func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn    func(ctx context.Context, id int64) error
	keepAliveFn func(ctx context.Context, id int64) (*lease.Lease, error)
	ttlFn       func(ctx context.Context, id int64) (*lease.Lease, error)
	leasesFn    func(ctx context.Context) ([]*lease.Lease, error)
}

var _ lease.LeaseServicer = (*mockLeaseService)(nil)

// failingLeaseService returns a mockLeaseService whose every call fails
// with err.
func failingLeaseService(err error) *mockLeaseService {
	return &mockLeaseService{
		grantFn:     func(context.Context, int64) (*lease.Lease, error) { return nil, err },
		revokeFn:    func(context.Context, int64) error { return err },
		keepAliveFn: func(context.Context, int64) (*lease.Lease, error) { return nil, err },
		ttlFn:       func(context.Context, int64) (*lease.Lease, error) { return nil, err },
		leasesFn:    func(context.Context) ([]*lease.Lease, error) { return nil, err },
	}
}

func (m *mockLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (m *mockLeaseService) Revoke(ctx context.Context, id int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, id)
	}
	return nil
}

func (m *mockLeaseService) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 60}, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.ttlFn != nil {
		return m.ttlFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 30}, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return []*lease.Lease{{ID: 1, TTL: 60}, {ID: 2, TTL: 120}}, nil
}

// errorKVService fails every KV read with errSentinel.
type errorKVService struct {
	mockKVService
}

func (*errorKVService) Get(context.Context, string) ([]byte, error) { return nil, errSentinel }

func (*errorKVService) List(context.Context, string) ([]string, error) { return nil, errSentinel }

func (*errorKVService) ListAll(context.Context, string) ([]string, error) { return nil, errSentinel }

func (*errorKVService) Scan(context.Context, string) (map[string][]byte, error) {
	return nil, errSentinel
}

func (*errorKVService) GetWithConsistency(context.Context, string, consistency.ReadOptions) ([]byte, error) {
	return nil, errSentinel
}

func (*errorKVService) ListWithConsistency(context.Context, string, consistency.ReadOptions) ([]string, error) {
	return nil, errSentinel
}

func (*errorKVService) ScanWithConsistency(context.Context, string, consistency.ReadOptions) (map[string][]byte, error) {
	return nil, errSentinel
}

func (*errorKVService) ExistsWithConsistency(context.Context, string, consistency.ReadOptions) (bool, error) {
	return false, errSentinel
}

// errorAdminService fails every admin call that reaches a shard or the
// node's state with errSentinel.
type errorAdminService struct {
	mockAdminService
}

func (*errorAdminService) Health(context.Context) (*service.HealthStatus, error) {
	return nil, errSentinel
}

func (*errorAdminService) Status(context.Context) (*service.StatusInfo, error) {
	return nil, errSentinel
}

func (*errorAdminService) Capabilities(context.Context) (*capabilities.Capabilities, error) {
	return nil, errSentinel
}

func (*errorAdminService) StartShard(context.Context, *service.StartShardRequest) error {
	return errSentinel
}

func (*errorAdminService) StopShard(context.Context, uint64) error { return errSentinel }

func (*errorAdminService) GetShardState(context.Context, uint64) (*service.ShardStateResponse, error) {
	return nil, errSentinel
}

func (*errorAdminService) GetTopology(context.Context) (*service.TopologyResponse, error) {
	return nil, errSentinel
}

func (*errorAdminService) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (*errorAdminService) SetNodeCompaction(context.Context, *compaction.Policy) error { return nil }

// errTSSvc fails the time-series series, retention and metric calls with
// errSentinel.
type errTSSvc struct {
	captureTSSvc
}

func (*errTSSvc) DeleteSeries(context.Context, *timeseries.DeleteRequest) error { return errSentinel }

func (*errTSSvc) GetRetentionPolicy(string) (*timeseries.RetentionPolicy, error) {
	return nil, errSentinel
}

func (*errTSSvc) SetRetentionPolicy(context.Context, string, *timeseries.RetentionPolicy) error {
	return errSentinel
}

func (*errTSSvc) ListMetrics(string, int) ([]string, error) { return nil, errSentinel }

func (*errTSSvc) ListMetricsWithConsistency(context.Context, string, int, consistency.ReadOptions) ([]string, error) {
	return nil, errSentinel
}

// errVectorService fails every vector call with errSentinel.
type errVectorService struct {
	mockVectorService
}

func (*errVectorService) CreateNamespace(context.Context, *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) UpdateNamespace(context.Context, *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) DeleteNamespace(context.Context, *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) ListNamespaces(context.Context, *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) GetNamespaceInfo(context.Context, *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) UpsertVector(context.Context, *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) GetVector(context.Context, *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) DeleteVector(context.Context, *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) BatchUpsert(context.Context, *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) BatchDeleteVectors(context.Context, *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) ListVectors(context.Context, *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) UpdateMetadata(context.Context, *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) SearchSimilar(context.Context, *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) SearchByMetadata(context.Context, *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) HybridSearch(context.Context, *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) SearchByID(context.Context, *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) RebuildIndex(context.Context, *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) GetRebuildProgress(context.Context, *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) GetIndexStats(context.Context, *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) CompactNamespace(context.Context, *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return nil, errSentinel
}

func (*errVectorService) GetCompactionProgress(context.Context, *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return nil, errSentinel
}
