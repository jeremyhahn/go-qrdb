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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoConsistencyFromString
// ---------------------------------------------------------------------------

// TestProtoConsistencyFromString_Strong verifies "strong" maps to STRONG.
func TestProtoConsistencyFromString_Strong(t *testing.T) {
	got := protoConsistencyFromString("strong")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG {
		t.Errorf("protoConsistencyFromString(%q) = %v, want STRONG", "strong", got)
	}
}

// TestProtoConsistencyFromString_Linearizable verifies "linearizable" maps to LINEARIZABLE.
func TestProtoConsistencyFromString_Linearizable(t *testing.T) {
	got := protoConsistencyFromString("linearizable")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE {
		t.Errorf("protoConsistencyFromString(%q) = %v, want LINEARIZABLE", "linearizable", got)
	}
}

// TestProtoConsistencyFromString_Eventual verifies "eventual" maps to EVENTUAL.
func TestProtoConsistencyFromString_Eventual(t *testing.T) {
	got := protoConsistencyFromString("eventual")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("protoConsistencyFromString(%q) = %v, want EVENTUAL", "eventual", got)
	}
}

// TestProtoConsistencyFromString_Stale verifies "stale" maps to EVENTUAL.
func TestProtoConsistencyFromString_Stale(t *testing.T) {
	got := protoConsistencyFromString("stale")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("protoConsistencyFromString(%q) = %v, want EVENTUAL", "stale", got)
	}
}

// TestProtoConsistencyFromString_Empty verifies "" maps to UNSPECIFIED.
func TestProtoConsistencyFromString_Empty(t *testing.T) {
	got := protoConsistencyFromString("")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("protoConsistencyFromString(%q) = %v, want UNSPECIFIED", "", got)
	}
}

// TestProtoConsistencyFromString_Unknown verifies unknown strings map to UNSPECIFIED.
func TestProtoConsistencyFromString_Unknown(t *testing.T) {
	got := protoConsistencyFromString("unknown-value")
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("protoConsistencyFromString(%q) = %v, want UNSPECIFIED", "unknown-value", got)
	}
}

// ---------------------------------------------------------------------------
// Proto round-trip: TSQuery Consistency field encoding
// ---------------------------------------------------------------------------

// TestTSQuery_ProtoConsistencyField_Encoded verifies that TSQuery serialises
// the Consistency field from the SDK request into the proto wire message. The
// test builds the same proto message the SDK would marshal and confirms the
// field survives marshal → unmarshal.
func TestTSQuery_ProtoConsistencyField_Encoded(t *testing.T) {
	req := &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: "eventual",
	}

	pbMsg := &pb.TSQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Consistency: protoConsistencyFromString(req.Consistency),
	}

	payload, err := proto.Marshal(pbMsg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	var decoded pb.TSQueryRequest
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if decoded.GetConsistency() != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("Consistency = %v, want EVENTUAL", decoded.GetConsistency())
	}
}

// TestTSQuery_ProtoConsistencyField_UnspecifiedWhenEmpty verifies that an
// empty Consistency string produces UNSPECIFIED in the proto wire message.
func TestTSQuery_ProtoConsistencyField_UnspecifiedWhenEmpty(t *testing.T) {
	req := &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	}

	pbMsg := &pb.TSQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Consistency: protoConsistencyFromString(req.Consistency),
	}

	payload, err := proto.Marshal(pbMsg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	var decoded pb.TSQueryRequest
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if decoded.GetConsistency() != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("Consistency = %v, want UNSPECIFIED", decoded.GetConsistency())
	}
}

// ---------------------------------------------------------------------------
// Proto round-trip: TSAggregate Consistency field encoding
// ---------------------------------------------------------------------------

