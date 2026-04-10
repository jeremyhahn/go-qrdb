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
	"errors"
	"testing"
	"time"

	grpcserver "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock time-series service
// ---------------------------------------------------------------------------

type mockTSService struct {
	writeErr        error
	writeBatchErr   error
	writePointErr   error
	queryResult     []timeseries.DataPoint
	queryErr        error
	aggregateResult []timeseries.AggResult
	aggregateErr    error
	richQueryResult *timeseries.QueryResult
	richQueryErr    error
	deleteErr       error
	getRetentionRes *timeseries.RetentionPolicy
	getRetentionErr error
	setRetentionErr error
	listMetricsRes  []string
	listMetricsErr  error

	// capture fields to verify requests are forwarded correctly
	lastWriteMetric string
	lastWriteValue  float64
	lastWriteTags   map[string]string
}

func newMockTSService() *mockTSService {
	return &mockTSService{}
}

func (m *mockTSService) Write(_ context.Context, metric string, value float64, _ time.Time, tags map[string]string) error {
	m.lastWriteMetric = metric
	m.lastWriteValue = value
	m.lastWriteTags = tags
	return m.writeErr
}

func (m *mockTSService) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	return m.writeBatchErr
}

func (m *mockTSService) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return m.queryResult, nil
}

func (m *mockTSService) Aggregate(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateErr != nil {
		return nil, m.aggregateErr
	}
	return m.aggregateResult, nil
}

func (m *mockTSService) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return m.writePointErr
}

func (m *mockTSService) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.richQueryErr != nil {
		return nil, m.richQueryErr
	}
	return m.richQueryResult, nil
}

func (m *mockTSService) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return m.deleteErr
}

func (m *mockTSService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	if m.getRetentionErr != nil {
		return nil, m.getRetentionErr
	}
	return m.getRetentionRes, nil
}

func (m *mockTSService) SetRetentionPolicy(_ string, _ *timeseries.RetentionPolicy) error {
	return m.setRetentionErr
}

func (m *mockTSService) ListMetrics(_ string, _ int) ([]string, error) {
	if m.listMetricsErr != nil {
		return nil, m.listMetricsErr
	}
	return m.listMetricsRes, nil
}

// Compile-time assertion.
var _ timeseries.TimeSeriesServicer = (*mockTSService)(nil)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// connectTSTransport creates a new gRPC server with TS support, connects
// an SDK transport, and returns both for testing.
func connectTSTransport(t *testing.T, tsSvc timeseries.TimeSeriesServicer) (*Transport, func()) {
	t.Helper()

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	srv := grpcserver.NewServer(kvSvc, adminSvc, "127.0.0.1:0", grpcserver.WithTimeSeries(tsSvc))
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start gRPC server: %v", err)
	}

	tr, err := New(transport.WithAddress(srv.Addr()))
	if err != nil {
		srv.Stop()
		t.Fatalf("New: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		srv.Stop()
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
		srv.Stop()
	}

	return tr, cleanup
}

// ---------------------------------------------------------------------------
// TSWrite tests
// ---------------------------------------------------------------------------

func TestTSWrite_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       42.5,
		TimestampNs: time.Now().UnixNano(),
		Tags:        []transport.TSTag{{Key: "host", Value: "node-1"}},
	})
	if err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
}

func TestTSWrite_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.writeErr = errors.New("disk full")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       42.5,
		TimestampNs: time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSWrite")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSWrite_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric: "cpu.usage",
		Value:  1.0,
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSWriteBatch tests
// ---------------------------------------------------------------------------

func TestTSWriteBatch_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	points := []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 42.5, TimestampNs: time.Now().UnixNano()},
		{Metric: "mem.usage", Value: 78.2, TimestampNs: time.Now().UnixNano()},
	}

	err := tr.TSWriteBatch(context.Background(), points)
	if err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestTSWriteBatch_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.writeBatchErr = errors.New("batch too large")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	points := []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 42.5, TimestampNs: time.Now().UnixNano()},
	}

	err := tr.TSWriteBatch(context.Background(), points)
	if err == nil {
		t.Fatal("expected error from TSWriteBatch")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSWriteBatch_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 1.0},
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSQuery tests
// ---------------------------------------------------------------------------

