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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedTS_Write(t *testing.T) {
	var gotMetric string
	var gotValue float64
	var gotTS time.Time
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		writeFn: func(_ context.Context, metric string, value float64, ts time.Time, _ map[string]string) error {
			gotMetric, gotValue, gotTS = metric, value, ts
			return nil
		},
	}))
	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.5, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
	if gotMetric != "m" || gotValue != 1.5 || gotTS.UnixNano() != 1700000000000000000 {
		t.Fatalf("service got (%q, %v, %d), want (m, 1.5, 1700000000000000000)", gotMetric, gotValue, gotTS.UnixNano())
	}
}

func TestEmbeddedTS_WriteBatch(t *testing.T) {
	var got []timeseries.DataPoint
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		writeBatchFn: func(_ context.Context, points []timeseries.DataPoint) error {
			got = points
			return nil
		},
	}))
	if err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "a", Value: 1.0, TimestampNs: 1700000000000000000},
		{Metric: "b", Value: 2.0, TimestampNs: 1700000000000000001},
	}); err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
	if len(got) != 2 || got[0].Metric != "a" || got[1].Metric != "b" || got[1].Value != 2.0 {
		t.Fatalf("service got %+v, want points a and b in order", got)
	}
}

func TestEmbeddedTS_Query(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Tags:       []transport.TSTag{{Key: "host", Value: "a"}},
		Limit:      10,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 1 {
		t.Fatalf("got %d points", len(resp.Points))
	}
}

func TestEmbeddedTS_Query_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, errSentinel
		},
	}))
	if _, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{}); !errors.Is(err, errSentinel) {
		t.Fatalf("err = %v, want the service's error", err)
	}
}

func TestEmbeddedTS_Aggregate(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Function:   transport.AggAvg,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results", len(resp.Results))
	}
}

func TestEmbeddedTS_WritePoint(t *testing.T) {
	var got *timeseries.DataPoint
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		writePointFn: func(_ context.Context, point *timeseries.DataPoint) error {
			got = point
			return nil
		},
	}))
	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.5, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
	if got == nil || got.Metric != "m" || got.Value != 1.5 || got.Timestamp.UnixNano() != 1700000000000000000 {
		t.Fatalf("service got %+v, want metric m value 1.5 at 1700000000000000000", got)
	}
}

func TestEmbeddedTS_RichQuery(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "m",
		StartNs:     1700000000000000000,
		EndNs:       1700001000000000000,
		Aggregation: transport.AggAvg,
		IntervalNs:  int64(time.Minute),
		GroupBy:     []string{"host"},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "m" {
		t.Fatalf("Metric = %q", resp.Metric)
	}
}

func TestEmbeddedTS_DeleteSeries(t *testing.T) {
	var got *timeseries.DeleteRequest
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		deleteFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			got = req
			return nil
		},
	}))
	resp, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
	if got == nil || got.Metric != "m" {
		t.Fatalf("service got %+v, want metric m", got)
	}
}

func TestEmbeddedTS_GetRetention(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp.Policy == nil || resp.Policy.DurationNs != int64(24*time.Hour) {
		t.Fatalf("Policy = %+v, want the service's 24h duration", resp.Policy)
	}
}

func TestEmbeddedTS_SetRetention(t *testing.T) {
	var gotMetric string
	var gotPolicy *timeseries.RetentionPolicy
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		setRetFn: func(metric string, policy *timeseries.RetentionPolicy) error {
			gotMetric, gotPolicy = metric, policy
			return nil
		},
	}))
	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "m",
		Policy: &transport.TSRetentionPolicy{DurationNs: int64(24 * time.Hour)},
	}); err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
	if gotMetric != "m" || gotPolicy == nil || gotPolicy.Duration != 24*time.Hour {
		t.Fatalf("service got (%q, %+v), want (m, 24h)", gotMetric, gotPolicy)
	}
}

func TestEmbeddedTS_ListMetrics(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{Prefix: "cpu", Limit: 10})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("got %d metrics", len(resp.Metrics))
	}
}

// tsCalls drives every time-series verb of tr once.
func tsCalls(tr *Transport) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"TSWrite":      func() error { return tr.TSWrite(ctx, &transport.TSDataPoint{}) },
		"TSWriteBatch": func() error { return tr.TSWriteBatch(ctx, nil) },
		"TSQuery":      func() error { _, err := tr.TSQuery(ctx, &transport.TSQueryRequest{}); return err },
		"TSAggregate":  func() error { _, err := tr.TSAggregate(ctx, &transport.TSAggregateRequest{}); return err },
		"TSWritePoint": func() error { return tr.TSWritePoint(ctx, &transport.TSDataPoint{}) },
		"TSRichQuery":  func() error { _, err := tr.TSRichQuery(ctx, &transport.TSRichQueryRequest{}); return err },
		"TSDeleteSeries": func() error {
			_, err := tr.TSDeleteSeries(ctx, &transport.TSDeleteRequest{})
			return err
		},
		"TSGetRetention": func() error {
			_, err := tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{})
			return err
		},
		"TSSetRetention": func() error { return tr.TSSetRetention(ctx, &transport.TSSetRetentionRequest{}) },
		"TSListMetrics": func() error {
			_, err := tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
			return err
		},
	}
}

// Every verb answers a transport built without a time-series service with
// the typed wiring-defect error.
func TestEmbeddedTS_NilServiceIsTyped(t *testing.T) {
	for name, call := range tsCalls(newExtTransport(t)) {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: err = %T (%v), want *NilServiceError", name, err, err)
		}
	}
}

// Every verb on a closed transport is refused with *transport.ClosedError
// before it reaches the service.
func TestEmbeddedTS_ClosedIsRefused(t *testing.T) {
	for name, call := range tsCalls(closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))) {
		var ce *transport.ClosedError
		if err := call(); !errors.As(err, &ce) {
			t.Errorf("%s: err = %T (%v), want *transport.ClosedError", name, err, err)
		}
	}
}
