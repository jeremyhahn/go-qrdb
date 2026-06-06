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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoConsistencyFromString unit tests
// ---------------------------------------------------------------------------

// TestProtoConsistencyFromString_KnownValues verifies all documented string
// values map to the correct protobuf ConsistencyLevel enum.
func TestProtoConsistencyFromString_KnownValues(t *testing.T) {
	tests := []struct {
		input string
		want  pb.ConsistencyLevel
	}{
		{"strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG},
		{"linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE},
		{"eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL},
		{"stale", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL},
		{"", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED},
		{"unknown", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED},
		{"STRONG", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED}, // case-sensitive
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := protoConsistencyFromString(tc.input)
			if got != tc.want {
				t.Fatalf("protoConsistencyFromString(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestProtoConsistencyFromString_EmptyIsUnspecified ensures that an empty
// Consistency field results in UNSPECIFIED, preserving backward-compatible
// default behaviour for existing callers.
func TestProtoConsistencyFromString_EmptyIsUnspecified(t *testing.T) {
	got := protoConsistencyFromString("")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Fatalf("empty string should produce UNSPECIFIED, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests: SDK client Consistency string → server routes correctly
// ---------------------------------------------------------------------------

// TestTSQuery_RoundTrip_Consistency performs an end-to-end round-trip where
// the SDK transport sends a strong-consistency TSQuery and verifies a
// successful response is returned.
func TestTSQuery_RoundTrip_Consistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSQuery (strong): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSQuery_RoundTrip_NoConsistency verifies that an empty Consistency
// string results in a successful call through the default (plain) code path.
func TestTSQuery_RoundTrip_NoConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery (unspecified): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSAggregate_RoundTrip_Consistency performs a round-trip with eventual
// consistency and verifies the response is well-formed.
func TestTSAggregate_RoundTrip_Consistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Function:    transport.AggAvg,
		IntervalNs:  int64(time.Minute),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSAggregate (eventual): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSAggregate_RoundTrip_NoConsistency verifies that an empty Consistency
// field works correctly through the default (plain) code path.
func TestTSAggregate_RoundTrip_NoConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    now.Add(-time.Hour).UnixNano(),
		EndNs:      now.UnixNano(),
		Function:   transport.AggSum,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("TSAggregate (unspecified): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSRichQuery_RoundTrip_Consistency performs a round-trip with linearizable
// consistency.
func TestTSRichQuery_RoundTrip_Consistency(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{
		Metric: "cpu",
		Points: []*timeseries.DataPoint{},
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "linearizable",
	})
	if err != nil {
		t.Fatalf("TSRichQuery (linearizable): %v", err)
	}
}

// TestTSRichQuery_RoundTrip_NoConsistency verifies the plain path for
// TSRichQuery when Consistency is empty.
func TestTSRichQuery_RoundTrip_NoConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{
		Metric: "cpu",
		Points: []*timeseries.DataPoint{},
	}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	now := time.Now()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery (unspecified): %v", err)
	}
}

// TestTSListMetrics_RoundTrip_Consistency performs a round-trip with strong
// consistency and verifies the returned metrics list.
func TestTSListMetrics_RoundTrip_Consistency(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsRes = []string{"cpu.usage"}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (strong): %v", err)
	}
	if len(resp.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(resp.Metrics))
	}
}

// TestTSListMetrics_RoundTrip_NoConsistency verifies the plain path for
// TSListMetrics when Consistency is empty.
func TestTSListMetrics_RoundTrip_NoConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsRes = []string{"mem.usage"}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "mem",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (unspecified): %v", err)
	}
	if len(resp.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(resp.Metrics))
	}
}

// ---------------------------------------------------------------------------
// Invalid Consistency rejection tests (no server call must occur)
// ---------------------------------------------------------------------------

// TestTSQuery_InvalidConsistency verifies that a non-empty unrecognized
// Consistency string is rejected before any RPC is made.
func TestTSQuery_InvalidConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		Consistency: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid consistency")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TSQuery" {
		t.Fatalf("expected Method TSQuery, got %q", reqErr.Method)
	}
}

// TestTSQuery_ValidConsistency_Eventual verifies that "eventual" is accepted
// and the RPC completes successfully.
func TestTSQuery_ValidConsistency_Eventual(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSQuery (eventual): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSAggregate_InvalidConsistency verifies that an invalid Consistency value
// is rejected before any RPC is made.
func TestTSAggregate_InvalidConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		Consistency: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid consistency")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TSAggregate" {
		t.Fatalf("expected Method TSAggregate, got %q", reqErr.Method)
	}
}

// TestTSAggregate_ValidConsistency_Eventual verifies that "eventual" is
// accepted and the RPC completes successfully.
func TestTSAggregate_ValidConsistency_Eventual(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSAggregate (eventual): %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestTSRichQuery_InvalidConsistency verifies that an invalid Consistency value
// is rejected before any RPC is made.
func TestTSRichQuery_InvalidConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{Metric: "cpu", Points: []*timeseries.DataPoint{}}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		Consistency: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid consistency")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TSRichQuery" {
		t.Fatalf("expected Method TSRichQuery, got %q", reqErr.Method)
	}
}

// TestTSRichQuery_ValidConsistency_Eventual verifies that "eventual" is
// accepted and the RPC completes successfully.
func TestTSRichQuery_ValidConsistency_Eventual(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.richQueryResult = &timeseries.QueryResult{Metric: "cpu", Points: []*timeseries.DataPoint{}}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSRichQuery (eventual): %v", err)
	}
}

// TestTSListMetrics_InvalidConsistency verifies that an invalid Consistency
// value is rejected before any RPC is made.
func TestTSListMetrics_InvalidConsistency(t *testing.T) {
	tsSvc := newMockTSService()
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid consistency")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TSListMetrics" {
		t.Fatalf("expected Method TSListMetrics, got %q", reqErr.Method)
	}
}

// TestTSListMetrics_ValidConsistency_Eventual verifies that "eventual" is
// accepted and the RPC completes successfully.
func TestTSListMetrics_ValidConsistency_Eventual(t *testing.T) {
	tsSvc := newMockTSService()
	tsSvc.listMetricsRes = []string{"cpu.usage"}
	tr, cleanup := connectTSTransport(t, tsSvc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (eventual): %v", err)
	}
	if len(resp.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(resp.Metrics))
	}
}
