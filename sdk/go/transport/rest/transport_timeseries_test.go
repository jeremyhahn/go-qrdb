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
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTSTestServer creates an httptest.Server that simulates the QRDB REST
// API time-series endpoints.
func newTSTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	type storedPoint struct {
		Metric    string            `json:"metric"`
		Value     float64           `json:"value"`
		Timestamp int64             `json:"timestamp"`
		Tags      map[string]string `json:"tags,omitempty"`
	}

	var points []storedPoint

	type retentionEntry struct {
		DurationNs   int64  `json:"duration_ns"`
		ResolutionNs int64  `json:"resolution_ns,omitempty"`
		Aggregation  string `json:"aggregation,omitempty"`
	}

	retentions := make(map[string]*retentionEntry)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Health endpoint.
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}

		// POST /ts/write
		if r.URL.Path == "/ts/write" && r.Method == http.MethodPost {
			var req storedPoint
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			points = append(points, req)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		// POST /ts/write-batch
		if r.URL.Path == "/ts/write-batch" && r.Method == http.MethodPost {
			var req struct {
				Points []storedPoint `json:"points"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			points = append(points, req.Points...)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		// POST /ts/write-point
		if r.URL.Path == "/ts/write-point" && r.Method == http.MethodPost {
			var req storedPoint
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			points = append(points, req)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// GET /ts/query
		if r.URL.Path == "/ts/query" && r.Method == http.MethodGet {
			q := r.URL.Query()
			metric := q.Get("metric")
			if metric == "" {
				http.Error(w, `{"error":"missing metric"}`, http.StatusBadRequest)
				return
			}

			var matched []storedPoint
			for _, p := range points {
				if p.Metric == metric {
					matched = append(matched, p)
				}
			}

			type resp struct {
				Points []storedPoint `json:"points"`
			}
			if matched == nil {
				matched = []storedPoint{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp{Points: matched})
			return
		}

		// GET /ts/aggregate
		if r.URL.Path == "/ts/aggregate" && r.Method == http.MethodGet {
			type aggResult struct {
				Timestamp int64   `json:"timestamp"`
				Value     float64 `json:"value"`
				Count     int64   `json:"count"`
			}
			type resp struct {
				Results []aggResult `json:"results"`
			}

			// Return a single synthetic result for testing.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp{
				Results: []aggResult{
					{Timestamp: 1000, Value: 42.5, Count: 3},
				},
			})
			return
		}

		// POST /ts/query-rich
		if r.URL.Path == "/ts/query-rich" && r.Method == http.MethodPost {
			var req struct {
				Metric string `json:"metric"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}

			aggVal := 99.5
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metric":           req.Metric,
				"tags":             map[string]string{"env": "prod"},
				"points":           []storedPoint{{Metric: req.Metric, Value: 10.0, Timestamp: 500, Tags: map[string]string{"host": "a"}}},
				"aggregated_value": aggVal,
				"grouped_results": []map[string]any{
					{
						"tags":             map[string]string{"region": "us"},
						"points":           []storedPoint{{Metric: req.Metric, Value: 5.0, Timestamp: 600}},
						"aggregated_value": 5.0,
					},
				},
			})
			return
		}

		// /ts/series/ prefix for delete + retention.
		if len(r.URL.Path) > 11 && r.URL.Path[:11] == "/ts/series/" {
			rest := strings.TrimPrefix(r.URL.Path, "/ts/series/")
			if rest == "" {
				http.Error(w, `{"error":"missing metric"}`, http.StatusBadRequest)
				return
			}

			// Check for /retention suffix.
			if metric, found := strings.CutSuffix(rest, "/retention"); found && metric != "" {
				switch r.Method {
				case http.MethodGet:
					w.Header().Set("Content-Type", "application/json")
					policy := retentions[metric]
					var policyJSON any
					if policy != nil {
						policyJSON = policy
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"metric": metric,
						"policy": policyJSON,
					})
					return
				case http.MethodPut:
					var req retentionEntry
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
						return
					}
					retentions[metric] = &req
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
					return
				default:
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
			}

			// DELETE /ts/series/{metric}
			if r.Method == http.MethodDelete {
				metric := rest
				var remaining []storedPoint
				for _, p := range points {
					if p.Metric != metric {
						remaining = append(remaining, p)
					}
				}
				points = remaining
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}

			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// GET /ts/metrics
		if r.URL.Path == "/ts/metrics" && r.Method == http.MethodGet {
			seen := make(map[string]struct{})
			var metrics []string
			for _, p := range points {
				if _, ok := seen[p.Metric]; !ok {
					seen[p.Metric] = struct{}{}
					metrics = append(metrics, p.Metric)
				}
			}
			if metrics == nil {
				metrics = []string{}
			}
			sort.Strings(metrics)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"metrics": metrics})
			return
		}

		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