func TestTSQuery_Success(t *testing.T) {
	now := time.Now()
	tsSvc := newMockTSService()
	tsSvc.queryResult = []timeseries.DataPoint{
		{Metric: "cpu.usage", Value: 42.5, Timestamp: now, Tags: map[string]string{"host": "node-1"}},
		{Metric: "cpu.usage", Value: 38.1, Timestamp: now.Add(-time.Minute), Tags: map[string]string{"host": "node-1"}},
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
		Tags:    []transport.TSTag{{Key: "host", Value: "node-1"}},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(resp.Points))
	}
	if resp.Points[0].Metric != "cpu.usage" {
		t.Fatalf("expected metric cpu.usage, got %s", resp.Points[0].Metric)
	}
}

func TestTSQuery_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.queryErr = errors.New("query timeout")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSQuery")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSQuery_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestTSQuery_EmptyResult(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.queryResult = nil
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "nonexistent.metric",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 0 {
		t.Fatalf("expected 0 points, got %d", len(resp.Points))
	}
}

// ---------------------------------------------------------------------------
// TSAggregate tests
// ---------------------------------------------------------------------------

func TestTSAggregate_Success(t *testing.T) {
	now := time.Now()
	tsSvc := newMockTSService()
	tsSvc.aggregateResult = []timeseries.AggResult{
		{Timestamp: now.Add(-2 * time.Hour), Value: 35.0, Count: 120},
		{Timestamp: now.Add(-time.Hour), Value: 42.5, Count: 130},
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu.usage",
		StartNs:    now.Add(-2 * time.Hour).UnixNano(),
		EndNs:      now.UnixNano(),
		Function:   transport.AggAvg,
		IntervalNs: int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[1].Value != 42.5 {
		t.Fatalf("expected value 42.5, got %f", resp.Results[1].Value)
	}
	if resp.Results[1].Count != 130 {
		t.Fatalf("expected count 130, got %d", resp.Results[1].Count)
	}
}

func TestTSAggregate_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.aggregateErr = errors.New("aggregation failed")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:   "cpu.usage",
		StartNs:  time.Now().Add(-time.Hour).UnixNano(),
		EndNs:    time.Now().UnixNano(),
		Function: transport.AggSum,
	})
	if err == nil {
		t.Fatal("expected error from TSAggregate")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSAggregate_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSWritePoint tests
// ---------------------------------------------------------------------------

func TestTSWritePoint_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric:      "disk.io",
		Value:       1024.0,
		TimestampNs: time.Now().UnixNano(),
		Tags:        []transport.TSTag{{Key: "device", Value: "sda1"}},
	})
	if err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestTSWritePoint_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.writePointErr = errors.New("validation failed")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric:      "disk.io",
		Value:       1024.0,
		TimestampNs: time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSWritePoint")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSWritePoint_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "disk.io",
		Value:  1.0,
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery tests
// ---------------------------------------------------------------------------

func TestTSRichQuery_Success(t *testing.T) {
	now := time.Now()
	aggVal := 42.5
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{
		Metric: "cpu.usage",
		Tags:   map[string]string{"host": "node-1"},
		Points: []*timeseries.DataPoint{
			{Metric: "cpu.usage", Value: 42.5, Timestamp: now, Tags: map[string]string{"host": "node-1"}},
		},
		AggregatedValue: &aggVal,
		GroupedResults: []*timeseries.GroupedResult{
			{
				Tags: map[string]string{"region": "us-east"},
				Points: []*timeseries.DataPoint{
					{Metric: "cpu.usage", Value: 40.0, Timestamp: now},
				},
				AggregatedValue: &aggVal,
			},
		},
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu.usage",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Aggregation: transport.AggAvg,
		IntervalNs:  int64(time.Minute),
		GroupBy:     []string{"region"},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "cpu.usage" {
		t.Fatalf("expected metric cpu.usage, got %s", resp.Metric)
	}
	if resp.AggregatedValue == nil {
		t.Fatal("expected non-nil AggregatedValue")
	}
	if *resp.AggregatedValue != 42.5 {
		t.Fatalf("expected AggregatedValue 42.5, got %f", *resp.AggregatedValue)
	}
	if len(resp.GroupedResults) != 1 {
		t.Fatalf("expected 1 grouped result, got %d", len(resp.GroupedResults))
	}
	if resp.GroupedResults[0].AggregatedValue == nil {
		t.Fatal("expected non-nil grouped AggregatedValue")
	}
}

func TestTSRichQuery_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryErr = errors.New("rich query failed")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu.usage",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSRichQuery")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSRichQuery_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestTSRichQuery_NilAggregatedValue(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{
		Metric:          "cpu.usage",
		AggregatedValue: nil,
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu.usage",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.AggregatedValue != nil {
		t.Fatalf("expected nil AggregatedValue, got %v", *resp.AggregatedValue)
	}
}

