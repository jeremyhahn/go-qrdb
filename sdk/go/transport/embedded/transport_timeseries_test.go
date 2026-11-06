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
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordingTSService builds a mockTSService that writes what each call
// receives into *sent, as "<metric> <detail>", and answers with one point at
// value 2.5 and timestamp 7ns.
func recordingTSService(sent *string) *mockTSService {
	at := time.Unix(0, 7)
	point := timeseries.DataPoint{Metric: "m", Value: 2.5, Timestamp: at}
	return &mockTSService{
		writeFn: func(_ context.Context, metric string, value float64, ts time.Time, _ map[string]string) error {
			*sent = fmt.Sprintf("%s %v@%d", metric, value, ts.UnixNano())
			return nil
		},
		writeBatchFn: func(_ context.Context, points []timeseries.DataPoint) error {
			*sent = fmt.Sprintf("%s %v@%d n=%d", points[0].Metric, points[0].Value, points[0].Timestamp.UnixNano(), len(points))
			return nil
		},
		writePointFn: func(_ context.Context, p *timeseries.DataPoint) error {
			*sent = fmt.Sprintf("%s %v@%d", p.Metric, p.Value, p.Timestamp.UnixNano())
			return nil
		},
		queryFn: func(_ context.Context, metric string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			*sent = metric
			return []timeseries.DataPoint{point}, nil
		},
		aggregateFn: func(_ context.Context, metric string, _, _ time.Time, fn timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			*sent = fmt.Sprintf("%s fn=%d", metric, fn)
			return []timeseries.AggResult{{Timestamp: at, Value: 2.5, Count: 3}}, nil
		},
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			*sent = req.Metric
			return &timeseries.QueryResult{Metric: "m", Points: []*timeseries.DataPoint{&point}}, nil
		},
		deleteFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			*sent = fmt.Sprintf("%s %d..%d", req.Metric, req.StartTime.UnixNano(), req.EndTime.UnixNano())
			return nil
		},
		getRetFn: func(metric string) (*timeseries.RetentionPolicy, error) {
			*sent = metric
			return &timeseries.RetentionPolicy{Duration: time.Hour}, nil
		},
		setRetFn: func(metric string, policy *timeseries.RetentionPolicy) error {
			*sent = fmt.Sprintf("%s %s", metric, policy.Duration)
			return nil
		},
		listMetricsFn: func(prefix string, limit int) ([]string, error) {
			*sent = fmt.Sprintf("%s %d", prefix, limit)
			return []string{"m"}, nil
		},
	}
}