// TestTSAggregate_ProtoConsistencyField_Encoded verifies that TSAggregate
// correctly encodes the Consistency field in the proto wire message.
func TestTSAggregate_ProtoConsistencyField_Encoded(t *testing.T) {
	req := &transport.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Function:    transport.AggAvg,
		Consistency: "strong",
	}

	pbMsg := &pb.TSAggregateRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Function:    aggFuncToProto[req.Function],
		Consistency: protoConsistencyFromString(req.Consistency),
	}

	payload, err := proto.Marshal(pbMsg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	var decoded pb.TSAggregateRequest
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if decoded.GetConsistency() != pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG {
		t.Errorf("Consistency = %v, want STRONG", decoded.GetConsistency())
	}
}

// TestTSAggregate_ProtoConsistencyField_UnspecifiedWhenEmpty verifies that an
// empty Consistency string produces UNSPECIFIED.
func TestTSAggregate_ProtoConsistencyField_UnspecifiedWhenEmpty(t *testing.T) {
	req := &transport.TSAggregateRequest{
		Metric:   "cpu",
		StartNs:  1,
		EndNs:    2,
		Function: transport.AggSum,
	}

	pbMsg := &pb.TSAggregateRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Function:    aggFuncToProto[req.Function],
		Consistency: protoConsistencyFromString(req.Consistency),
	}

	payload, err := proto.Marshal(pbMsg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	var decoded pb.TSAggregateRequest
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if decoded.GetConsistency() != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("Consistency = %v, want UNSPECIFIED", decoded.GetConsistency())
	}
}

// ---------------------------------------------------------------------------
// JSON wire: TSRichQuery Consistency field encoding
// ---------------------------------------------------------------------------

// TestTSRichQuery_JSONConsistencyField_Encoded verifies that TSRichQuery
// encodes req.Consistency into the JSON wire request struct.
func TestTSRichQuery_JSONConsistencyField_Encoded(t *testing.T) {
	req := &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     1,
		EndNs:       2,
		Consistency: "linearizable",
	}

	wireReq := tsRichQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Aggregation: int(req.Aggregation),
		Consistency: req.Consistency,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded tsRichQueryRequest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Consistency != "linearizable" {
		t.Errorf("Consistency = %q, want %q", decoded.Consistency, "linearizable")
	}
}

