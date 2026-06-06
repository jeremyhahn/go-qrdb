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

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoConsistencyFromString unit tests
// ---------------------------------------------------------------------------

// TestUnix_ProtoConsistencyFromString_KnownValues verifies all documented
// string values map to the correct protobuf ConsistencyLevel enum.
func TestUnix_ProtoConsistencyFromString_KnownValues(t *testing.T) {
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

// TestUnix_ProtoConsistencyFromString_EmptyIsUnspecified ensures that an empty
// Consistency field results in UNSPECIFIED.
func TestUnix_ProtoConsistencyFromString_EmptyIsUnspecified(t *testing.T) {
	got := protoConsistencyFromString("")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Fatalf("empty string should produce UNSPECIFIED, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests: SDK unix client Consistency → server routes correctly
// ---------------------------------------------------------------------------

// TestUnixRT_TSQuery_Consistency performs a round-trip with strong consistency
// over a Unix domain socket.
func TestUnixRT_TSQuery_Consistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSQuery_NoConsistency verifies the unspecified (plain) path
// for TSQuery over Unix domain socket.
func TestUnixRT_TSQuery_NoConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSAggregate_Consistency performs a round-trip with eventual
// consistency.
func TestUnixRT_TSAggregate_Consistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSAggregate_NoConsistency verifies the plain path for TSAggregate.
func TestUnixRT_TSAggregate_NoConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSRichQuery_Consistency performs a round-trip with linearizable
// consistency.
func TestUnixRT_TSRichQuery_Consistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSRichQuery_NoConsistency verifies the plain path for
// TSRichQuery when Consistency is empty.
func TestUnixRT_TSRichQuery_NoConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnixRT_TSListMetrics_Consistency performs a round-trip with strong
// consistency.
func TestUnixRT_TSListMetrics_Consistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (strong): %v", err)
	}
	if len(resp.Metrics) == 0 {
		t.Fatal("expected non-empty metrics list")
	}
}

// TestUnixRT_TSListMetrics_NoConsistency verifies the plain path for
// TSListMetrics when Consistency is empty.
func TestUnixRT_TSListMetrics_NoConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "mem",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (unspecified): %v", err)
	}
	if len(resp.Metrics) == 0 {
		t.Fatal("expected non-empty metrics list")
	}
}

// ---------------------------------------------------------------------------
// Invalid Consistency rejection tests (no server call must occur)
// ---------------------------------------------------------------------------

// TestUnix_TSQuery_InvalidConsistency verifies that a non-empty unrecognised
// Consistency string is rejected with *transport.RequestError before any RPC.
func TestUnix_TSQuery_InvalidConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSQuery_ValidConsistency_Eventual verifies that "eventual" passes
// validation and the RPC completes successfully.
func TestUnix_TSQuery_ValidConsistency_Eventual(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSAggregate_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestUnix_TSAggregate_InvalidConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSAggregate_ValidConsistency_Eventual verifies that "eventual"
// passes validation and the RPC completes successfully.
func TestUnix_TSAggregate_ValidConsistency_Eventual(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSRichQuery_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestUnix_TSRichQuery_InvalidConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSRichQuery_ValidConsistency_Eventual verifies that "eventual"
// passes validation and the RPC completes successfully.
func TestUnix_TSRichQuery_ValidConsistency_Eventual(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	now := time.Now()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSRichQuery (eventual): %v", err)
	}
}

// TestUnix_TSListMetrics_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestUnix_TSListMetrics_InvalidConsistency(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
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

// TestUnix_TSListMetrics_ValidConsistency_Eventual verifies that "eventual"
// passes validation and the RPC completes successfully.
func TestUnix_TSListMetrics_ValidConsistency_Eventual(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSListMetrics (eventual): %v", err)
	}
	if len(resp.Metrics) == 0 {
		t.Fatal("expected non-empty metrics list")
	}
}
