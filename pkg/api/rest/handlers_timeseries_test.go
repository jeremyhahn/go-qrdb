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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Mock with full function hooks for new TS handlers
// ---------------------------------------------------------------------------

// tsMock implements timeseries.TimeSeriesServicer with configurable hooks
// for all methods. Methods without hooks return nil/zero values.
type tsMock struct {
	writeFn                      func(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error
	writeBatchFn                 func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn                      func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	queryWithConsistencyFn       func(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn                  func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	aggregateWithConsistencyFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error)
	writePointFn                 func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn                  func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	queryRichWithConsistencyFn   func(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error)
	deleteSeriesFn               func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetentionFn               func(metric string) (*timeseries.RetentionPolicy, error)
	setRetentionFn               func(metric string, policy *timeseries.RetentionPolicy) error
	listMetricsFn                func(prefix string, limit int) ([]string, error)
	listMetricsWithConsistencyFn func(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error)
}

func (m *tsMock) Write(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, ts, tags)
	}
	return nil
}

func (m *tsMock) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *tsMock) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return nil, nil
}

func (m *tsMock) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryWithConsistencyFn != nil {
		return m.queryWithConsistencyFn(ctx, metric, start, end, ropts, opts...)
	}
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *tsMock) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return nil, nil
}

func (m *tsMock) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	if m.aggregateWithConsistencyFn != nil {
		return m.aggregateWithConsistencyFn(ctx, metric, start, end, fn, interval, ropts)
	}
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *tsMock) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *tsMock) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return nil, nil
}

func (m *tsMock) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	if m.queryRichWithConsistencyFn != nil {
		return m.queryRichWithConsistencyFn(ctx, req, ropts)
	}
	return m.QueryRich(ctx, req)
}

func (m *tsMock) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteSeriesFn != nil {
		return m.deleteSeriesFn(ctx, req)
	}
	return nil
}

func (m *tsMock) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetentionFn != nil {
		return m.getRetentionFn(metric)
	}
	return nil, nil
}

func (m *tsMock) SetRetentionPolicy(metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetentionFn != nil {
		return m.setRetentionFn(metric, policy)
	}
	return nil
}

func (m *tsMock) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return nil, nil
}

func (m *tsMock) ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	if m.listMetricsWithConsistencyFn != nil {
		return m.listMetricsWithConsistencyFn(ctx, prefix, limit, ropts)
	}
	return m.ListMetrics(prefix, limit)
}

// newTSServer creates a test Server with the given TS mock attached.
func newTSServer(mock *tsMock) *Server {
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(mock))
}

// ---------------------------------------------------------------------------
// POST /ts/write-point
// ---------------------------------------------------------------------------