// ---------------------------------------------------------------------------
// TSDeleteSeries tests
// ---------------------------------------------------------------------------

func TestTSDeleteSeries_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric:  "cpu.usage",
		Tags:    []transport.TSTag{{Key: "host", Value: "node-1"}},
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestTSDeleteSeries_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.deleteErr = errors.New("delete failed")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error from TSDeleteSeries")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSDeleteSeries_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSGetRetention tests
// ---------------------------------------------------------------------------

func TestTSGetRetention_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.getRetentionRes = &timeseries.RetentionPolicy{
		Duration:    30 * 24 * time.Hour,
		Resolution:  time.Minute,
		Aggregation: timeseries.AggAvg,
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp.Policy == nil {
		t.Fatal("expected non-nil policy")
	}
	expectedDuration := int64(30 * 24 * time.Hour)
	if resp.Policy.DurationNs != expectedDuration {
		t.Fatalf("expected duration %d, got %d", expectedDuration, resp.Policy.DurationNs)
	}
	expectedResolution := int64(time.Minute)
	if resp.Policy.ResolutionNs != expectedResolution {
		t.Fatalf("expected resolution %d, got %d", expectedResolution, resp.Policy.ResolutionNs)
	}
	if resp.Policy.Aggregation != transport.AggAvg {
		t.Fatalf("expected AggAvg, got %d", resp.Policy.Aggregation)
	}
}

func TestTSGetRetention_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.getRetentionErr = errors.New("metric not found")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "nonexistent.metric",
	})
	if err == nil {
		t.Fatal("expected error from TSGetRetention")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSGetRetention_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSSetRetention tests
// ---------------------------------------------------------------------------

func TestTSSetRetention_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   int64(30 * 24 * time.Hour),
			ResolutionNs: int64(time.Minute),
			Aggregation:  transport.AggAvg,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestTSSetRetention_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.setRetentionErr = errors.New("invalid policy")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs: int64(time.Hour),
		},
	})
	if err == nil {
		t.Fatal("expected error from TSSetRetention")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSSetRetention_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestTSSetRetention_NilPolicy(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: nil,
	})
	if err != nil {
		t.Fatalf("TSSetRetention with nil policy: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics tests
// ---------------------------------------------------------------------------

func TestTSListMetrics_Success(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsRes = []string{"cpu.usage", "cpu.idle", "cpu.system"}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu.",
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 3 {
		t.Fatalf("expected 3 metrics, got %d", len(resp.Metrics))
	}
	if resp.Metrics[0] != "cpu.usage" {
		t.Fatalf("expected first metric cpu.usage, got %s", resp.Metrics[0])
	}
}

func TestTSListMetrics_ServiceError(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsErr = errors.New("listing failed")
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu.",
	})
	if err == nil {
		t.Fatal("expected error from TSListMetrics")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTSListMetrics_ClosedTransport(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()
	_ = tr.Close()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu.",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestTSListMetrics_EmptyResult(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsRes = nil
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "nonexistent.",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 0 {
		t.Fatalf("expected 0 metrics, got %d", len(resp.Metrics))
	}
}

// ---------------------------------------------------------------------------
// Conversion helper tests
// ---------------------------------------------------------------------------

func TestSdkAggFuncToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		sdkFunc  transport.AggFunc
		expected string
	}{
		{"None", transport.AggNone, "AGG_AVG"},
		{"Avg", transport.AggAvg, "AGG_AVG"},
		{"Sum", transport.AggSum, "AGG_SUM"},
		{"Min", transport.AggMin, "AGG_MIN"},
		{"Max", transport.AggMax, "AGG_MAX"},
		{"Count", transport.AggCount, "AGG_COUNT"},
		{"Percentile", transport.AggPercentile, "AGG_PERCENTILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sdkAggFuncToProto(tt.sdkFunc)
			if got.String() != tt.expected {
				t.Fatalf("sdkAggFuncToProto(%d) = %s, want %s", tt.sdkFunc, got.String(), tt.expected)
			}
		})
	}
}