// ---------------------------------------------------------------------------
// TSWrite tests
// ---------------------------------------------------------------------------

func TestTSWrite_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       75.5,
		TimestampNs: 1000,
		Tags:        []transport.TSTag{{Key: "host", Value: "a"}},
	})
	if err != nil {
		t.Fatalf("TSWrite failed: %v", err)
	}
}

func TestTSWrite_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric: "cpu.usage",
		Value:  1.0,
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSWrite_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "write failed"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSWriteBatch tests
// ---------------------------------------------------------------------------

func TestTSWriteBatch_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 75.5, TimestampNs: 1000},
		{Metric: "cpu.usage", Value: 80.0, TimestampNs: 2000},
	})
	if err != nil {
		t.Fatalf("TSWriteBatch failed: %v", err)
	}
}

func TestTSWriteBatch_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "m", Value: 1.0},
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSWriteBatch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "batch failed"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "m", Value: 1.0},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSQuery tests
// ---------------------------------------------------------------------------

func TestTSQuery_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Write a point first.
	if err := rt.TSWrite(ctx, &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       75.5,
		TimestampNs: 1000,
	}); err != nil {
		t.Fatalf("TSWrite failed: %v", err)
	}

	resp, err := rt.TSQuery(ctx, &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 0,
		EndNs:   2000,
	})
	if err != nil {
		t.Fatalf("TSQuery failed: %v", err)
	}

	if len(resp.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(resp.Points))
	}

	if resp.Points[0].Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", resp.Points[0].Metric)
	}
	if resp.Points[0].Value != 75.5 {
		t.Errorf("expected value 75.5, got %v", resp.Points[0].Value)
	}
}

func TestTSQuery_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 0,
		EndNs:   2000,
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSQuery_WithTagsAndOptions(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	// Write two points.
	ctx := context.Background()
	if err := rt.TSWrite(ctx, &transport.TSDataPoint{
		Metric:      "mem.used",
		Value:       50.0,
		TimestampNs: 100,
		Tags:        []transport.TSTag{{Key: "host", Value: "a"}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.TSQuery(ctx, &transport.TSQueryRequest{
		Metric:     "mem.used",
		StartNs:    0,
		EndNs:      1000,
		Tags:       []transport.TSTag{{Key: "host", Value: "a"}},
		Limit:      10,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("TSQuery failed: %v", err)
	}

	if len(resp.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(resp.Points))
	}
}

func TestTSQuery_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: 0,
		EndNs:   1000,
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSQuery_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: 0,
		EndNs:   1000,
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSAggregate tests
// ---------------------------------------------------------------------------

func TestTSAggregate_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu.usage",
		StartNs:    0,
		EndNs:      2000,
		Function:   transport.AggAvg,
		IntervalNs: 1000,
	})
	if err != nil {
		t.Fatalf("TSAggregate failed: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}

	if resp.Results[0].Value != 42.5 {
		t.Errorf("expected value 42.5, got %v", resp.Results[0].Value)
	}
	if resp.Results[0].Count != 3 {
		t.Errorf("expected count 3, got %d", resp.Results[0].Count)
	}
	if resp.Results[0].TimestampNs != 1000 {
		t.Errorf("expected timestamp 1000, got %d", resp.Results[0].TimestampNs)
	}
}

func TestTSAggregate_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    0,
		EndNs:      1000,
		Function:   transport.AggSum,
		IntervalNs: 500,
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSAggregate_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"aggregate failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    0,
		EndNs:      1000,
		Function:   transport.AggAvg,
		IntervalNs: 500,
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSAggregate_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    0,
		EndNs:      1000,
		Function:   transport.AggAvg,
		IntervalNs: 500,
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSWritePoint tests
// ---------------------------------------------------------------------------

func TestTSWritePoint_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric:      "disk.io",
		Value:       100.0,
		TimestampNs: 5000,
		Tags:        []transport.TSTag{{Key: "device", Value: "sda"}},
	})
	if err != nil {
		t.Fatalf("TSWritePoint failed: %v", err)
	}
}

func TestTSWritePoint_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "disk.io",
		Value:  1.0,
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSWritePoint_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "validation failed"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery tests
// ---------------------------------------------------------------------------

