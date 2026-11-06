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
