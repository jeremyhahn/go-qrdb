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
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// dataPointToProto tests
// ---------------------------------------------------------------------------

func TestDataPointToProto_Nil(t *testing.T) {
	result := dataPointToProto(nil)
	if result != nil {
		t.Errorf("dataPointToProto(nil) = %v, want nil", result)
	}
}

func TestDataPointToProto_Valid(t *testing.T) {
	dp := &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       98.5,
		TimestampNs: 1234567890,
		Tags: []transport.TSTag{
			{Key: "host", Value: "server-1"},
			{Key: "region", Value: "us-east"},
		},
	}
	result := dataPointToProto(dp)
	if result == nil {
		t.Fatal("dataPointToProto() returned nil for valid input")
	}
	if result.Metric != "cpu.usage" {
		t.Errorf("Metric = %q, want %q", result.Metric, "cpu.usage")
	}
	if result.Value != 98.5 {
		t.Errorf("Value = %f, want 98.5", result.Value)
	}
	if result.TimestampNs != 1234567890 {
		t.Errorf("TimestampNs = %d, want 1234567890", result.TimestampNs)
	}
	if len(result.Tags) != 2 {
		t.Fatalf("Tags length = %d, want 2", len(result.Tags))
	}
	if result.Tags[0].Key != "host" || result.Tags[0].Value != "server-1" {
		t.Errorf("Tags[0] = %v, want {host, server-1}", result.Tags[0])
	}
}

func TestDataPointToProto_NoTags(t *testing.T) {
	dp := &transport.TSDataPoint{
		Metric:      "mem.free",
		Value:       1024.0,
		TimestampNs: 9876543210,
	}
	result := dataPointToProto(dp)
	if result == nil {
		t.Fatal("dataPointToProto() returned nil for valid input")
	}
	if result.Tags != nil {
		t.Errorf("Tags = %v, want nil", result.Tags)
	}
}

// ---------------------------------------------------------------------------
// protoToDataPoint tests
// ---------------------------------------------------------------------------

func TestProtoToDataPoint_Valid(t *testing.T) {
	p := &pb.TSDataPoint{
		Metric:      "disk.io",
		Value:       42.0,
		TimestampNs: 5555555555,
		Tags: []*pb.TSTag{
			{Key: "device", Value: "sda1"},
		},
	}
	result := protoToDataPoint(p)
	if result.Metric != "disk.io" {
		t.Errorf("Metric = %q, want %q", result.Metric, "disk.io")
	}
	if result.Value != 42.0 {
		t.Errorf("Value = %f, want 42.0", result.Value)
	}
	if result.TimestampNs != 5555555555 {
		t.Errorf("TimestampNs = %d, want 5555555555", result.TimestampNs)
	}
	if len(result.Tags) != 1 {
		t.Fatalf("Tags length = %d, want 1", len(result.Tags))
	}
	if result.Tags[0].Key != "device" || result.Tags[0].Value != "sda1" {
		t.Errorf("Tags[0] = %v, want {device, sda1}", result.Tags[0])
	}
}

func TestProtoToDataPoint_NoTags(t *testing.T) {
	p := &pb.TSDataPoint{
		Metric:      "net.rx",
		Value:       100.0,
		TimestampNs: 1111111111,
	}
	result := protoToDataPoint(p)
	if result.Tags != nil {
		t.Errorf("Tags = %v, want nil", result.Tags)
	}
}

// ---------------------------------------------------------------------------
// tagsToProto tests
// ---------------------------------------------------------------------------

func TestTagsToProto_Nil(t *testing.T) {
	result := tagsToProto(nil)
	if result != nil {
		t.Errorf("tagsToProto(nil) = %v, want nil", result)
	}
}

func TestTagsToProto_Empty(t *testing.T) {
	result := tagsToProto([]transport.TSTag{})
	if result != nil {
		t.Errorf("tagsToProto([]) = %v, want nil", result)
	}
}

func TestTagsToProto_Valid(t *testing.T) {
	tags := []transport.TSTag{
		{Key: "env", Value: "prod"},
		{Key: "app", Value: "qrdb"},
	}
	result := tagsToProto(tags)
	if len(result) != 2 {
		t.Fatalf("tagsToProto() returned %d elements, want 2", len(result))
	}
	if result[0].Key != "env" || result[0].Value != "prod" {
		t.Errorf("result[0] = %v, want {env, prod}", result[0])
	}
	if result[1].Key != "app" || result[1].Value != "qrdb" {
		t.Errorf("result[1] = %v, want {app, qrdb}", result[1])
	}
}

// ---------------------------------------------------------------------------
// protoToTags tests
// ---------------------------------------------------------------------------

func TestProtoToTags_Nil(t *testing.T) {
	result := protoToTags(nil)
	if result != nil {
		t.Errorf("protoToTags(nil) = %v, want nil", result)
	}
}