// TestEmbeddedTS_Request_SendsTheDocumentedRequest checks that each
// time-series operation sent through Request reaches the service with the
// request's values and decodes the service's answer.
func TestEmbeddedTS_Request_SendsTheDocumentedRequest(t *testing.T) {
	point := transport.TSDataPoint{Metric: "m", Value: 2.5, TimestampNs: 7}
	cases := []struct {
		name     string
		req      any
		out      any // pointer the answer decodes into; nil for a write
		wantSent string
		want     any
	}{
		{name: "TSWrite", req: &point, wantSent: "m 2.5@7"},
		{
			name: "TSWriteBatch", req: &tsWriteBatchRequest{Points: []transport.TSDataPoint{point}},
			wantSent: "m 2.5@7 n=1",
		},
		{name: "TSWritePoint", req: &point, wantSent: "m 2.5@7"},
		{
			name: "TSQuery", req: &transport.TSQueryRequest{Metric: "m"},
			out: new(*transport.TSQueryResponse), wantSent: "m",
			want: &transport.TSQueryResponse{Points: []transport.TSDataPoint{point}},
		},
		{
			name: "TSAggregate", req: &transport.TSAggregateRequest{Metric: "m", Function: transport.AggMax},
			out: new(*transport.TSAggregateResponse), wantSent: "m fn=4",
			want: &transport.TSAggregateResponse{Results: []transport.TSAggResult{{TimestampNs: 7, Value: 2.5, Count: 3}}},
		},
		{
			name: "TSRichQuery", req: &transport.TSRichQueryRequest{Metric: "m"},
			out: new(*transport.TSRichQueryResponse), wantSent: "m",
			want: &transport.TSRichQueryResponse{Metric: "m", Points: []transport.TSDataPoint{point}},
		},
		{
			name: "TSDeleteSeries", req: &transport.TSDeleteRequest{Metric: "m", StartNs: 3, EndNs: 9},
			out: new(*transport.TSDeleteResponse), wantSent: "m 3..9",
			want: &transport.TSDeleteResponse{},
		},
		{
			name: "TSGetRetention", req: &transport.TSGetRetentionRequest{Metric: "m"},
			out: new(*transport.TSGetRetentionResponse), wantSent: "m",
			want: &transport.TSGetRetentionResponse{Policy: &transport.TSRetentionPolicy{DurationNs: int64(time.Hour)}},
		},
		{
			name:     "TSSetRetention",
			req:      &transport.TSSetRetentionRequest{Metric: "m", Policy: &transport.TSRetentionPolicy{DurationNs: int64(time.Hour)}},
			wantSent: "m 1h0m0s",
		},
		{
			name: "TSListMetrics", req: &transport.TSListMetricsRequest{Prefix: "p", Limit: 5},
			out: new(*transport.TSListMetricsResponse), wantSent: "p 5",
			want: &transport.TSListMetricsResponse{Metrics: []string{"m"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent string
			tr := newExtTransport(t, WithTimeSeriesService(recordingTSService(&sent)))
			if err := tr.Request(context.Background(), tc.name, tc.req, tc.out); err != nil {
				t.Fatalf("Request(%s): %v", tc.name, err)
			}
			if sent != tc.wantSent {
				t.Errorf("service received %q, want %q", sent, tc.wantSent)
			}
			if tc.out == nil {
				return
			}
			if got := reflect.ValueOf(tc.out).Elem().Interface(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("answer %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEmbeddedTS_Query_ForwardsOptions asserts the tag filter, limit and
// order of a TSQuery reach the service as its query options.
func TestEmbeddedTS_Query_ForwardsOptions(t *testing.T) {
	var got timeseries.TSQueryOptions
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			for _, opt := range opts {
				opt(&got)
			}
			return nil, nil
		},
	}))
	if _, err := tr.TSQuery(t.Context(), &transport.TSQueryRequest{
		Metric:     "m",
		Tags:       []transport.TSTag{{Key: "host", Value: "a"}},
		Limit:      10,
		Descending: true,
	}); err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	want := timeseries.TSQueryOptions{Tags: map[string]string{"host": "a"}, Limit: 10, Descending: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service received %+v, want %+v", got, want)
	}
}

// mockTSService answers every time-series call with a fixed result; a
// non-nil Fn field replaces the answer. The WithConsistency reads delegate
// to their plain forms.
type mockTSService struct {
	writeFn       func(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error
	writeBatchFn  func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn       func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	writePointFn  func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn   func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	deleteFn      func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetFn      func(metric string) (*timeseries.RetentionPolicy, error)
	setRetFn      func(metric string, policy *timeseries.RetentionPolicy) error
	applyRetFn    func(ctx context.Context, metric string, cutoff time.Time) error
	listMetricsFn func(prefix string, limit int) ([]string, error)
}

var _ timeseries.TimeSeriesServicer = (*mockTSService)(nil)

func (m *mockTSService) Write(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, ts, tags)
	}
	return nil
}

func (m *mockTSService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *mockTSService) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *mockTSService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTSService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return []timeseries.AggResult{{Timestamp: start, Value: 100.0, Count: 1}}, nil
}

func (m *mockTSService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTSService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *mockTSService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return &timeseries.QueryResult{Metric: req.Metric, Points: []*timeseries.DataPoint{{Metric: req.Metric, Value: 1.0, Timestamp: req.StartTime}}}, nil
}

func (m *mockTSService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(ctx, req)
}

func (m *mockTSService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return nil
}

func (m *mockTSService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetFn != nil {
		return m.getRetFn(metric)
	}
	return &timeseries.RetentionPolicy{Duration: 24 * time.Hour}, nil
}

func (m *mockTSService) SetRetentionPolicy(_ context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetFn != nil {
		return m.setRetFn(metric, policy)
	}
	return nil
}

func (m *mockTSService) ApplyRetention(ctx context.Context, metric string, cutoff time.Time) error {
	if m.applyRetFn != nil {
		return m.applyRetFn(ctx, metric, cutoff)
	}
	return nil
}

func (m *mockTSService) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return []string{"cpu.usage", "mem.free"}, nil
}

func (m *mockTSService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}
