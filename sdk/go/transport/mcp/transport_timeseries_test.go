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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockTSSvc is an in-memory service.TimeSeriesServicer for transport tests.
// ---------------------------------------------------------------------------

type mockTSSvc struct {
	mu sync.Mutex

	writeErr      error
	writeBatchErr error
	queryErr      error
	aggregateErr  error
	writePointErr error
	queryRichErr  error
	deleteErr     error
	getRetentErr  error
	setRetentErr  error
	listMetricErr error

	lastMetric    string
	lastValue     float64
	lastTimestamp time.Time
	lastTags      map[string]string
	lastPoints    []timeseries.DataPoint
	lastStart     time.Time
	lastEnd       time.Time
	lastAggFunc   timeseries.AggFunc
	lastInterval  time.Duration

	// consistency capture fields
	lastQueryConsistency     *consistency.ReadOptions
	lastAggConsistency       *consistency.ReadOptions
	lastRichQueryConsistency *consistency.ReadOptions
	lastListConsistency      *consistency.ReadOptions

	queryResp     []timeseries.DataPoint
	aggregateResp []timeseries.AggResult
	queryRichResp *timeseries.QueryResult
	retentionResp *timeseries.RetentionPolicy
	metricsResp   []string
}

func newMockTSSvc() *mockTSSvc {
	now := time.Now().UTC()
	return &mockTSSvc{
		queryResp: []timeseries.DataPoint{
			{Metric: "cpu", Value: 0.5, Timestamp: now, Tags: map[string]string{"host": "a"}},
			{Metric: "cpu", Value: 0.6, Timestamp: now.Add(time.Minute), Tags: map[string]string{"host": "a"}},
		},
		aggregateResp: []timeseries.AggResult{
			{Timestamp: now, Value: 0.55, Count: 2},
		},
		queryRichResp: &timeseries.QueryResult{
			Metric: "cpu",
			Points: []*timeseries.DataPoint{
				{Metric: "cpu", Value: 0.5, Timestamp: now},
			},
		},
		retentionResp: &timeseries.RetentionPolicy{
			Duration: 7 * 24 * time.Hour,
		},
		metricsResp: []string{"cpu", "memory", "disk"},
	}
}

func (m *mockTSSvc) Write(_ context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMetric = metric
	m.lastValue = value
	m.lastTimestamp = timestamp
	m.lastTags = tags
	return m.writeErr
}

func (m *mockTSSvc) WriteBatch(_ context.Context, points []timeseries.DataPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPoints = points
	return m.writeBatchErr
}

func (m *mockTSSvc) Query(_ context.Context, metric string, start, end time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMetric = metric
	m.lastStart = start
	m.lastEnd = end
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return m.queryResp, nil
}

func (m *mockTSSvc) QueryWithConsistency(_ context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	m.lastQueryConsistency = &ropts
	m.mu.Unlock()
	return m.Query(nil, metric, start, end, opts...)
}

func (m *mockTSSvc) Aggregate(_ context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMetric = metric
	m.lastStart = start
	m.lastEnd = end
	m.lastAggFunc = fn
	m.lastInterval = interval
	if m.aggregateErr != nil {
		return nil, m.aggregateErr
	}
	return m.aggregateResp, nil
}

func (m *mockTSSvc) AggregateWithConsistency(_ context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	m.lastAggConsistency = &ropts
	m.mu.Unlock()
	return m.Aggregate(nil, metric, start, end, fn, interval)
}

func (m *mockTSSvc) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return m.writePointErr
}

func (m *mockTSSvc) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichErr != nil {
		return nil, m.queryRichErr
	}
	return m.queryRichResp, nil
}

func (m *mockTSSvc) QueryRichWithConsistency(_ context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	m.mu.Lock()
	m.lastRichQueryConsistency = &ropts
	m.mu.Unlock()
	return m.QueryRich(nil, req)
}

func (m *mockTSSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return m.deleteErr
}

func (m *mockTSSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	if m.getRetentErr != nil {
		return nil, m.getRetentErr
	}
	return m.retentionResp, nil
}

func (m *mockTSSvc) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return m.setRetentErr
}