func TestHandleTSWritePoint_Success(t *testing.T) {
	var captured *timeseries.DataPoint
	mock := &tsMock{
		writePointFn: func(_ context.Context, point *timeseries.DataPoint) error {
			captured = point
			return nil
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu.usage","value":42.5,"timestamp":1000000000,"tags":{"host":"a"}}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write-point", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if captured == nil {
		t.Fatal("WritePoint was not called")
	}
	if captured.Metric != "cpu.usage" {
		t.Fatalf("metric = %q, want cpu.usage", captured.Metric)
	}
	if captured.Value != 42.5 {
		t.Fatalf("value = %v, want 42.5", captured.Value)
	}
	if captured.Timestamp != time.Unix(0, 1000000000) {
		t.Fatalf("timestamp = %v, want %v", captured.Timestamp, time.Unix(0, 1000000000))
	}
	if captured.Tags["host"] != "a" {
		t.Fatalf("tags = %v, want host=a", captured.Tags)
	}
}

func TestHandleTSWritePoint_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"metric":"cpu.usage","value":42.5,"timestamp":1000000000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write-point", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSWritePoint_BadBody(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodPost, "/ts/write-point", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSWritePoint_ServiceError(t *testing.T) {
	mock := &tsMock{
		writePointFn: func(_ context.Context, _ *timeseries.DataPoint) error {
			return &service.TimeSeriesError{Op: "write_point", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu.usage","value":42.5,"timestamp":1000000000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write-point", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSWritePoint_MethodNotAllowed(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodGet, "/ts/write-point", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// POST /ts/query-rich
// ---------------------------------------------------------------------------

func TestHandleTSRichQuery_Success(t *testing.T) {
	aggVal := 42.5
	mock := &tsMock{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			if req.Metric != "cpu.usage" {
				t.Fatalf("metric = %q, want cpu.usage", req.Metric)
			}
			if req.Aggregation != timeseries.AggAvg {
				t.Fatalf("aggregation = %v, want avg", req.Aggregation)
			}
			return &timeseries.QueryResult{
				Metric: "cpu.usage",
				Points: []*timeseries.DataPoint{
					{
						Metric:    "cpu.usage",
						Value:     42.5,
						Timestamp: time.Unix(0, 1000),
						Tags:      map[string]string{"host": "a"},
					},
				},
				AggregatedValue: &aggVal,
			}, nil
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu.usage","start_ns":1000,"end_ns":2000,"aggregation":"avg"}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tsRichQueryResponse
	decodeJSON(t, rec, &resp)
	if resp.Metric != "cpu.usage" {
		t.Fatalf("metric = %q, want cpu.usage", resp.Metric)
	}
	if len(resp.Points) != 1 {
		t.Fatalf("points count = %d, want 1", len(resp.Points))
	}
	if resp.Points[0].Value != 42.5 {
		t.Fatalf("point value = %v, want 42.5", resp.Points[0].Value)
	}
	if resp.AggregatedValue == nil || *resp.AggregatedValue != 42.5 {
		t.Fatalf("aggregated_value = %v, want 42.5", resp.AggregatedValue)
	}
}

func TestHandleTSRichQuery_WithGroupBy(t *testing.T) {
	aggVal1 := 10.0
	aggVal2 := 20.0
	mock := &tsMock{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			if len(req.GroupBy) != 1 || req.GroupBy[0] != "host" {
				t.Fatalf("group_by = %v, want [host]", req.GroupBy)
			}
			return &timeseries.QueryResult{
				Metric: "cpu.usage",
				Points: []*timeseries.DataPoint{},
				GroupedResults: []*timeseries.GroupedResult{
					{
						Tags:            map[string]string{"host": "a"},
						AggregatedValue: &aggVal1,
						Points: []*timeseries.DataPoint{
							{Metric: "cpu.usage", Value: 10.0, Timestamp: time.Unix(0, 1000)},
						},
					},
					{
						Tags:            map[string]string{"host": "b"},
						AggregatedValue: &aggVal2,
					},
				},
			}, nil
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu.usage","start_ns":1000,"end_ns":2000,"aggregation":"avg","group_by":["host"]}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tsRichQueryResponse
	decodeJSON(t, rec, &resp)
	if len(resp.GroupedResults) != 2 {
		t.Fatalf("grouped_results count = %d, want 2", len(resp.GroupedResults))
	}
	if resp.GroupedResults[0].Tags["host"] != "a" {
		t.Fatalf("first group tag host = %q, want a", resp.GroupedResults[0].Tags["host"])
	}
	if resp.GroupedResults[0].AggregatedValue == nil || *resp.GroupedResults[0].AggregatedValue != 10.0 {
		t.Fatalf("first group aggregated_value = %v, want 10.0", resp.GroupedResults[0].AggregatedValue)
	}
	if len(resp.GroupedResults[0].Points) != 1 {
		t.Fatalf("first group points count = %d, want 1", len(resp.GroupedResults[0].Points))
	}
}

func TestHandleTSRichQuery_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"metric":"cpu.usage","start_ns":1000,"end_ns":2000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSRichQuery_BadBody(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSRichQuery_MissingMetric(t *testing.T) {
	srv := newTSServer(&tsMock{})

	body := `{"start_ns":1000,"end_ns":2000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSRichQuery_InvalidAggregation(t *testing.T) {
	srv := newTSServer(&tsMock{})

	body := `{"metric":"cpu.usage","start_ns":1000,"end_ns":2000,"aggregation":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSRichQuery_ServiceError(t *testing.T) {
	mock := &tsMock{
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return nil, &service.TimeSeriesError{Op: "query_rich", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu.usage","start_ns":1000,"end_ns":2000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSRichQuery_MethodNotAllowed(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodGet, "/ts/query-rich", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// DELETE /ts/series/{metric}
// ---------------------------------------------------------------------------

func TestHandleTSDeleteSeries_Success(t *testing.T) {
	var captured *timeseries.DeleteRequest
	mock := &tsMock{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			captured = req
			return nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu.usage", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil {
		t.Fatal("delete was not called")
	}
	if captured.Metric != "cpu.usage" {
		t.Fatalf("metric = %q, want cpu.usage", captured.Metric)
	}

	var resp tsDeleteSeriesResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
}

func TestHandleTSDeleteSeries_WithQueryParams(t *testing.T) {
	var captured *timeseries.DeleteRequest
	mock := &tsMock{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			captured = req
			return nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodDelete,
		"/ts/series/mem.used?start_ns=1000&end_ns=2000&tags=host%3Da%2Cenv%3Dprod", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured.Metric != "mem.used" {
		t.Fatalf("metric = %q, want mem.used", captured.Metric)
	}
	if captured.StartTime != time.Unix(0, 1000) {
		t.Fatalf("start = %v, want %v", captured.StartTime, time.Unix(0, 1000))
	}
	if captured.EndTime != time.Unix(0, 2000) {
		t.Fatalf("end = %v, want %v", captured.EndTime, time.Unix(0, 2000))
	}
	if captured.Tags["host"] != "a" || captured.Tags["env"] != "prod" {
		t.Fatalf("tags = %v, want host=a,env=prod", captured.Tags)
	}
}

func TestHandleTSDeleteSeries_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSDeleteSeries_ServiceError(t *testing.T) {
	mock := &tsMock{
		deleteSeriesFn: func(_ context.Context, _ *timeseries.DeleteRequest) error {
			return &service.TimeSeriesError{Op: "delete", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSDeleteSeries_InvalidStartNs(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu?start_ns=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSDeleteSeries_InvalidEndNs(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu?end_ns=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSSeries_MethodNotAllowed(t *testing.T) {
	srv := newTSServer(&tsMock{})

	// GET on a series path without /retention suffix should be method not allowed.
	req := httptest.NewRequest(http.MethodGet, "/ts/series/cpu", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleTSSeries_MissingMetric(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Path "/ts/series/" has len==11, dispatch checks >11, so this becomes 404.
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /ts/series/{metric}/retention
// ---------------------------------------------------------------------------

func TestHandleTSGetRetention_Success(t *testing.T) {
	mock := &tsMock{
		getRetentionFn: func(metric string) (*timeseries.RetentionPolicy, error) {
			if metric != "cpu.usage" {
				t.Fatalf("metric = %q, want cpu.usage", metric)
			}
			return &timeseries.RetentionPolicy{
				Duration:    24 * time.Hour,
				Resolution:  time.Minute,
				Aggregation: timeseries.AggAvg,
			}, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/series/cpu.usage/retention", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tsGetRetentionResponse
	decodeJSON(t, rec, &resp)
	if resp.Metric != "cpu.usage" {
		t.Fatalf("metric = %q, want cpu.usage", resp.Metric)
	}
	if resp.Policy == nil {
		t.Fatal("policy should not be nil")
	}
	if resp.Policy.DurationNs != int64(24*time.Hour) {
		t.Fatalf("duration_ns = %d, want %d", resp.Policy.DurationNs, int64(24*time.Hour))
	}
	if resp.Policy.ResolutionNs != int64(time.Minute) {
		t.Fatalf("resolution_ns = %d, want %d", resp.Policy.ResolutionNs, int64(time.Minute))
	}
	if resp.Policy.Aggregation != "avg" {
		t.Fatalf("aggregation = %q, want avg", resp.Policy.Aggregation)
	}
}

func TestHandleTSGetRetention_NilPolicy(t *testing.T) {
	mock := &tsMock{
		getRetentionFn: func(_ string) (*timeseries.RetentionPolicy, error) {
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/series/cpu/retention", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tsGetRetentionResponse
	decodeJSON(t, rec, &resp)
	if resp.Policy != nil {
		t.Fatalf("policy should be nil, got %+v", resp.Policy)
	}
}

func TestHandleTSGetRetention_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/ts/series/cpu/retention", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSGetRetention_ServiceError(t *testing.T) {
	mock := &tsMock{
		getRetentionFn: func(_ string) (*timeseries.RetentionPolicy, error) {
			return nil, &service.TimeSeriesError{Op: "get_retention", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/series/cpu/retention", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// PUT /ts/series/{metric}/retention
// ---------------------------------------------------------------------------

func TestHandleTSSetRetention_Success(t *testing.T) {
	var capturedMetric string
	var capturedPolicy *timeseries.RetentionPolicy
	mock := &tsMock{
		setRetentionFn: func(metric string, policy *timeseries.RetentionPolicy) error {
			capturedMetric = metric
			capturedPolicy = policy
			return nil
		},
	}
	srv := newTSServer(mock)

	body := `{"duration_ns":86400000000000,"resolution_ns":60000000000,"aggregation":"avg"}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu.usage/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedMetric != "cpu.usage" {
		t.Fatalf("metric = %q, want cpu.usage", capturedMetric)
	}
	if capturedPolicy.Duration != 24*time.Hour {
		t.Fatalf("duration = %v, want 24h", capturedPolicy.Duration)
	}
	if capturedPolicy.Resolution != time.Minute {
		t.Fatalf("resolution = %v, want 1m", capturedPolicy.Resolution)
	}
	if capturedPolicy.Aggregation != timeseries.AggAvg {
		t.Fatalf("aggregation = %v, want avg", capturedPolicy.Aggregation)
	}

	var resp tsSetRetentionResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
}

func TestHandleTSSetRetention_NoAggregation(t *testing.T) {
	var capturedPolicy *timeseries.RetentionPolicy
	mock := &tsMock{
		setRetentionFn: func(_ string, policy *timeseries.RetentionPolicy) error {
			capturedPolicy = policy
			return nil
		},
	}
	srv := newTSServer(mock)

	body := `{"duration_ns":3600000000000}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedPolicy.Aggregation != timeseries.AggNone {
		t.Fatalf("aggregation = %v, want none", capturedPolicy.Aggregation)
	}
}

func TestHandleTSSetRetention_BadBody(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSSetRetention_ZeroDuration(t *testing.T) {
	srv := newTSServer(&tsMock{})

	body := `{"duration_ns":0}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSSetRetention_NegativeDuration(t *testing.T) {
	srv := newTSServer(&tsMock{})

	body := `{"duration_ns":-1}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSSetRetention_InvalidAggregation(t *testing.T) {
	srv := newTSServer(&tsMock{})

	body := `{"duration_ns":3600000000000,"aggregation":"invalid"}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSSetRetention_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"duration_ns":3600000000000}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSSetRetention_ServiceError(t *testing.T) {
	mock := &tsMock{
		setRetentionFn: func(_ string, _ *timeseries.RetentionPolicy) error {
			return &service.TimeSeriesError{Op: "set_retention", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	body := `{"duration_ns":3600000000000}`
	req := httptest.NewRequest(http.MethodPut, "/ts/series/cpu/retention", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSRetention_MethodNotAllowed(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodDelete, "/ts/series/cpu/retention", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// GET /ts/metrics
// ---------------------------------------------------------------------------

func TestHandleTSListMetrics_Success(t *testing.T) {
	mock := &tsMock{
		listMetricsFn: func(prefix string, limit int) ([]string, error) {
			if prefix != "cpu" {
				t.Fatalf("prefix = %q, want cpu", prefix)
			}
			if limit != 50 {
				t.Fatalf("limit = %d, want 50", limit)
			}
			return []string{"cpu.usage", "cpu.idle", "cpu.system"}, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics?prefix=cpu&limit=50", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tsListMetricsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Metrics) != 3 {
		t.Fatalf("metrics count = %d, want 3", len(resp.Metrics))
	}
	if resp.Metrics[0] != "cpu.usage" {
		t.Fatalf("first metric = %q, want cpu.usage", resp.Metrics[0])
	}
}

func TestHandleTSListMetrics_DefaultLimit(t *testing.T) {
	var capturedLimit int
	mock := &tsMock{
		listMetricsFn: func(_ string, limit int) ([]string, error) {
			capturedLimit = limit
			return []string{}, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedLimit != 100 {
		t.Fatalf("default limit = %d, want 100", capturedLimit)
	}
}

func TestHandleTSListMetrics_EmptyResult(t *testing.T) {
	mock := &tsMock{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	// Ensure we get an empty array, not null.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["metrics"]) == "null" {
		t.Fatal("metrics should be [] not null")
	}
}

func TestHandleTSListMetrics_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSListMetrics_InvalidLimit(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics?limit=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSListMetrics_ServiceError(t *testing.T) {
	mock := &tsMock{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, &service.TimeSeriesError{Op: "list_metrics", Err: errors.New("fail")}
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSListMetrics_MethodNotAllowed(t *testing.T) {
	srv := newTSServer(&tsMock{})

	req := httptest.NewRequest(http.MethodPost, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}