func TestProtoToTags_Empty(t *testing.T) {
	result := protoToTags([]*pb.TSTag{})
	if result != nil {
		t.Errorf("protoToTags([]) = %v, want nil", result)
	}
}

func TestProtoToTags_Valid(t *testing.T) {
	tags := []*pb.TSTag{
		{Key: "dc", Value: "us-west-2"},
		{Key: "az", Value: "a"},
	}
	result := protoToTags(tags)
	if len(result) != 2 {
		t.Fatalf("protoToTags() returned %d elements, want 2", len(result))
	}
	if result[0].Key != "dc" || result[0].Value != "us-west-2" {
		t.Errorf("result[0] = %v, want {dc, us-west-2}", result[0])
	}
	if result[1].Key != "az" || result[1].Value != "a" {
		t.Errorf("result[1] = %v, want {az, a}", result[1])
	}
}

// ---------------------------------------------------------------------------
// aggFuncToProto map tests
// ---------------------------------------------------------------------------

func TestAggFuncToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.AggFunc
		expected pb.AggFunc
	}{
		{"AggAvg", transport.AggAvg, pb.AggFunc_AGG_AVG},
		{"AggSum", transport.AggSum, pb.AggFunc_AGG_SUM},
		{"AggMin", transport.AggMin, pb.AggFunc_AGG_MIN},
		{"AggMax", transport.AggMax, pb.AggFunc_AGG_MAX},
		{"AggCount", transport.AggCount, pb.AggFunc_AGG_COUNT},
		{"AggPercentile", transport.AggPercentile, pb.AggFunc_AGG_PERCENTILE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggFuncToProto[tt.input]
			if got != tt.expected {
				t.Errorf("aggFuncToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for TimeSeries methods
// ---------------------------------------------------------------------------

func TestTransport_TSWrite_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.TSWrite(ctx, &transport.TSDataPoint{Metric: "test"})
	if err == nil {
		t.Fatal("TSWrite() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSWriteBatch_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.TSWriteBatch(ctx, []transport.TSDataPoint{{Metric: "test"}})
	if err == nil {
		t.Fatal("TSWriteBatch() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSQuery_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSQuery(ctx, &transport.TSQueryRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSQuery() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSAggregate_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSAggregate(ctx, &transport.TSAggregateRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSAggregate() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for JSON-based TS methods
// ---------------------------------------------------------------------------

func TestTransport_TSWritePoint_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.TSWritePoint(ctx, &transport.TSDataPoint{Metric: "test"})
	if err == nil {
		t.Fatal("TSWritePoint() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSRichQuery_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSRichQuery(ctx, &transport.TSRichQueryRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSRichQuery() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSDeleteSeries_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSDeleteSeries(ctx, &transport.TSDeleteRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSDeleteSeries() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSGetRetention_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSGetRetention() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSSetRetention_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	err = tr.TSSetRetention(ctx, &transport.TSSetRetentionRequest{Metric: "test"})
	if err == nil {
		t.Fatal("TSSetRetention() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_TSListMetrics_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{Prefix: "cpu"})
	if err == nil {
		t.Fatal("TSListMetrics() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// tagsToMap / mapToTags helper tests
// ---------------------------------------------------------------------------

func TestTagsToMap_Nil(t *testing.T) {
	result := tagsToMap(nil)
	if result != nil {
		t.Errorf("tagsToMap(nil) = %v, want nil", result)
	}
}

func TestTagsToMap_Empty(t *testing.T) {
	result := tagsToMap([]transport.TSTag{})
	if result != nil {
		t.Errorf("tagsToMap([]) = %v, want nil", result)
	}
}

func TestTagsToMap_Valid(t *testing.T) {
	tags := []transport.TSTag{
		{Key: "host", Value: "server-1"},
		{Key: "region", Value: "us-east"},
	}
	result := tagsToMap(tags)
	if len(result) != 2 {
		t.Fatalf("tagsToMap() returned %d elements, want 2", len(result))
	}
	if result["host"] != "server-1" {
		t.Errorf("result[host] = %q, want %q", result["host"], "server-1")
	}
	if result["region"] != "us-east" {
		t.Errorf("result[region] = %q, want %q", result["region"], "us-east")
	}
}

func TestMapToTags_Nil(t *testing.T) {
	result := mapToTags(nil)
	if result != nil {
		t.Errorf("mapToTags(nil) = %v, want nil", result)
	}
}

func TestMapToTags_Empty(t *testing.T) {
	result := mapToTags(map[string]string{})
	if result != nil {
		t.Errorf("mapToTags({}) = %v, want nil", result)
	}
}

func TestMapToTags_Valid(t *testing.T) {
	m := map[string]string{"dc": "us-west-2"}
	result := mapToTags(m)
	if len(result) != 1 {
		t.Fatalf("mapToTags() returned %d elements, want 1", len(result))
	}
	if result[0].Key != "dc" || result[0].Value != "us-west-2" {
		t.Errorf("result[0] = %v, want {dc, us-west-2}", result[0])
	}
}
