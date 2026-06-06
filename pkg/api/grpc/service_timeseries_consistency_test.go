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
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// capturingTSService records which consistency-aware method was called and
// which ReadOptions it received, delegating the actual work to an inner
// mockTimeSeriesService.
// ---------------------------------------------------------------------------

type capturingTSService struct {
	mockTimeSeriesService

	// set to true when the *WithConsistency variant is invoked
	queryCalled       bool
	aggregateCalled   bool
	richQueryCalled   bool
	listMetricsCalled bool

	// last captured ReadOptions from each *WithConsistency call
	queryOpts       consistency.ReadOptions
	aggregateOpts   consistency.ReadOptions
	richQueryOpts   consistency.ReadOptions
	listMetricsOpts consistency.ReadOptions
}

func (m *capturingTSService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.queryCalled = true
	m.queryOpts = ropts
	return m.mockTimeSeriesService.Query(ctx, metric, start, end, opts...)
}

func (m *capturingTSService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	m.aggregateCalled = true
	m.aggregateOpts = ropts
	return m.mockTimeSeriesService.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *capturingTSService) QueryRich(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return &timeseries.QueryResult{Metric: req.Metric, Points: []*timeseries.DataPoint{}}, nil
}

func (m *capturingTSService) QueryRichWithConsistency(_ context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	m.richQueryCalled = true
	m.richQueryOpts = ropts
	return &timeseries.QueryResult{Metric: req.Metric, Points: []*timeseries.DataPoint{}}, nil
}

func (m *capturingTSService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	m.listMetricsCalled = true
	m.listMetricsOpts = ropts
	return m.mockTimeSeriesService.ListMetrics(prefix, limit)
}

// ---------------------------------------------------------------------------
// TSQuery consistency routing
// ---------------------------------------------------------------------------

// TestGRPC_TSQuery_ConsistencyRoutes_WithConsistency verifies that each
// non-UNSPECIFIED ConsistencyLevel causes the handler to call
// QueryWithConsistency with the correctly parsed ReadOptions.
func TestGRPC_TSQuery_ConsistencyRoutes_WithConsistency(t *testing.T) {
	tests := []struct {
		name          string
		level         pb.ConsistencyLevel
		wantConsLevel consistency.Level
		wantStale     bool
	}{
		{"strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, consistency.Strong, false},
		{"linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, consistency.Linearizable, false},
		{"eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, consistency.Eventual, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capturingTSService{}
			client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
			defer cleanup()

			now := time.Now()
			_, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
				Metric:      "cpu",
				StartNs:     now.Add(-time.Hour).UnixNano(),
				EndNs:       now.UnixNano(),
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("TSQuery: %v", err)
			}
			if !svc.queryCalled {
				t.Fatal("expected QueryWithConsistency to be called, but it was not")
			}
			if svc.queryOpts.Consistency != tc.wantConsLevel {
				t.Fatalf("Consistency = %v, want %v", svc.queryOpts.Consistency, tc.wantConsLevel)
			}
			if svc.queryOpts.AllowStale != tc.wantStale {
				t.Fatalf("AllowStale = %v, want %v", svc.queryOpts.AllowStale, tc.wantStale)
			}
		})
	}
}

// TestGRPC_TSQuery_ConsistencyRoutes_Unspecified verifies that UNSPECIFIED
// consistency causes the handler to call the plain Query method.
func TestGRPC_TSQuery_ConsistencyRoutes_Unspecified(t *testing.T) {
	svc := &capturingTSService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
	defer cleanup()

	now := time.Now()
	_, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if svc.queryCalled {
		t.Fatal("expected plain Query to be called, but QueryWithConsistency was invoked")
	}
}

// ---------------------------------------------------------------------------
// TSAggregate consistency routing
// ---------------------------------------------------------------------------

