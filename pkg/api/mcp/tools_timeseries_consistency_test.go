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
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// capturingTSSvc records which variant of each method was called.
// ---------------------------------------------------------------------------

type capturingTSSvc struct {
	mu sync.Mutex

	calledQuery        bool
	calledQueryConsist bool
	lastQueryROpts     consistency.ReadOptions
	calledAggregate    bool
	calledAggConsist   bool
	lastAggROpts       consistency.ReadOptions
	calledQueryRich    bool
	calledRichConsist  bool
	lastRichROpts      consistency.ReadOptions
	calledListMetrics  bool
	calledListConsist  bool
	lastListROpts      consistency.ReadOptions
}

func newCapturingTSSvc() *capturingTSSvc { return &capturingTSSvc{} }

func (m *capturingTSSvc) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return nil
}
func (m *capturingTSSvc) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error { return nil }

func (m *capturingTSSvc) Query(_ context.Context, metric string, start, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	m.calledQuery = true
	m.mu.Unlock()
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *capturingTSSvc) QueryWithConsistency(_ context.Context, metric string, start, _ time.Time, ropts consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.mu.Lock()
	m.calledQueryConsist = true
	m.lastQueryROpts = ropts
	m.mu.Unlock()
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *capturingTSSvc) Aggregate(_ context.Context, _ string, start, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	m.calledAggregate = true
	m.mu.Unlock()
	return []timeseries.AggResult{{Timestamp: start, Value: 1.0, Count: 1}}, nil
}

func (m *capturingTSSvc) AggregateWithConsistency(_ context.Context, _ string, start, _ time.Time, _ timeseries.AggFunc, _ time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	m.mu.Lock()
	m.calledAggConsist = true
	m.lastAggROpts = ropts
	m.mu.Unlock()
	return []timeseries.AggResult{{Timestamp: start, Value: 1.0, Count: 1}}, nil
}

func (m *capturingTSSvc) WritePoint(_ context.Context, _ *timeseries.DataPoint) error { return nil }

func (m *capturingTSSvc) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	m.mu.Lock()
	m.calledQueryRich = true
	m.mu.Unlock()
	return &timeseries.QueryResult{}, nil
}

func (m *capturingTSSvc) QueryRichWithConsistency(_ context.Context, _ *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	m.mu.Lock()
	m.calledRichConsist = true
	m.lastRichROpts = ropts
	m.mu.Unlock()
	return &timeseries.QueryResult{}, nil
}

func (m *capturingTSSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}

func (m *capturingTSSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return &timeseries.RetentionPolicy{}, nil
}

func (m *capturingTSSvc) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return nil
}

func (m *capturingTSSvc) ApplyRetention(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *capturingTSSvc) ListMetrics(_ string, _ int) ([]string, error) {
	m.mu.Lock()
	m.calledListMetrics = true
	m.mu.Unlock()
	return []string{"cpu"}, nil
}

func (m *capturingTSSvc) ListMetricsWithConsistency(_ context.Context, _ string, _ int, ropts consistency.ReadOptions) ([]string, error) {
	m.mu.Lock()
	m.calledListConsist = true
	m.lastListROpts = ropts
	m.mu.Unlock()
	return []string{"cpu"}, nil
}

// ---------------------------------------------------------------------------
// qrdb_ts_query consistency routing
// ---------------------------------------------------------------------------

func TestHandleTSQuery_NoConsistency_UsesPlainQuery(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric": "cpu",
		"start":  float64(now - int64(time.Hour)),
		"end":    float64(now),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error")
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

func TestHandleTSQuery_WithConsistency_UsesConsistencyMethod(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"consistency": "strong",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	svc.mu.Lock()
	consist := svc.calledQueryConsist
	ropts := svc.lastQueryROpts
	svc.mu.Unlock()
	if !consist {
		t.Error("expected QueryWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency level: %v, want Strong", ropts.Consistency)
	}
}

func TestHandleTSQuery_InvalidConsistency_ReturnsToolError(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"consistency": "invalid-level",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected tool result error for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// qrdb_ts_aggregate consistency routing
// ---------------------------------------------------------------------------

func TestHandleTSAggregate_NoConsistency_UsesPlainAggregate(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSAggregate(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"func":        "avg",
		"interval_ns": float64(60_000_000_000),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
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

func TestHandleTSAggregate_WithConsistency_UsesConsistencyMethod(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSAggregate(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"func":        "avg",
		"interval_ns": float64(60_000_000_000),
		"consistency": "eventual",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	svc.mu.Lock()
	consist := svc.calledAggConsist
	ropts := svc.lastAggROpts
	svc.mu.Unlock()
	if !consist {
		t.Error("expected AggregateWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Eventual {
		t.Errorf("consistency level: %v, want Eventual", ropts.Consistency)
	}
	if !ropts.AllowStale {
		t.Error("AllowStale should be true for eventual consistency")
	}
}

func TestHandleTSAggregate_InvalidConsistency_ReturnsToolError(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSAggregate(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"func":        "avg",
		"interval_ns": float64(60_000_000_000),
		"consistency": "bad",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected tool result error for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// qrdb_ts_rich_query consistency routing
// ---------------------------------------------------------------------------

func TestHandleTSRichQuery_NoConsistency_UsesPlainQueryRich(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSRichQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric": "cpu",
		"start":  float64(now - int64(time.Hour)),
		"end":    float64(now),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error")
	}
	svc.mu.Lock()
	plain := svc.calledQueryRich
	consist := svc.calledRichConsist
	svc.mu.Unlock()
	if !plain {
		t.Error("expected plain QueryRich to be called")
	}
	if consist {
		t.Error("expected QueryRichWithConsistency NOT to be called")
	}
}

func TestHandleTSRichQuery_WithConsistency_UsesConsistencyMethod(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSRichQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"consistency": "linearizable",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	svc.mu.Lock()
	consist := svc.calledRichConsist
	ropts := svc.lastRichROpts
	svc.mu.Unlock()
	if !consist {
		t.Error("expected QueryRichWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Linearizable {
		t.Errorf("consistency level: %v, want Linearizable", ropts.Consistency)
	}
}

func TestHandleTSRichQuery_InvalidConsistency_ReturnsToolError(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSRichQuery(svc)
	now := time.Now().UnixNano()
	req := makeReq(map[string]any{
		"metric":      "cpu",
		"start":       float64(now - int64(time.Hour)),
		"end":         float64(now),
		"consistency": "not-a-level",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected tool result error for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// qrdb_ts_list_metrics consistency routing
// ---------------------------------------------------------------------------

func TestHandleTSListMetrics_NoConsistency_UsesPlainListMetrics(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSListMetrics(svc)
	req := makeReq(map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error")
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

func TestHandleTSListMetrics_WithConsistency_UsesConsistencyMethod(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSListMetrics(svc)
	req := makeReq(map[string]any{
		"consistency": "strong",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	svc.mu.Lock()
	consist := svc.calledListConsist
	ropts := svc.lastListROpts
	svc.mu.Unlock()
	if !consist {
		t.Error("expected ListMetricsWithConsistency to be called")
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency level: %v, want Strong", ropts.Consistency)
	}
}

func TestHandleTSListMetrics_InvalidConsistency_ReturnsToolError(t *testing.T) {
	svc := newCapturingTSSvc()
	handler := handleTSListMetrics(svc)
	req := makeReq(map[string]any{
		"consistency": "garbage",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected tool result error for invalid consistency")
	}
}
