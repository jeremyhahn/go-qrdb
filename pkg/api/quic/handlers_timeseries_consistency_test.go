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
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// handleTSQuery: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSQuery_WithConsistency_RoutesToWithConsistency verifies that a
// TSQueryRequest carrying a non-UNSPECIFIED ConsistencyLevel routes to
// QueryWithConsistency and passes the resolved ReadOptions.
func TestHandleTSQuery_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	tsSvc := &mockTimeSeriesService{
		queryWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, ropts consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []timeseries.DataPoint{}, nil
		},
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			t.Error("plain Query must not be called when consistency is specified")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSQuery(context.Background(), payload); err != nil {
		t.Fatalf("handleTSQuery: %v", err)
	}

	if !withConsistencyCalled {
		t.Fatal("QueryWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", capturedOpts.Consistency)
	}
	if !capturedOpts.AllowStale {
		t.Error("AllowStale should be true for Eventual consistency")
	}
}

// TestHandleTSQuery_UnspecifiedConsistency_RoutesToPlain verifies that a
// TSQueryRequest with UNSPECIFIED consistency routes to the plain Query method.
func TestHandleTSQuery_UnspecifiedConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			plainCalled = true
			return []timeseries.DataPoint{}, nil
		},
		queryWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			t.Error("QueryWithConsistency must not be called when consistency is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSQuery(context.Background(), payload); err != nil {
		t.Fatalf("handleTSQuery: %v", err)
	}

	if !plainCalled {
		t.Fatal("plain Query was not called")
	}
}

// ---------------------------------------------------------------------------
// handleTSAggregate: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSAggregate_WithConsistency_RoutesToWithConsistency verifies that
// a TSAggregateRequest with a non-UNSPECIFIED ConsistencyLevel routes to
// AggregateWithConsistency with the resolved ReadOptions.
func TestHandleTSAggregate_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	tsSvc := &mockTimeSeriesService{
		aggregateWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []timeseries.AggResult{}, nil
		},
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			t.Error("plain Aggregate must not be called when consistency is specified")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Function:    pb.AggFunc_AGG_AVG,
		IntervalNs:  int64(time.Hour),
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSAggregate(context.Background(), payload); err != nil {
		t.Fatalf("handleTSAggregate: %v", err)
	}

	if !withConsistencyCalled {
		t.Fatal("AggregateWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", capturedOpts.Consistency)
	}
}

// TestHandleTSAggregate_UnspecifiedConsistency_RoutesToPlain verifies that a
// TSAggregateRequest with UNSPECIFIED consistency routes to the plain Aggregate.
func TestHandleTSAggregate_UnspecifiedConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			plainCalled = true
			return []timeseries.AggResult{}, nil
		},
		aggregateWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
			t.Error("AggregateWithConsistency must not be called when consistency is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    1,
		EndNs:      2,
		Function:   pb.AggFunc_AGG_SUM,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSAggregate(context.Background(), payload); err != nil {
		t.Fatalf("handleTSAggregate: %v", err)
	}

	if !plainCalled {
		t.Fatal("plain Aggregate was not called")
	}
}

// ---------------------------------------------------------------------------
// handleTSRichQuery: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSRichQuery_WithConsistency_RoutesToWithConsistency verifies that
// a JSON rich-query request with a non-empty consistency string routes to
// QueryRichWithConsistency with the resolved ReadOptions.
func TestHandleTSRichQuery_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	tsSvc := &mockTimeSeriesService{
		queryRichWithConsistencyFn: func(_ context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			t.Error("plain QueryRich must not be called when consistency is specified")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := json.Marshal(tsRichQueryWireRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: "linearizable",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSRichQuery(context.Background(), payload); err != nil {
		t.Fatalf("handleTSRichQuery: %v", err)
	}

	if !withConsistencyCalled {
		t.Fatal("QueryRichWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", capturedOpts.Consistency)
	}
}

// TestHandleTSRichQuery_EmptyConsistency_RoutesToPlain verifies that a rich
// query request with an empty consistency string routes to the plain QueryRich.
func TestHandleTSRichQuery_EmptyConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	tsSvc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			plainCalled = true
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
		queryRichWithConsistencyFn: func(_ context.Context, _ *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
			t.Error("QueryRichWithConsistency must not be called when consistency is empty")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := json.Marshal(tsRichQueryWireRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSRichQuery(context.Background(), payload); err != nil {
		t.Fatalf("handleTSRichQuery: %v", err)
	}

	if !plainCalled {
		t.Fatal("plain QueryRich was not called")
	}
}

// ---------------------------------------------------------------------------
// handleTSListMetrics: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSListMetrics_WithConsistency_RoutesToWithConsistency verifies
// that a JSON list-metrics request with a non-empty consistency string routes
// to ListMetricsWithConsistency with the resolved ReadOptions.
func TestHandleTSListMetrics_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	tsSvc := &mockTimeSeriesService{
		listMetricsWithConsistencyFn: func(_ context.Context, _ string, _ int, ropts consistency.ReadOptions) ([]string, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []string{"cpu"}, nil
		},
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			t.Error("plain ListMetrics must not be called when consistency is specified")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := json.Marshal(tsListMetricsWireRequest{
		Prefix:      "cpu.",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSListMetrics(context.Background(), payload); err != nil {
		t.Fatalf("handleTSListMetrics: %v", err)
	}

	if !withConsistencyCalled {
		t.Fatal("ListMetricsWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", capturedOpts.Consistency)
	}
	if !capturedOpts.AllowStale {
		t.Error("AllowStale should be true for Eventual consistency")
	}
}

// TestHandleTSListMetrics_EmptyConsistency_RoutesToPlain verifies that a
// list-metrics request with no consistency string routes to plain ListMetrics.
func TestHandleTSListMetrics_EmptyConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	tsSvc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			plainCalled = true
			return []string{"cpu"}, nil
		},
		listMetricsWithConsistencyFn: func(_ context.Context, _ string, _ int, _ consistency.ReadOptions) ([]string, error) {
			t.Error("ListMetricsWithConsistency must not be called when consistency is empty")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := json.Marshal(tsListMetricsWireRequest{
		Prefix: "cpu.",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSListMetrics(context.Background(), payload); err != nil {
		t.Fatalf("handleTSListMetrics: %v", err)
	}

	if !plainCalled {
		t.Fatal("plain ListMetrics was not called")
	}
}