func TestTSRichQuery_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu.usage",
		StartNs:     0,
		EndNs:       2000,
		Aggregation: transport.AggAvg,
		IntervalNs:  1000,
		GroupBy:     []string{"region"},
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("TSRichQuery failed: %v", err)
	}

	if resp.Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", resp.Metric)
	}

	if resp.AggregatedValue == nil {
		t.Fatal("expected non-nil aggregated value")
	}
	if *resp.AggregatedValue != 99.5 {
		t.Errorf("expected aggregated value 99.5, got %v", *resp.AggregatedValue)
	}

	if len(resp.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(resp.Points))
	}

	if len(resp.GroupedResults) != 1 {
		t.Fatalf("expected 1 grouped result, got %d", len(resp.GroupedResults))
	}

	gr := resp.GroupedResults[0]
	if gr.AggregatedValue == nil || *gr.AggregatedValue != 5.0 {
		t.Errorf("expected grouped aggregated value 5.0, got %v", gr.AggregatedValue)
	}
}

func TestTSRichQuery_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: 0,
		EndNs:   1000,
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSRichQuery_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"query failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: 0,
		EndNs:   1000,
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSRichQuery_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: 0,
		EndNs:   1000,
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSRichQuery_NoAggregation(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	// AggNone should result in empty aggregation field.
	resp, err := rt.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu.usage",
		StartNs:     0,
		EndNs:       2000,
		Aggregation: transport.AggNone,
	})
	if err != nil {
		t.Fatalf("TSRichQuery failed: %v", err)
	}
	if resp.Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", resp.Metric)
	}
}

// ---------------------------------------------------------------------------
// TSDeleteSeries tests
// ---------------------------------------------------------------------------

func TestTSDeleteSeries_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Write and then delete.
	if err := rt.TSWrite(ctx, &transport.TSDataPoint{Metric: "cpu.usage", Value: 50.0, TimestampNs: 1000}); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.TSDeleteSeries(ctx, &transport.TSDeleteRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestTSDeleteSeries_WithParams(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric:  "mem.used",
		Tags:    []transport.TSTag{{Key: "host", Value: "a"}, {Key: "env", Value: "prod"}},
		StartNs: 1000,
		EndNs:   2000,
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestTSDeleteSeries_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu",
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSDeleteSeries_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"delete failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSGetRetention tests
// ---------------------------------------------------------------------------

func TestTSGetRetention_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Set a retention first.
	if err := rt.TSSetRetention(ctx, &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   86400000000000,
			ResolutionNs: 1000000000,
			Aggregation:  transport.AggAvg,
		},
	}); err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}

	resp, err := rt.TSGetRetention(ctx, &transport.TSGetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}

	if resp.Policy == nil {
		t.Fatal("expected non-nil policy")
	}
	if resp.Policy.DurationNs != 86400000000000 {
		t.Errorf("expected duration_ns 86400000000000, got %d", resp.Policy.DurationNs)
	}
	if resp.Policy.ResolutionNs != 1000000000 {
		t.Errorf("expected resolution_ns 1000000000, got %d", resp.Policy.ResolutionNs)
	}
	if resp.Policy.Aggregation != transport.AggAvg {
		t.Errorf("expected aggregation AggAvg, got %d", resp.Policy.Aggregation)
	}
}

func TestTSGetRetention_NilPolicy(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	// Query retention for a metric that has no policy set.
	resp, err := rt.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "nonexistent.metric",
	})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}

	if resp.Policy != nil {
		t.Errorf("expected nil policy, got %+v", resp.Policy)
	}
}

func TestTSGetRetention_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "cpu",
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSGetRetention_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"get retention failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSGetRetention_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected decode error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TSSetRetention tests
// ---------------------------------------------------------------------------

func TestTSSetRetention_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   86400000000000,
			ResolutionNs: 1000000000,
			Aggregation:  transport.AggSum,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
}

func TestTSSetRetention_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu",
		Policy: &transport.TSRetentionPolicy{DurationNs: 100},
	})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSSetRetention_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"set retention failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu",
		Policy: &transport.TSRetentionPolicy{DurationNs: 100},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSSetRetention_NoAggregation(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:  86400000000000,
			Aggregation: transport.AggNone,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics tests
// ---------------------------------------------------------------------------

func TestTSListMetrics_Success(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Write some points to create metrics.
	if err := rt.TSWrite(ctx, &transport.TSDataPoint{Metric: "cpu.usage", Value: 1.0, TimestampNs: 100}); err != nil {
		t.Fatal(err)
	}
	if err := rt.TSWrite(ctx, &transport.TSDataPoint{Metric: "mem.used", Value: 2.0, TimestampNs: 200}); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}

	if len(resp.Metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(resp.Metrics))
	}
}