// TestTSRichQuery_JSONConsistencyField_EmptyOmitted verifies that an empty
// Consistency string is omitted from the JSON wire payload.
func TestTSRichQuery_JSONConsistencyField_EmptyOmitted(t *testing.T) {
	wireReq := tsRichQueryRequest{
		Metric:  "cpu",
		StartNs: 1,
		EndNs:   2,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if _, ok := m["consistency"]; ok {
		t.Error("consistency field should be omitted when empty")
	}
}

// ---------------------------------------------------------------------------
// JSON wire: TSListMetrics Consistency field encoding
// ---------------------------------------------------------------------------

// TestTSListMetrics_JSONConsistencyField_Encoded verifies that TSListMetrics
// encodes req.Consistency into the JSON wire request struct.
func TestTSListMetrics_JSONConsistencyField_Encoded(t *testing.T) {
	req := &transport.TSListMetricsRequest{
		Prefix:      "cpu.",
		Consistency: "strong",
	}

	wireReq := tsListMetricsRequest{
		Prefix:      req.Prefix,
		Limit:       req.Limit,
		Consistency: req.Consistency,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded tsListMetricsRequest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Consistency != "strong" {
		t.Errorf("Consistency = %q, want %q", decoded.Consistency, "strong")
	}
}

// TestTSListMetrics_JSONConsistencyField_EmptyOmitted verifies that an empty
// Consistency string is omitted from the JSON wire payload.
func TestTSListMetrics_JSONConsistencyField_EmptyOmitted(t *testing.T) {
	wireReq := tsListMetricsRequest{
		Prefix: "cpu.",
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if _, ok := m["consistency"]; ok {
		t.Error("consistency field should be omitted when empty")
	}
}

// ---------------------------------------------------------------------------
// Invalid Consistency rejection tests (no server call must occur)
//
// The validation guard fires before any network I/O, so we use a freshly
// created (not-connected, not-closed) Transport backed by a dummy address.
// ---------------------------------------------------------------------------

// newOpenTransport returns a Transport that is open (not closed) but not
// connected to any server, sufficient for pre-RPC validation tests.
func newOpenTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// TestQUIC_TSQuery_InvalidConsistency verifies that a non-empty unrecognised
// Consistency string is rejected with *transport.RequestError before any RPC.
func TestQUIC_TSQuery_InvalidConsistency(t *testing.T) {
	tr := newOpenTransport(t)
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

// TestQUIC_TSQuery_EmptyConsistency_Accepted verifies that an empty Consistency
// string does not trigger InvalidConsistencyLevelError. The guard is only
// entered when req.Consistency != "".
func TestQUIC_TSQuery_EmptyConsistency_Accepted(t *testing.T) {
	// Confirm the contract: ParseConsistencyLevel("") returns no error,
	// so the guard correctly skips validation for empty strings.
	if _, err := transport.ParseConsistencyLevel(""); err != nil {
		t.Fatalf("ParseConsistencyLevel(%q) must not error: %v", "", err)
	}
	// Confirm that a closed transport (which returns ClosedError before
	// the guard) does not return an InvalidConsistencyLevelError.
	tr := newOpenTransport(t)
	_ = tr.Close()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		Consistency: "",
	})
	var invErr *transport.InvalidConsistencyLevelError
	if errors.As(err, &invErr) {
		t.Fatalf("empty consistency must not produce InvalidConsistencyLevelError: %v", err)
	}
}

// TestQUIC_TSAggregate_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestQUIC_TSAggregate_InvalidConsistency(t *testing.T) {
	tr := newOpenTransport(t)
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

// TestQUIC_TSAggregate_EmptyConsistency_Accepted verifies that empty Consistency
// does not trigger InvalidConsistencyLevelError.
func TestQUIC_TSAggregate_EmptyConsistency_Accepted(t *testing.T) {
	tr := newOpenTransport(t)
	_ = tr.Close()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		Consistency: "",
	})
	var invErr *transport.InvalidConsistencyLevelError
	if errors.As(err, &invErr) {
		t.Fatalf("empty consistency must not produce InvalidConsistencyLevelError: %v", err)
	}
}

// TestQUIC_TSRichQuery_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestQUIC_TSRichQuery_InvalidConsistency(t *testing.T) {
	tr := newOpenTransport(t)
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

// TestQUIC_TSRichQuery_EmptyConsistency_Accepted verifies that empty Consistency
// does not trigger InvalidConsistencyLevelError.
func TestQUIC_TSRichQuery_EmptyConsistency_Accepted(t *testing.T) {
	tr := newOpenTransport(t)
	_ = tr.Close()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		Consistency: "",
	})
	var invErr *transport.InvalidConsistencyLevelError
	if errors.As(err, &invErr) {
		t.Fatalf("empty consistency must not produce InvalidConsistencyLevelError: %v", err)
	}
}

// TestQUIC_TSListMetrics_InvalidConsistency verifies that an invalid Consistency
// value is rejected with *transport.RequestError before any RPC.
func TestQUIC_TSListMetrics_InvalidConsistency(t *testing.T) {
	tr := newOpenTransport(t)
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

// TestQUIC_TSListMetrics_EmptyConsistency_Accepted verifies that empty
// Consistency does not trigger InvalidConsistencyLevelError.
func TestQUIC_TSListMetrics_EmptyConsistency_Accepted(t *testing.T) {
	tr := newOpenTransport(t)
	_ = tr.Close()
	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "",
	})
	var invErr *transport.InvalidConsistencyLevelError
	if errors.As(err, &invErr) {
		t.Fatalf("empty consistency must not produce InvalidConsistencyLevelError: %v", err)
	}
}
