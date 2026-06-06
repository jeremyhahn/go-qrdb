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
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_TSWrite_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.Write", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric: "m", Value: 1.5, TimestampNs: 100,
		Tags: []transport.TSTag{{Key: "k", Value: "v"}},
	}); err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
}

func TestE2E_TSWriteBatch_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.WriteBatch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "m", Value: 1.0, TimestampNs: 100},
		{Metric: "m", Value: 2.0, TimestampNs: 200},
	}); err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestE2E_TSQuery_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TSQueryResponse{
			Points: []*pb.TSDataPoint{
				{Metric: "m", Value: 1.0, TimestampNs: 100},
			},
		})
	})
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric: "m", StartNs: 0, EndNs: 1000, Limit: 10,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 1 {
		t.Fatalf("Points = %v", resp.Points)
	}
}

func TestE2E_TSAggregate_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.Aggregate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TSAggregateResponse{
			Results: []*pb.TSAggResult{
				{TimestampNs: 100, Value: 1.5, Count: 1},
			},
		})
	})
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric: "m", StartNs: 0, EndNs: 1000, Function: transport.AggAvg, IntervalNs: 100,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v", resp.Results)
	}
}

func TestE2E_TSWritePoint_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.WritePoint", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "m", Value: 1.5, TimestampNs: 100,
	}); err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestE2E_TSRichQuery_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.RichQuery", func(_ []byte) (uint16, []byte) {
		// JSON-encoded response
		resp := tsRichQueryResponse{
			Metric: "m",
			Points: []tsDataPointWire{
				{Metric: "m", Value: 1.0, TimestampNs: 100},
			},
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric: "m", StartNs: 0, EndNs: 1000,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "m" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_TSDeleteSeries_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.DeleteSeries", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	resp, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
	if resp == nil {
		t.Fatalf("resp is nil")
	}
}

func TestE2E_TSGetRetention_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.GetRetention", func(_ []byte) (uint16, []byte) {
		resp := tsGetRetentionResponse{
			Policy: &tsRetentionPolicyWire{DurationNs: 100, ResolutionNs: 10, Aggregation: 1},
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp.Policy == nil || resp.Policy.DurationNs != 100 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_TSSetRetention_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.SetRetention", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "m", Policy: &transport.TSRetentionPolicy{DurationNs: 100},
	}); err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestE2E_TSListMetrics_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.ListMetrics", func(_ []byte) (uint16, []byte) {
		resp := tsListMetricsResponse{Metrics: []string{"m1", "m2"}}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{Prefix: "m"})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("Metrics = %v", resp.Metrics)
	}
}