func TestTSListMetrics_WithPrefix(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	if err := rt.TSWrite(ctx, &transport.TSDataPoint{Metric: "cpu.usage", Value: 1.0, TimestampNs: 100}); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.TSListMetrics(ctx, &transport.TSListMetricsRequest{
		Prefix: "cpu",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}

	if len(resp.Metrics) == 0 {
		t.Fatal("expected at least 1 metric")
	}
}

func TestTSListMetrics_Empty(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}

	if len(resp.Metrics) != 0 {
		t.Errorf("expected 0 metrics, got %d", len(resp.Metrics))
	}
}

func TestTSListMetrics_AfterClose(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestTSListMetrics_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"list failed"}`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error from server")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestTSListMetrics_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected decode error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tag conversion helper tests
// ---------------------------------------------------------------------------

func TestTagsSliceToMap(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		m := tagsSliceToMap(nil)
		if m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("empty", func(t *testing.T) {
		m := tagsSliceToMap([]transport.TSTag{})
		if m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("populated", func(t *testing.T) {
		m := tagsSliceToMap([]transport.TSTag{
			{Key: "host", Value: "a"},
			{Key: "env", Value: "prod"},
		})
		if len(m) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(m))
		}
		if m["host"] != "a" {
			t.Errorf("expected host=a, got %q", m["host"])
		}
		if m["env"] != "prod" {
			t.Errorf("expected env=prod, got %q", m["env"])
		}
	})
}

func TestTagsMapToSlice(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		s := tagsMapToSlice(nil)
		if s != nil {
			t.Errorf("expected nil, got %v", s)
		}
	})

	t.Run("empty", func(t *testing.T) {
		s := tagsMapToSlice(map[string]string{})
		if s != nil {
			t.Errorf("expected nil, got %v", s)
		}
	})

	t.Run("populated", func(t *testing.T) {
		s := tagsMapToSlice(map[string]string{"host": "a", "env": "prod"})
		if len(s) != 2 {
			t.Fatalf("expected 2 tags, got %d", len(s))
		}
		// Sort for deterministic comparison.
		sort.Slice(s, func(i, j int) bool { return s[i].Key < s[j].Key })
		if s[0].Key != "env" || s[0].Value != "prod" {
			t.Errorf("expected env=prod, got %s=%s", s[0].Key, s[0].Value)
		}
		if s[1].Key != "host" || s[1].Value != "a" {
			t.Errorf("expected host=a, got %s=%s", s[1].Key, s[1].Value)
		}
	})
}

func TestEncodeTagsQuery(t *testing.T) {
	result := encodeTagsQuery([]transport.TSTag{
		{Key: "host", Value: "a"},
		{Key: "env", Value: "prod"},
	})
	if result != "host=a,env=prod" {
		t.Errorf("expected 'host=a,env=prod', got %q", result)
	}
}

func TestAggFuncToString(t *testing.T) {
	tests := []struct {
		fn   transport.AggFunc
		want string
	}{
		{transport.AggNone, ""},
		{transport.AggAvg, "avg"},
		{transport.AggSum, "sum"},
		{transport.AggMin, "min"},
		{transport.AggMax, "max"},
		{transport.AggCount, "count"},
		{transport.AggPercentile, "percentile"},
		{transport.AggFirst, "first"},
		{transport.AggLast, "last"},
		{transport.AggFunc(255), ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := aggFuncToString(tt.fn)
			if got != tt.want {
				t.Errorf("aggFuncToString(%d) = %q, want %q", tt.fn, got, tt.want)
			}
		})
	}
}

func TestStringToAggFunc(t *testing.T) {
	tests := []struct {
		s    string
		want transport.AggFunc
	}{
		{"", transport.AggNone},
		{"avg", transport.AggAvg},
		{"sum", transport.AggSum},
		{"min", transport.AggMin},
		{"max", transport.AggMax},
		{"count", transport.AggCount},
		{"percentile", transport.AggPercentile},
		{"first", transport.AggFirst},
		{"last", transport.AggLast},
		{"unknown", transport.AggNone},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := stringToAggFunc(tt.s)
			if got != tt.want {
				t.Errorf("stringToAggFunc(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

func TestWirePointToSDK(t *testing.T) {
	wp := tsWireDataPoint{
		Metric:    "cpu.usage",
		Value:     75.5,
		Timestamp: 1000,
		Tags:      map[string]string{"host": "a"},
	}

	sdk := wirePointToSDK(wp)
	if sdk.Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", sdk.Metric)
	}
	if sdk.Value != 75.5 {
		t.Errorf("expected value 75.5, got %v", sdk.Value)
	}
	if sdk.TimestampNs != 1000 {
		t.Errorf("expected timestamp 1000, got %d", sdk.TimestampNs)
	}
	if len(sdk.Tags) != 1 || sdk.Tags[0].Key != "host" || sdk.Tags[0].Value != "a" {
		t.Errorf("unexpected tags: %v", sdk.Tags)
	}
}

func TestWirePointToSDK_NoTags(t *testing.T) {
	wp := tsWireDataPoint{
		Metric:    "mem.used",
		Value:     50.0,
		Timestamp: 2000,
	}

	sdk := wirePointToSDK(wp)
	if sdk.Tags != nil {
		t.Errorf("expected nil tags, got %v", sdk.Tags)
	}
}

// ---------------------------------------------------------------------------
// Integration-style round-trip tests
// ---------------------------------------------------------------------------

func TestTSWriteAndQuery_RoundTrip(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Write multiple points.
	if err := rt.TSWriteBatch(ctx, []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 75.5, TimestampNs: 1000, Tags: []transport.TSTag{{Key: "host", Value: "a"}}},
		{Metric: "cpu.usage", Value: 80.0, TimestampNs: 2000},
		{Metric: "mem.used", Value: 50.0, TimestampNs: 1000},
	}); err != nil {
		t.Fatalf("TSWriteBatch failed: %v", err)
	}

	// Query cpu.usage.
	resp, err := rt.TSQuery(ctx, &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 0,
		EndNs:   3000,
	})
	if err != nil {
		t.Fatalf("TSQuery failed: %v", err)
	}

	if len(resp.Points) != 2 {
		t.Errorf("expected 2 cpu.usage points, got %d", len(resp.Points))
	}

	// List metrics.
	metricsResp, err := rt.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}

	if len(metricsResp.Metrics) != 2 {
		t.Errorf("expected 2 metrics, got %d", len(metricsResp.Metrics))
	}

	// Delete cpu.usage series.
	_, err = rt.TSDeleteSeries(ctx, &transport.TSDeleteRequest{Metric: "cpu.usage"})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}

	// Verify deletion: query should return no points.
	resp, err = rt.TSQuery(ctx, &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 0,
		EndNs:   3000,
	})
	if err != nil {
		t.Fatalf("TSQuery after delete failed: %v", err)
	}

	if len(resp.Points) != 0 {
		t.Errorf("expected 0 points after delete, got %d", len(resp.Points))
	}
}

func TestTSSetAndGetRetention_RoundTrip(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Set retention.
	err := rt.TSSetRetention(ctx, &transport.TSSetRetentionRequest{
		Metric: "disk.io",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   172800000000000,
			ResolutionNs: 60000000000,
			Aggregation:  transport.AggMax,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}

	// Get retention.
	resp, err := rt.TSGetRetention(ctx, &transport.TSGetRetentionRequest{
		Metric: "disk.io",
	})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}

	if resp.Policy == nil {
		t.Fatal("expected non-nil policy")
	}
	if resp.Policy.DurationNs != 172800000000000 {
		t.Errorf("expected duration_ns 172800000000000, got %d", resp.Policy.DurationNs)
	}
	if resp.Policy.ResolutionNs != 60000000000 {
		t.Errorf("expected resolution_ns 60000000000, got %d", resp.Policy.ResolutionNs)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation tests
// ---------------------------------------------------------------------------

func TestTSWrite_ContextCanceled(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rt.TSWrite(ctx, &transport.TSDataPoint{Metric: "m", Value: 1.0})
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestTSQuery_ContextCanceled(t *testing.T) {
	srv := newTSTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.TSQuery(ctx, &transport.TSQueryRequest{Metric: "m", StartNs: 0, EndNs: 1000})
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

// ---------------------------------------------------------------------------
// Body drain tests for void-return methods
// ---------------------------------------------------------------------------

func TestTSWritePoint_BodyDrained(t *testing.T) {
	// Verify that the body is drained on NoContent responses (the defer with
	// io.Copy(io.Discard, resp.Body) is critical for HTTP/2 connection reuse).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		// Return 204 with a body (unusual but tests the drain path).
		w.WriteHeader(http.StatusNoContent)
		_, _ = io.WriteString(w, "unexpected body")
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}
