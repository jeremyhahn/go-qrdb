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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// GET /ts/query: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSQuery_WithConsistency_RoutesToWithConsistency verifies that
// ?consistency=eventual routes the request to QueryWithConsistency with the
// resolved Eventual ReadOptions and returns 200.
func TestHandleTSQuery_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	mock := &tsMock{
		queryWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, ropts consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []timeseries.DataPoint{}, nil
		},
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			t.Error("plain Query must not be called when consistency is set")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet,
		"/ts/query?metric=cpu&start_ns=1&end_ns=2&consistency=eventual",
		nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !withConsistencyCalled {
		t.Fatal("QueryWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", capturedOpts.Consistency)
	}
	if !capturedOpts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

// TestHandleTSQuery_StaleParam_RoutesToWithConsistency verifies that the
// legacy ?stale=true parameter is treated as Eventual consistency.
func TestHandleTSQuery_StaleParam_RoutesToWithConsistency(t *testing.T) {
	withConsistencyCalled := false

	mock := &tsMock{
		queryWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, ropts consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			withConsistencyCalled = true
			if ropts.Consistency != consistency.Eventual {
				t.Errorf("consistency = %v, want Eventual", ropts.Consistency)
			}
			return []timeseries.DataPoint{}, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet,
		"/ts/query?metric=cpu&start_ns=1&end_ns=2&stale=true",
		nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !withConsistencyCalled {
		t.Fatal("QueryWithConsistency was not called for ?stale=true")
	}
}

// TestHandleTSQuery_NoConsistency_RoutesToPlain verifies that an absent
// consistency parameter routes to the plain Query method.
func TestHandleTSQuery_NoConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	mock := &tsMock{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			plainCalled = true
			return []timeseries.DataPoint{}, nil
		},
		queryWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			t.Error("QueryWithConsistency must not be called when consistency is absent")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=1&end_ns=2", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("plain Query was not called")
	}
}

// ---------------------------------------------------------------------------
// GET /ts/aggregate: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSAggregate_WithConsistency_RoutesToWithConsistency verifies that
// ?consistency=strong routes to AggregateWithConsistency with Strong ReadOptions.
func TestHandleTSAggregate_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	mock := &tsMock{
		aggregateWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []timeseries.AggResult{}, nil
		},
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			t.Error("plain Aggregate must not be called when consistency is set")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet,
		"/ts/aggregate?metric=cpu&start_ns=1&end_ns=2&function=avg&consistency=strong",
		nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !withConsistencyCalled {
		t.Fatal("AggregateWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", capturedOpts.Consistency)
	}
}

// TestHandleTSAggregate_NoConsistency_RoutesToPlain verifies that an absent
// consistency parameter routes to the plain Aggregate method.
func TestHandleTSAggregate_NoConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	mock := &tsMock{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			plainCalled = true
			return []timeseries.AggResult{}, nil
		},
		aggregateWithConsistencyFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
			t.Error("AggregateWithConsistency must not be called when consistency is absent")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet,
		"/ts/aggregate?metric=cpu&start_ns=1&end_ns=2&function=sum",
		nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("plain Aggregate was not called")
	}
}

// ---------------------------------------------------------------------------
// POST /ts/query-rich: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSRichQuery_WithConsistency_RoutesToWithConsistency verifies that
// ?consistency=linearizable routes to QueryRichWithConsistency with
// Linearizable ReadOptions.
func TestHandleTSRichQuery_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	mock := &tsMock{
		queryRichWithConsistencyFn: func(_ context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			t.Error("plain QueryRich must not be called when consistency is set")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu","start_ns":1,"end_ns":2}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich?consistency=linearizable",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !withConsistencyCalled {
		t.Fatal("QueryRichWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", capturedOpts.Consistency)
	}
}

// TestHandleTSRichQuery_NoConsistency_RoutesToPlain verifies that POST
// /ts/query-rich without a consistency param routes to plain QueryRich.
func TestHandleTSRichQuery_NoConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	mock := &tsMock{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			plainCalled = true
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
		queryRichWithConsistencyFn: func(_ context.Context, _ *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
			t.Error("QueryRichWithConsistency must not be called when consistency is absent")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	body := `{"metric":"cpu","start_ns":1,"end_ns":2}`
	req := httptest.NewRequest(http.MethodPost, "/ts/query-rich", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("plain QueryRich was not called")
	}
}

// ---------------------------------------------------------------------------
// GET /ts/metrics: consistency routing
// ---------------------------------------------------------------------------

// TestHandleTSListMetrics_WithConsistency_RoutesToWithConsistency verifies
// that ?consistency=strong routes to ListMetricsWithConsistency.
func TestHandleTSListMetrics_WithConsistency_RoutesToWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	withConsistencyCalled := false

	mock := &tsMock{
		listMetricsWithConsistencyFn: func(_ context.Context, _ string, _ int, ropts consistency.ReadOptions) ([]string, error) {
			capturedOpts = ropts
			withConsistencyCalled = true
			return []string{"cpu"}, nil
		},
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			t.Error("plain ListMetrics must not be called when consistency is set")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics?consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !withConsistencyCalled {
		t.Fatal("ListMetricsWithConsistency was not called")
	}
	if capturedOpts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", capturedOpts.Consistency)
	}
}

// TestHandleTSListMetrics_NoConsistency_RoutesToPlain verifies that GET
// /ts/metrics without a consistency param routes to plain ListMetrics.
func TestHandleTSListMetrics_NoConsistency_RoutesToPlain(t *testing.T) {
	plainCalled := false

	mock := &tsMock{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			plainCalled = true
			return []string{"cpu"}, nil
		},
		listMetricsWithConsistencyFn: func(_ context.Context, _ string, _ int, _ consistency.ReadOptions) ([]string, error) {
			t.Error("ListMetricsWithConsistency must not be called when consistency is absent")
			return nil, nil
		},
	}
	srv := newTSServer(mock)

	req := httptest.NewRequest(http.MethodGet, "/ts/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("plain ListMetrics was not called")
	}
}