func (m *mockTSSvc) ListMetrics(_ string, _ int) ([]string, error) {
	if m.listMetricErr != nil {
		return nil, m.listMetricErr
	}
	return m.metricsResp, nil
}

func (m *mockTSSvc) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	m.mu.Lock()
	m.lastListConsistency = &ropts
	m.mu.Unlock()
	return m.ListMetrics(prefix, limit)
}

var _ service.TimeSeriesServicer = (*mockTSSvc)(nil)

func connectTSInProcess(t *testing.T, svc service.TimeSeriesServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithTimeSeries(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// TSWrite
// ---------------------------------------------------------------------------

func TestTSWrite_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu",
		Value:       0.75,
		TimestampNs: time.Now().UnixNano(),
		Tags:        []transport.TSTag{{Key: "host", Value: "a"}},
	})
	if err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastMetric != "cpu" {
		t.Errorf("metric: %q", svc.lastMetric)
	}
}

func TestTSWrite_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.writeErr = errors.New("write failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "x", TimestampNs: time.Now().UnixNano()})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSWriteBatch
// ---------------------------------------------------------------------------

func TestTSWriteBatch_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "cpu", Value: 0.5, TimestampNs: time.Now().UnixNano()},
		{Metric: "cpu", Value: 0.6, TimestampNs: time.Now().UnixNano()},
	})
	if err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestTSWriteBatch_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.writeBatchErr = errors.New("batch failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "cpu", TimestampNs: time.Now().UnixNano()},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSQuery
// ---------------------------------------------------------------------------

func TestTSQuery_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 2 {
		t.Errorf("points: %d", len(resp.Points))
	}
}

func TestTSQuery_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.queryErr = errors.New("query failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric: "cpu", StartNs: now.UnixNano(), EndNs: now.UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSAggregate
// ---------------------------------------------------------------------------

func TestTSAggregate_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    now.Add(-time.Hour).UnixNano(),
		EndNs:      now.UnixNano(),
		Function:   transport.AggAvg,
		IntervalNs: 60_000_000_000,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results: %d", len(resp.Results))
	}
}

func TestTSAggregate_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.aggregateErr = errors.New("agg failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric: "cpu", StartNs: now.UnixNano(), EndNs: now.UnixNano(), Function: transport.AggAvg, IntervalNs: 1_000_000_000,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Tag conversion helpers
// ---------------------------------------------------------------------------

func TestTagsSliceToMap(t *testing.T) {
	in := []transport.TSTag{{Key: "host", Value: "a"}, {Key: "region", Value: "us"}}
	out := tagsSliceToMap(in)
	if len(out) != 2 || out["host"] != "a" || out["region"] != "us" {
		t.Errorf("converted: %+v", out)
	}

	if tagsSliceToMap(nil) != nil {
		t.Error("expected nil for nil input")
	}
}

func TestTagsMapToSlice(t *testing.T) {
	in := map[string]string{"host": "a", "region": "us"}
	out := tagsMapToSlice(in)
	if len(out) != 2 {
		t.Errorf("count: %d", len(out))
	}

	if tagsMapToSlice(nil) != nil {
		t.Error("expected nil for nil input")
	}
}

func TestTagsMapToJSON(t *testing.T) {
	in := map[string]string{"host": "a"}
	out, err := tagsMapToJSON(in)
	if err != nil {
		t.Fatalf("tagsMapToJSON: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty json")
	}

	empty, err := tagsMapToJSON(nil)
	if err != nil {
		t.Fatalf("tagsMapToJSON(nil): %v", err)
	}
	if empty != "" {
		t.Errorf("expected empty string, got %q", empty)
	}
}

func TestServerDataPointToTransport(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	in := &serverDataPoint{
		Metric:    "cpu",
		Value:     0.75,
		Timestamp: ts,
		Tags:      map[string]string{"host": "a"},
	}
	out := serverDataPointToTransport(in)
	if out.Metric != "cpu" || out.Value != 0.75 {
		t.Errorf("conversion: %+v", out)
	}
	if out.TimestampNs != ts.UnixNano() {
		t.Errorf("timestamp: %d", out.TimestampNs)
	}
	if len(out.Tags) != 1 {
		t.Errorf("tags: %d", len(out.Tags))
	}
}