func TestProtoAggFuncToSDK_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		expected transport.AggFunc
	}{
		{"AGG_AVG", transport.AggAvg},
		{"AGG_SUM", transport.AggSum},
		{"AGG_MIN", transport.AggMin},
		{"AGG_MAX", transport.AggMax},
		{"AGG_COUNT", transport.AggCount},
		{"AGG_PERCENTILE", transport.AggPercentile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoAggFuncToSDK(sdkAggFuncToProto(tt.expected))
			if got != tt.expected {
				t.Fatalf("round-trip failed: got %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestSdkAggFuncToProto_UnknownValue(t *testing.T) {
	// Unknown SDK values should default to AGG_AVG.
	got := sdkAggFuncToProto(transport.AggFunc(255))
	if got.String() != "AGG_AVG" {
		t.Fatalf("expected AGG_AVG for unknown value, got %s", got.String())
	}
}

func TestProtoAggFuncToSDK_UnknownValue(t *testing.T) {
	// Unknown proto values should default to AggNone.
	got := protoAggFuncToSDK(99)
	if got != transport.AggNone {
		t.Fatalf("expected AggNone for unknown value, got %d", got)
	}
}

func TestSdkPointToProto_NilInput(t *testing.T) {
	result := sdkPointToProto(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestSdkTagsToProto_EmptyInput(t *testing.T) {
	result := sdkTagsToProto(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
	result = sdkTagsToProto([]transport.TSTag{})
	if result != nil {
		t.Fatal("expected nil for empty input")
	}
}

func TestProtoTagsToSDK_EmptyInput(t *testing.T) {
	result := protoTagsToSDK(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestProtoPointsToSDK_EmptyInput(t *testing.T) {
	result := protoPointsToSDK(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestSdkPointToProto_WithTags(t *testing.T) {
	point := &transport.TSDataPoint{
		Metric:      "test.metric",
		Value:       99.9,
		TimestampNs: 1000000000,
		Tags: []transport.TSTag{
			{Key: "env", Value: "prod"},
			{Key: "region", Value: "us-east"},
		},
	}
	result := sdkPointToProto(point)
	if result.Metric != "test.metric" {
		t.Fatalf("expected metric test.metric, got %s", result.Metric)
	}
	if result.Value != 99.9 {
		t.Fatalf("expected value 99.9, got %f", result.Value)
	}
	if result.TimestampNs != 1000000000 {
		t.Fatalf("expected timestamp 1000000000, got %d", result.TimestampNs)
	}
	if len(result.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(result.Tags))
	}
}

func TestProtoRichQueryResponseToSDK_NoAggregatedValue(t *testing.T) {
	resp := protoRichQueryResponseToSDK(&pb.TSRichQueryResponse{
		Metric:             "m",
		HasAggregatedValue: false,
		AggregatedValue:    0,
	})
	if resp.AggregatedValue != nil {
		t.Fatal("expected nil AggregatedValue when HasAggregatedValue is false")
	}
}

func TestProtoRichQueryResponseToSDK_WithGroupedResults(t *testing.T) {
	aggVal := float64(55.5)
	resp := protoRichQueryResponseToSDK(&pb.TSRichQueryResponse{
		Metric:             "cpu.usage",
		HasAggregatedValue: true,
		AggregatedValue:    aggVal,
		GroupedResults: []*pb.TSGroupedResult{
			{
				Tags: []*pb.TSTag{
					{Key: "region", Value: "us-east"},
				},
				HasAggregatedValue: true,
				AggregatedValue:    33.3,
			},
			{
				Tags: []*pb.TSTag{
					{Key: "region", Value: "eu-west"},
				},
				HasAggregatedValue: false,
			},
		},
	})
	if resp.Metric != "cpu.usage" {
		t.Fatalf("expected metric cpu.usage, got %s", resp.Metric)
	}
	if resp.AggregatedValue == nil || *resp.AggregatedValue != aggVal {
		t.Fatalf("expected AggregatedValue %f, got %v", aggVal, resp.AggregatedValue)
	}
	if len(resp.GroupedResults) != 2 {
		t.Fatalf("expected 2 grouped results, got %d", len(resp.GroupedResults))
	}
	if resp.GroupedResults[0].AggregatedValue == nil {
		t.Fatal("expected non-nil AggregatedValue for first group")
	}
	if resp.GroupedResults[1].AggregatedValue != nil {
		t.Fatal("expected nil AggregatedValue for second group")
	}
}
