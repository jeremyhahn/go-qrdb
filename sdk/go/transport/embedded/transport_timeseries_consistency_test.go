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
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// captureTSSvc records which consistency variant was called.
// ---------------------------------------------------------------------------

type captureTSSvc struct {
	mu sync.Mutex

	calledQuery        bool
	calledQueryConsist bool
	lastQueryROpts     consistency.ReadOptions

	calledAggregate  bool
	calledAggConsist bool
	lastAggROpts     consistency.ReadOptions

	calledRichQuery   bool
	calledRichConsist bool
	lastRichROpts     consistency.ReadOptions

	calledListMetrics bool
	calledListConsist bool
	lastListROpts     consistency.ReadOptions
}

func newCaptureTSSvc() *captureTSSvc { return &captureTSSvc{} }

func (m *captureTSSvc) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return nil
}

func (m *captureTSSvc) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	return nil
}

func (m *captureTSSvc) Query(_ context.Context, metric string, start, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	m.calledQuery = true
	m.mu.Unlock()
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *captureTSSvc) QueryWithConsistency(_ context.Context, metric string, start, _ time.Time, ropts consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	m.calledQueryConsist = true
	m.lastQueryROpts = ropts
	m.mu.Unlock()
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *captureTSSvc) Aggregate(_ context.Context, _ string, start, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	m.calledAggregate = true
	m.mu.Unlock()
	return []timeseries.AggResult{{Timestamp: start, Value: 1.0, Count: 1}}, nil
}

func (m *captureTSSvc) AggregateWithConsistency(_ context.Context, _ string, start, _ time.Time, _ timeseries.AggFunc, _ time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	m.calledAggConsist = true
	m.lastAggROpts = ropts
	m.mu.Unlock()
	return []timeseries.AggResult{{Timestamp: start, Value: 1.0, Count: 1}}, nil
}

func (m *captureTSSvc) WritePoint(_ context.Context, _ *timeseries.DataPoint) error { return nil }

func (m *captureTSSvc) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	m.mu.Lock()
	m.calledRichQuery = true
	m.mu.Unlock()
	return &timeseries.QueryResult{}, nil
}

func (m *captureTSSvc) QueryRichWithConsistency(_ context.Context, _ *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	m.mu.Lock()
	m.calledRichConsist = true
	m.lastRichROpts = ropts
	m.mu.Unlock()
	return &timeseries.QueryResult{}, nil
}

func (m *captureTSSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}

func (m *captureTSSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return &timeseries.RetentionPolicy{}, nil
}

func (m *captureTSSvc) ApplyRetention(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *captureTSSvc) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return nil
}

func (m *captureTSSvc) ListMetrics(_ string, _ int) ([]string, error) {
	m.mu.Lock()
	m.calledListMetrics = true
	m.mu.Unlock()
	return []string{"cpu"}, nil
}

func (m *captureTSSvc) ListMetricsWithConsistency(_ context.Context, _ string, _ int, ropts consistency.ReadOptions) ([]string, error) {
	m.mu.Lock()
	m.calledListConsist = true
	m.lastListROpts = ropts
	m.mu.Unlock()
	return []string{"cpu"}, nil
}

var _ timeseries.TimeSeriesServicer = (*captureTSSvc)(nil)

// ---------------------------------------------------------------------------
// TSQuery: consistency routing in embedded transport
// ---------------------------------------------------------------------------

func TestEmbeddedTSQuery_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	plain := svc.calledQuery
	consist := svc.calledQueryConsist
	svc.mu.Unlock()

	if !plain {
		t.Error("expected plain Query to be called")
	}
	if consist {
		t.Error("expected QueryWithConsistency NOT to be called")
	}
}

func TestEmbeddedTSQuery_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	consist := svc.calledQueryConsist
	ropts := svc.lastQueryROpts
	svc.mu.Unlock()

	if !consist {
		t.Error("expected QueryWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}

func TestEmbeddedTSQuery_EventualConsistency_SetsAllowStale(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	ropts := svc.lastQueryROpts
	svc.mu.Unlock()

	if !ropts.AllowStale {
		t.Error("AllowStale should be true for eventual consistency")
	}
}

// ---------------------------------------------------------------------------
// TSAggregate: consistency routing in embedded transport
// ---------------------------------------------------------------------------

func TestEmbeddedTSAggregate_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    now.Add(-time.Hour).UnixNano(),
		EndNs:      now.UnixNano(),
		Function:   transport.AggAvg,
		IntervalNs: 60_000_000_000,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	svc.mu.Lock()
	plain := svc.calledAggregate
	consist := svc.calledAggConsist
	svc.mu.Unlock()

	if !plain {
		t.Error("expected plain Aggregate to be called")
	}
	if consist {
		t.Error("expected AggregateWithConsistency NOT to be called")
	}
}

func TestEmbeddedTSAggregate_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Function:    transport.AggAvg,
		IntervalNs:  60_000_000_000,
		Consistency: "linearizable",
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	svc.mu.Lock()
	consist := svc.calledAggConsist
	ropts := svc.lastAggROpts
	svc.mu.Unlock()

	if !consist {
		t.Error("expected AggregateWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", ropts.Consistency)
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery: consistency routing in embedded transport
// ---------------------------------------------------------------------------

func TestEmbeddedTSRichQuery_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	svc.mu.Lock()
	plain := svc.calledRichQuery
	consist := svc.calledRichConsist
	svc.mu.Unlock()

	if !plain {
		t.Error("expected plain QueryRich to be called")
	}
	if consist {
		t.Error("expected QueryRichWithConsistency NOT to be called")
	}
}

func TestEmbeddedTSRichQuery_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	now := time.Now().UTC()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	svc.mu.Lock()
	consist := svc.calledRichConsist
	ropts := svc.lastRichROpts
	svc.mu.Unlock()

	if !consist {
		t.Error("expected QueryRichWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", ropts.Consistency)
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics: consistency routing in embedded transport
// ---------------------------------------------------------------------------

func TestEmbeddedTSListMetrics_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	svc.mu.Lock()
	plain := svc.calledListMetrics
	consist := svc.calledListConsist
	svc.mu.Unlock()

	if !plain {
		t.Error("expected plain ListMetrics to be called")
	}
	if consist {
		t.Error("expected ListMetricsWithConsistency NOT to be called")
	}
}

func TestEmbeddedTSListMetrics_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newCaptureTSSvc()
	tr := newExtTransport(t, WithTimeSeriesService(svc))

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	svc.mu.Lock()
	consist := svc.calledListConsist
	ropts := svc.lastListROpts
	svc.mu.Unlock()

	if !consist {
		t.Error("expected ListMetricsWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}