// TestGRPC_TSAggregate_ConsistencyRoutes_WithConsistency verifies that each
// non-UNSPECIFIED ConsistencyLevel routes to AggregateWithConsistency.
func TestGRPC_TSAggregate_ConsistencyRoutes_WithConsistency(t *testing.T) {
	tests := []struct {
		name          string
		level         pb.ConsistencyLevel
		wantConsLevel consistency.Level
	}{
		{"strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, consistency.Strong},
		{"eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, consistency.Eventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capturingTSService{}
			client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
			defer cleanup()

			now := time.Now()
			_, err := client.TSAggregate(context.Background(), &pb.TSAggregateRequest{
				Metric:      "cpu",
				StartNs:     now.Add(-time.Hour).UnixNano(),
				EndNs:       now.UnixNano(),
				Function:    pb.AggFunc_AGG_AVG,
				IntervalNs:  int64(time.Minute),
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("TSAggregate: %v", err)
			}
			if !svc.aggregateCalled {
				t.Fatal("expected AggregateWithConsistency to be called, but it was not")
			}
			if svc.aggregateOpts.Consistency != tc.wantConsLevel {
				t.Fatalf("Consistency = %v, want %v", svc.aggregateOpts.Consistency, tc.wantConsLevel)
			}
		})
	}
}

// TestGRPC_TSAggregate_ConsistencyRoutes_Unspecified verifies that UNSPECIFIED
// consistency causes the handler to call the plain Aggregate method.
func TestGRPC_TSAggregate_ConsistencyRoutes_Unspecified(t *testing.T) {
	svc := &capturingTSService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
	defer cleanup()

	now := time.Now()
	_, err := client.TSAggregate(context.Background(), &pb.TSAggregateRequest{
		Metric:   "cpu",
		StartNs:  now.Add(-time.Hour).UnixNano(),
		EndNs:    now.UnixNano(),
		Function: pb.AggFunc_AGG_SUM,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if svc.aggregateCalled {
		t.Fatal("expected plain Aggregate to be called, but AggregateWithConsistency was invoked")
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery consistency routing
// ---------------------------------------------------------------------------

// TestGRPC_TSRichQuery_ConsistencyRoutes_WithConsistency verifies that each
// non-UNSPECIFIED ConsistencyLevel routes to QueryRichWithConsistency.
func TestGRPC_TSRichQuery_ConsistencyRoutes_WithConsistency(t *testing.T) {
	tests := []struct {
		name          string
		level         pb.ConsistencyLevel
		wantConsLevel consistency.Level
	}{
		{"strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, consistency.Strong},
		{"linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, consistency.Linearizable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capturingTSService{}
			client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
			defer cleanup()

			now := time.Now()
			_, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
				Metric:      "cpu",
				StartNs:     now.Add(-time.Hour).UnixNano(),
				EndNs:       now.UnixNano(),
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("TSRichQuery: %v", err)
			}
			if !svc.richQueryCalled {
				t.Fatal("expected QueryRichWithConsistency to be called, but it was not")
			}
			if svc.richQueryOpts.Consistency != tc.wantConsLevel {
				t.Fatalf("Consistency = %v, want %v", svc.richQueryOpts.Consistency, tc.wantConsLevel)
			}
		})
	}
}

// TestGRPC_TSRichQuery_ConsistencyRoutes_Unspecified verifies that UNSPECIFIED
// consistency causes the handler to call the plain QueryRich method.
func TestGRPC_TSRichQuery_ConsistencyRoutes_Unspecified(t *testing.T) {
	svc := &capturingTSService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
	defer cleanup()

	now := time.Now()
	_, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if svc.richQueryCalled {
		t.Fatal("expected plain QueryRich to be called, but QueryRichWithConsistency was invoked")
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics consistency routing
// ---------------------------------------------------------------------------

// TestGRPC_TSListMetrics_ConsistencyRoutes_WithConsistency verifies that each
// non-UNSPECIFIED ConsistencyLevel routes to ListMetricsWithConsistency.
func TestGRPC_TSListMetrics_ConsistencyRoutes_WithConsistency(t *testing.T) {
	tests := []struct {
		name          string
		level         pb.ConsistencyLevel
		wantConsLevel consistency.Level
		wantStale     bool
	}{
		{"strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, consistency.Strong, false},
		{"eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, consistency.Eventual, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capturingTSService{}
			client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
			defer cleanup()

			_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{
				Prefix:      "cpu",
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("TSListMetrics: %v", err)
			}
			if !svc.listMetricsCalled {
				t.Fatal("expected ListMetricsWithConsistency to be called, but it was not")
			}
			if svc.listMetricsOpts.Consistency != tc.wantConsLevel {
				t.Fatalf("Consistency = %v, want %v", svc.listMetricsOpts.Consistency, tc.wantConsLevel)
			}
			if svc.listMetricsOpts.AllowStale != tc.wantStale {
				t.Fatalf("AllowStale = %v, want %v", svc.listMetricsOpts.AllowStale, tc.wantStale)
			}
		})
	}
}

// TestGRPC_TSListMetrics_ConsistencyRoutes_Unspecified verifies that
// UNSPECIFIED consistency causes the handler to call the plain ListMetrics.
func TestGRPC_TSListMetrics_ConsistencyRoutes_Unspecified(t *testing.T) {
	svc := &capturingTSService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(svc))
	defer cleanup()

	_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{
		Prefix: "cpu",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if svc.listMetricsCalled {
		t.Fatal("expected plain ListMetrics to be called, but ListMetricsWithConsistency was invoked")
	}
}
