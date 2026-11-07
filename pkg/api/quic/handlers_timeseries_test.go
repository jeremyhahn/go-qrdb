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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// handleTSWrite
// ---------------------------------------------------------------------------

func TestHandleTSWrite_Success(t *testing.T) {
	var capturedMetric string
	var capturedValue float64
	var capturedTags map[string]string

	tsSvc := &mockTimeSeriesService{
		writeFn: func(_ context.Context, metric string, value float64, _ time.Time, tags map[string]string) error {
			capturedMetric = metric
			capturedValue = value
			capturedTags = tags
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	now := time.Now()
	payload, err := proto.Marshal(&pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu.usage",
			Value:       99.5,
			TimestampNs: now.UnixNano(),
			Tags: []*pb.TSTag{
				{Key: "host", Value: "node-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal TSWriteRequest: %v", err)
	}

	resp, err := srv.handleTSWrite(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSWrite returned unexpected error: %v", err)
	}

	if capturedMetric != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", capturedMetric)
	}
	if capturedValue != 99.5 {
		t.Errorf("expected value 99.5, got %f", capturedValue)
	}
	if capturedTags["host"] != "node-1" {
		t.Errorf("expected tag host=node-1, got %q", capturedTags["host"])
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSWrite_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "ts write failed"}
	tsSvc := &mockTimeSeriesService{
		writeFn: func(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "m",
			Value:       1.0,
			TimestampNs: time.Now().UnixNano(),
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal TSWriteRequest: %v", err)
	}

	_, err = srv.handleTSWrite(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSWrite, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSWrite_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSWrite(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSWrite_NilPoint(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSWriteRequest{})
	if err != nil {
		t.Fatalf("failed to marshal TSWriteRequest: %v", err)
	}

	_, err = srv.handleTSWrite(ctx, payload)
	if err == nil {
		t.Fatal("expected error for nil point, got nil")
	}

	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestHandleTSWrite_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSWrite(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSWriteBatch
// ---------------------------------------------------------------------------

func TestHandleTSWriteBatch_Success(t *testing.T) {
	var capturedCount int
	tsSvc := &mockTimeSeriesService{
		writeBatchFn: func(_ context.Context, points []timeseries.DataPoint) error {
			capturedCount = len(points)
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	now := time.Now()
	payload, err := proto.Marshal(&pb.TSWriteBatchRequest{
		Points: []*pb.TSDataPoint{
			{Metric: "cpu", Value: 50.0, TimestampNs: now.UnixNano()},
			{Metric: "mem", Value: 70.0, TimestampNs: now.UnixNano()},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal TSWriteBatchRequest: %v", err)
	}

	resp, err := srv.handleTSWriteBatch(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSWriteBatch returned unexpected error: %v", err)
	}

	if capturedCount != 2 {
		t.Errorf("expected 2 points, got %d", capturedCount)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSWriteBatch_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "batch write failed"}
	tsSvc := &mockTimeSeriesService{
		writeBatchFn: func(_ context.Context, _ []timeseries.DataPoint) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSWriteBatchRequest{
		Points: []*pb.TSDataPoint{
			{Metric: "m", Value: 1.0, TimestampNs: time.Now().UnixNano()},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal TSWriteBatchRequest: %v", err)
	}

	_, err = srv.handleTSWriteBatch(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSWriteBatch, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSWriteBatch_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSWriteBatch(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSQuery
// ---------------------------------------------------------------------------

func TestHandleTSQuery_Success(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, metric string, s, e time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return []timeseries.DataPoint{
				{Metric: metric, Value: 42.0, Timestamp: s},
				{Metric: metric, Value: 84.0, Timestamp: e},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: start.UnixNano(),
		EndNs:   end.UnixNano(),
	})
	if err != nil {
		t.Fatalf("failed to marshal TSQueryRequest: %v", err)
	}

	resp, err := srv.handleTSQuery(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSQuery returned unexpected error: %v", err)
	}

	var queryResp pb.TSQueryResponse
	if err := proto.Unmarshal(resp, &queryResp); err != nil {
		t.Fatalf("failed to unmarshal TSQueryResponse: %v", err)
	}
	if len(queryResp.GetPoints()) != 2 {
		t.Fatalf("expected 2 points, got %d", len(queryResp.GetPoints()))
	}
	if queryResp.GetPoints()[0].GetMetric() != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", queryResp.GetPoints()[0].GetMetric())
	}
	if queryResp.GetPoints()[0].GetValue() != 42.0 {
		t.Errorf("expected value 42.0, got %f", queryResp.GetPoints()[0].GetValue())
	}
}

func TestHandleTSQuery_WithOptions(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			resolved := timeseries.ApplyTSQueryOptions(opts)
			if resolved.Limit != 5 {
				t.Errorf("expected limit 5, got %d", resolved.Limit)
			}
			if !resolved.Descending {
				t.Error("expected descending to be true")
			}
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:     "m",
		StartNs:    1,
		EndNs:      2,
		Limit:      5,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("failed to marshal TSQueryRequest: %v", err)
	}

	_, err = srv.handleTSQuery(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSQuery returned unexpected error: %v", err)
	}
}

func TestHandleTSQuery_WithTags(t *testing.T) {
	var gotTags map[string]string
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			gotTags = timeseries.ApplyTSQueryOptions(opts).Tags
			return []timeseries.DataPoint{}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: 1000,
		EndNs:   2000,
		Tags:    []*pb.TSTag{{Key: "host", Value: "web-01"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleTSQuery(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotTags) != 1 || gotTags["host"] != "web-01" {
		t.Errorf("tags forwarded = %v, want map[host:web-01]", gotTags)
	}
}

func TestHandleTSQuery_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(&mockTimeSeriesService{}))

	_, err := srv.handleTSQuery(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleTSQuery_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "ts query failed"}
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:  "m",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("failed to marshal TSQueryRequest: %v", err)
	}

	_, err = srv.handleTSQuery(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSQuery, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSQuery_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSQuery(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSAggregate
// ---------------------------------------------------------------------------

func TestHandleTSAggregate_Success(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	interval := time.Hour

	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, metric string, s, e time.Time, fn timeseries.AggFunc, intv time.Duration) ([]timeseries.AggResult, error) {
			return []timeseries.AggResult{
				{Timestamp: s, Value: 50.0, Count: 100},
				{Timestamp: s.Add(intv), Value: 60.0, Count: 120},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSAggregateRequest{
		Metric:     "cpu.usage",
		StartNs:    start.UnixNano(),
		EndNs:      end.UnixNano(),
		Function:   pb.AggFunc_AGG_AVG,
		IntervalNs: int64(interval),
	})
	if err != nil {
		t.Fatalf("failed to marshal TSAggregateRequest: %v", err)
	}

	resp, err := srv.handleTSAggregate(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSAggregate returned unexpected error: %v", err)
	}

	var aggResp pb.TSAggregateResponse
	if err := proto.Unmarshal(resp, &aggResp); err != nil {
		t.Fatalf("failed to unmarshal TSAggregateResponse: %v", err)
	}
	if len(aggResp.GetResults()) != 2 {
		t.Fatalf("expected 2 results, got %d", len(aggResp.GetResults()))
	}
	if aggResp.GetResults()[0].GetValue() != 50.0 {
		t.Errorf("expected value 50.0, got %f", aggResp.GetResults()[0].GetValue())
	}
	if aggResp.GetResults()[0].GetCount() != 100 {
		t.Errorf("expected count 100, got %d", aggResp.GetResults()[0].GetCount())
	}
}

func TestHandleTSAggregate_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "aggregate failed"}
	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.TSAggregateRequest{
		Metric:     "m",
		StartNs:    1,
		EndNs:      2,
		Function:   pb.AggFunc_AGG_SUM,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("failed to marshal TSAggregateRequest: %v", err)
	}

	_, err = srv.handleTSAggregate(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSAggregate, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSAggregate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSAggregate(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleTSAggregate_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSAggregate(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSWritePoint
// ---------------------------------------------------------------------------

func TestHandleTSWritePoint_Success(t *testing.T) {
	var capturedPoint *timeseries.DataPoint

	tsSvc := &mockTimeSeriesService{
		writePointFn: func(_ context.Context, point *timeseries.DataPoint) error {
			capturedPoint = point
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	now := time.Now()
	payload, err := json.Marshal(tsWritePointWireRequest{
		Metric:      "cpu.usage",
		Value:       99.5,
		TimestampNs: now.UnixNano(),
		Tags:        map[string]string{"host": "node-1"},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSWritePoint(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSWritePoint returned unexpected error: %v", err)
	}

	if capturedPoint == nil {
		t.Fatal("expected service to be called with a data point, got nil")
	}
	if capturedPoint.Metric != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", capturedPoint.Metric)
	}
	if capturedPoint.Value != 99.5 {
		t.Errorf("expected value 99.5, got %f", capturedPoint.Value)
	}
	if capturedPoint.Tags["host"] != "node-1" {
		t.Errorf("expected tag host=node-1, got %q", capturedPoint.Tags["host"])
	}
	if capturedPoint.Timestamp.UnixNano() != now.UnixNano() {
		t.Errorf("expected timestamp %d, got %d", now.UnixNano(), capturedPoint.Timestamp.UnixNano())
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSWritePoint_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "write point failed"}
	tsSvc := &mockTimeSeriesService{
		writePointFn: func(_ context.Context, _ *timeseries.DataPoint) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsWritePointWireRequest{
		Metric:      "m",
		Value:       1.0,
		TimestampNs: time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSWritePoint(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSWritePoint, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSWritePoint_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSWritePoint(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSWritePoint_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSWritePoint(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSRichQuery
// ---------------------------------------------------------------------------

func TestHandleTSRichQuery_Success(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	aggVal := 75.0
	grpAggVal := 80.0
	grpAggVal2 := 65.0

	tsSvc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return &timeseries.QueryResult{
				Metric: req.Metric,
				Tags:   req.Tags,
				Points: []*timeseries.DataPoint{
					{Metric: req.Metric, Value: 42.0, Timestamp: start, Tags: map[string]string{"host": "a"}},
					{Metric: req.Metric, Value: 84.0, Timestamp: end, Tags: map[string]string{"host": "b"}},
				},
				AggregatedValue: &aggVal,
				GroupedResults: []*timeseries.GroupedResult{
					{
						Tags: map[string]string{"region": "us-east"},
						Points: []*timeseries.DataPoint{
							{Metric: req.Metric, Value: 50.0, Timestamp: start},
						},
						AggregatedValue: &grpAggVal,
					},
					{
						Tags: map[string]string{"region": "eu-west"},
						Points: []*timeseries.DataPoint{
							{Metric: req.Metric, Value: 60.0, Timestamp: start},
							{Metric: req.Metric, Value: 70.0, Timestamp: end},
						},
						AggregatedValue: &grpAggVal2,
					},
				},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsRichQueryWireRequest{
		Metric:      "cpu.usage",
		StartNs:     start.UnixNano(),
		EndNs:       end.UnixNano(),
		Tags:        map[string]string{"env": "prod"},
		Aggregation: int(timeseries.AggAvg),
		IntervalNs:  int64(time.Hour),
		GroupBy:     []string{"region"},
		Limit:       100,
		Percentile:  99.9,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSRichQuery(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSRichQuery returned unexpected error: %v", err)
	}

	var wireResp tsRichQueryWireResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if wireResp.Metric != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", wireResp.Metric)
	}
	if len(wireResp.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(wireResp.Points))
	}
	if wireResp.Points[0].Value != 42.0 {
		t.Errorf("expected first point value 42.0, got %f", wireResp.Points[0].Value)
	}
	if wireResp.Points[1].Value != 84.0 {
		t.Errorf("expected second point value 84.0, got %f", wireResp.Points[1].Value)
	}
	if wireResp.AggregatedValue == nil || *wireResp.AggregatedValue != 75.0 {
		t.Errorf("expected aggregated value 75.0, got %v", wireResp.AggregatedValue)
	}
	if len(wireResp.GroupedResults) != 2 {
		t.Fatalf("expected 2 grouped results, got %d", len(wireResp.GroupedResults))
	}
	wantGroups := []struct {
		region string
		values []float64
		agg    float64
	}{
		{region: "us-east", values: []float64{50.0}, agg: 80.0},
		{region: "eu-west", values: []float64{60.0, 70.0}, agg: 65.0},
	}
	for i, want := range wantGroups {
		got := wireResp.GroupedResults[i]
		if got.Tags["region"] != want.region {
			t.Errorf("group %d: expected tag region=%s, got %q", i, want.region, got.Tags["region"])
		}
		if len(got.Points) != len(want.values) {
			t.Fatalf("group %d: expected %d points, got %d", i, len(want.values), len(got.Points))
		}
		for j, v := range want.values {
			if got.Points[j].Value != v {
				t.Errorf("group %d point %d: expected value %f, got %f", i, j, v, got.Points[j].Value)
			}
		}
		if got.AggregatedValue == nil || *got.AggregatedValue != want.agg {
			t.Errorf("group %d: expected aggregated value %f, got %v", i, want.agg, got.AggregatedValue)
		}
	}
}

func TestHandleTSRichQuery_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "rich query failed"}
	tsSvc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsRichQueryWireRequest{
		Metric:  "m",
		StartNs: 1,
		EndNs:   2,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSRichQuery(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSRichQuery, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSRichQuery_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSRichQuery(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSRichQuery_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSRichQuery(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSDeleteSeries
// ---------------------------------------------------------------------------

func TestHandleTSDeleteSeries_Success(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	var capturedReq *timeseries.DeleteRequest

	tsSvc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			capturedReq = req
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsDeleteSeriesWireRequest{
		Metric:  "cpu.usage",
		Tags:    map[string]string{"host": "node-1"},
		StartNs: start.UnixNano(),
		EndNs:   end.UnixNano(),
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSDeleteSeries(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSDeleteSeries returned unexpected error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected service to be called, got nil request")
	}
	if capturedReq.Metric != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", capturedReq.Metric)
	}
	if capturedReq.Tags["host"] != "node-1" {
		t.Errorf("expected tag host=node-1, got %q", capturedReq.Tags["host"])
	}
	if capturedReq.StartTime.UnixNano() != start.UnixNano() {
		t.Errorf("expected start time %d, got %d", start.UnixNano(), capturedReq.StartTime.UnixNano())
	}
	if capturedReq.EndTime.UnixNano() != end.UnixNano() {
		t.Errorf("expected end time %d, got %d", end.UnixNano(), capturedReq.EndTime.UnixNano())
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSDeleteSeries_ZeroTimestamps(t *testing.T) {
	var capturedReq *timeseries.DeleteRequest

	tsSvc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			capturedReq = req
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsDeleteSeriesWireRequest{
		Metric: "mem.free",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSDeleteSeries(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSDeleteSeries returned unexpected error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected service to be called, got nil request")
	}
	if capturedReq.Metric != "mem.free" {
		t.Errorf("expected metric %q, got %q", "mem.free", capturedReq.Metric)
	}
	if !capturedReq.StartTime.IsZero() {
		t.Errorf("expected zero start time, got %v", capturedReq.StartTime)
	}
	if !capturedReq.EndTime.IsZero() {
		t.Errorf("expected zero end time, got %v", capturedReq.EndTime)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSDeleteSeries_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete series failed"}
	tsSvc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, _ *timeseries.DeleteRequest) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsDeleteSeriesWireRequest{
		Metric: "m",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSDeleteSeries(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSDeleteSeries, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSDeleteSeries_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSDeleteSeries(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSDeleteSeries_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSDeleteSeries(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSGetRetention
// ---------------------------------------------------------------------------

func TestHandleTSGetRetention_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		getRetentionFn: func(metric string) (*timeseries.RetentionPolicy, error) {
			if metric != "cpu.usage" {
				t.Errorf("expected metric %q, got %q", "cpu.usage", metric)
			}
			return &timeseries.RetentionPolicy{
				Duration:    24 * time.Hour,
				Resolution:  time.Minute,
				Aggregation: timeseries.AggAvg,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsGetRetentionWireRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSGetRetention(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSGetRetention returned unexpected error: %v", err)
	}

	var wireResp tsRetentionPolicyWireResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if wireResp.DurationNs != int64(24*time.Hour) {
		t.Errorf("expected duration_ns %d, got %d", int64(24*time.Hour), wireResp.DurationNs)
	}
	if wireResp.ResolutionNs != int64(time.Minute) {
		t.Errorf("expected resolution_ns %d, got %d", int64(time.Minute), wireResp.ResolutionNs)
	}
	if wireResp.Aggregation != int(timeseries.AggAvg) {
		t.Errorf("expected aggregation %d, got %d", int(timeseries.AggAvg), wireResp.Aggregation)
	}
}

func TestHandleTSGetRetention_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get retention failed"}
	tsSvc := &mockTimeSeriesService{
		getRetentionFn: func(_ string) (*timeseries.RetentionPolicy, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsGetRetentionWireRequest{
		Metric: "m",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSGetRetention(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSGetRetention, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSGetRetention_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSGetRetention(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSGetRetention_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSGetRetention(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSSetRetention
// ---------------------------------------------------------------------------

func TestHandleTSSetRetention_Success(t *testing.T) {
	var capturedMetric string
	var capturedPolicy *timeseries.RetentionPolicy

	tsSvc := &mockTimeSeriesService{
		setRetentionFn: func(metric string, policy *timeseries.RetentionPolicy) error {
			capturedMetric = metric
			capturedPolicy = policy
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsSetRetentionWireRequest{
		Metric:       "cpu.usage",
		DurationNs:   int64(7 * 24 * time.Hour),
		ResolutionNs: int64(5 * time.Minute),
		Aggregation:  int(timeseries.AggSum),
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSSetRetention(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSSetRetention returned unexpected error: %v", err)
	}

	if capturedMetric != "cpu.usage" {
		t.Errorf("expected metric %q, got %q", "cpu.usage", capturedMetric)
	}
	if capturedPolicy == nil {
		t.Fatal("expected service to be called with a policy, got nil")
	}
	if capturedPolicy.Duration != 7*24*time.Hour {
		t.Errorf("expected duration %v, got %v", 7*24*time.Hour, capturedPolicy.Duration)
	}
	if capturedPolicy.Resolution != 5*time.Minute {
		t.Errorf("expected resolution %v, got %v", 5*time.Minute, capturedPolicy.Resolution)
	}
	if capturedPolicy.Aggregation != timeseries.AggSum {
		t.Errorf("expected aggregation %d, got %d", timeseries.AggSum, capturedPolicy.Aggregation)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleTSSetRetention_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "set retention failed"}
	tsSvc := &mockTimeSeriesService{
		setRetentionFn: func(_ string, _ *timeseries.RetentionPolicy) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsSetRetentionWireRequest{
		Metric:     "m",
		DurationNs: int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSSetRetention(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSSetRetention, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSSetRetention_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSSetRetention(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSSetRetention_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSSetRetention(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTSListMetrics
// ---------------------------------------------------------------------------

func TestHandleTSListMetrics_Success(t *testing.T) {
	var capturedPrefix string
	var capturedLimit int

	tsSvc := &mockTimeSeriesService{
		listMetricsFn: func(prefix string, limit int) ([]string, error) {
			capturedPrefix = prefix
			capturedLimit = limit
			return []string{"cpu.usage", "cpu.idle", "mem.free"}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsListMetricsWireRequest{
		Prefix: "cpu.",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSListMetrics(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSListMetrics returned unexpected error: %v", err)
	}

	if capturedPrefix != "cpu." {
		t.Errorf("expected prefix %q, got %q", "cpu.", capturedPrefix)
	}
	if capturedLimit != 50 {
		t.Errorf("expected limit 50, got %d", capturedLimit)
	}

	var wireResp tsListMetricsWireResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(wireResp.Metrics) != 3 {
		t.Fatalf("expected 3 metrics, got %d", len(wireResp.Metrics))
	}
	if wireResp.Metrics[0] != "cpu.usage" {
		t.Errorf("expected first metric %q, got %q", "cpu.usage", wireResp.Metrics[0])
	}
	if wireResp.Metrics[1] != "cpu.idle" {
		t.Errorf("expected second metric %q, got %q", "cpu.idle", wireResp.Metrics[1])
	}
	if wireResp.Metrics[2] != "mem.free" {
		t.Errorf("expected third metric %q, got %q", "mem.free", wireResp.Metrics[2])
	}
}

func TestHandleTSListMetrics_NilMetrics(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsListMetricsWireRequest{})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := srv.handleTSListMetrics(ctx, payload)
	if err != nil {
		t.Fatalf("handleTSListMetrics returned unexpected error: %v", err)
	}

	var wireResp tsListMetricsWireResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if wireResp.Metrics == nil {
		t.Fatal("expected non-nil metrics slice, got nil")
	}
	if len(wireResp.Metrics) != 0 {
		t.Errorf("expected 0 metrics, got %d", len(wireResp.Metrics))
	}
}

func TestHandleTSListMetrics_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list metrics failed"}
	tsSvc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	payload, err := json.Marshal(tsListMetricsWireRequest{
		Prefix: "cpu.",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	_, err = srv.handleTSListMetrics(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleTSListMetrics, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleTSListMetrics_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleTSListMetrics(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil timeseries service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "timeseries" {
		t.Errorf("expected service %q, got %q", "timeseries", snc.Service)
	}
}

func TestHandleTSListMetrics_UnmarshalError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTimeSeries(tsSvc))
	ctx := context.Background()

	_, err := srv.handleTSListMetrics(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ume *UnmarshalError
	if !errors.As(err, &ume) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
