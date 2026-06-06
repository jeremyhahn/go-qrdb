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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Helper: captures-consistency test server
// ---------------------------------------------------------------------------

// newConsistencyCapturingServer creates an httptest.Server that records the
// ?consistency= query parameter received for each of the 4 TS read endpoints.
// The capturedConsistency map keys are "query", "aggregate", "rich", "list".
func newConsistencyCapturingServer(t *testing.T, capturedConsistency map[string]string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	// GET /ts/query
	mux.HandleFunc("/ts/query", func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency["query"] = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"points": []any{}})
	})

	// GET /ts/aggregate
	mux.HandleFunc("/ts/aggregate", func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency["aggregate"] = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	})

	// POST /ts/query-rich
	mux.HandleFunc("/ts/query-rich", func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency["rich"] = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metric": "cpu",
			"points": []any{},
		})
	})

	// GET /ts/metrics
	mux.HandleFunc("/ts/metrics", func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency["list"] = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"metrics": []string{}})
	})

	return httptest.NewServer(mux)
}

// ---------------------------------------------------------------------------
// TSQuery: consistency query param emitted
// ---------------------------------------------------------------------------

// TestRESTTSQuery_ConsistencyParamEmitted verifies that the REST transport
// appends ?consistency= to GET /ts/query when req.Consistency is non-empty.
func TestRESTTSQuery_ConsistencyParamEmitted(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSQuery(t.Context(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	if captured["query"] != "eventual" {
		t.Errorf("?consistency = %q, want %q", captured["query"], "eventual")
	}
}

// TestRESTTSQuery_NoConsistencyParam_Absent verifies that when
// req.Consistency is empty, no ?consistency= query param is sent.
func TestRESTTSQuery_NoConsistencyParam_Absent(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSQuery(t.Context(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	if captured["query"] != "" {
		t.Errorf("?consistency should be absent, got %q", captured["query"])
	}
}

// ---------------------------------------------------------------------------
// TSAggregate: consistency query param emitted
// ---------------------------------------------------------------------------

// TestRESTTSAggregate_ConsistencyParamEmitted verifies that the REST transport
// appends ?consistency= to GET /ts/aggregate when req.Consistency is non-empty.
func TestRESTTSAggregate_ConsistencyParamEmitted(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSAggregate(t.Context(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Function:    transport.AggAvg,
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	if captured["aggregate"] != "strong" {
		t.Errorf("?consistency = %q, want %q", captured["aggregate"], "strong")
	}
}

// TestRESTTSAggregate_NoConsistencyParam_Absent verifies that when
// req.Consistency is empty no ?consistency= param is sent.
func TestRESTTSAggregate_NoConsistencyParam_Absent(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSAggregate(t.Context(), &transport.TSAggregateRequest{
		Metric:   "cpu",
		StartNs:  1,
		EndNs:    2,
		Function: transport.AggSum,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	if captured["aggregate"] != "" {
		t.Errorf("?consistency should be absent, got %q", captured["aggregate"])
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery: consistency query param emitted
// ---------------------------------------------------------------------------

// TestRESTTSRichQuery_ConsistencyParamEmitted verifies that the REST transport
// appends ?consistency= to POST /ts/query-rich when req.Consistency is non-empty.
func TestRESTTSRichQuery_ConsistencyParamEmitted(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSRichQuery(t.Context(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: "linearizable",
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	if captured["rich"] != "linearizable" {
		t.Errorf("?consistency = %q, want %q", captured["rich"], "linearizable")
	}
}

// TestRESTTSRichQuery_NoConsistencyParam_Absent verifies that when
// req.Consistency is empty no ?consistency= param is sent.
func TestRESTTSRichQuery_NoConsistencyParam_Absent(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSRichQuery(t.Context(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	if captured["rich"] != "" {
		t.Errorf("?consistency should be absent, got %q", captured["rich"])
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics: consistency query param emitted
// ---------------------------------------------------------------------------

// TestRESTTSListMetrics_ConsistencyParamEmitted verifies that the REST
// transport appends ?consistency= to GET /ts/metrics when req.Consistency
// is non-empty.
func TestRESTTSListMetrics_ConsistencyParamEmitted(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSListMetrics(t.Context(), &transport.TSListMetricsRequest{
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	if captured["list"] != "eventual" {
		t.Errorf("?consistency = %q, want %q", captured["list"], "eventual")
	}
}

// TestRESTTSListMetrics_NoConsistencyParam_Absent verifies that when
// req.Consistency is empty no ?consistency= param is sent.
func TestRESTTSListMetrics_NoConsistencyParam_Absent(t *testing.T) {
	captured := make(map[string]string)
	srv := newConsistencyCapturingServer(t, captured)
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.TSListMetrics(t.Context(), &transport.TSListMetricsRequest{})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	if captured["list"] != "" {
		t.Errorf("?consistency should be absent, got %q", captured["list"])
	}
}
