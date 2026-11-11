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

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// TSWritePoint
// ---------------------------------------------------------------------------

func TestTSWritePoint_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu",
		Value:       0.5,
		TimestampNs: time.Now().UnixNano(),
		Tags:        []transport.TSTag{{Key: "host", Value: "a"}},
	})
	if err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestTSWritePoint_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.writePointErr = errors.New("write failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "x", TimestampNs: time.Now().UnixNano(),
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery
// ---------------------------------------------------------------------------

func TestTSRichQuery_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.queryRichErr = errors.New("query failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	if _, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: now.UnixNano(),
		EndNs:   now.UnixNano(),
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSDeleteSeries
// ---------------------------------------------------------------------------

func TestTSDeleteSeries_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric:  "cpu",
		Tags:    []transport.TSTag{{Key: "host", Value: "a"}},
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
}

func TestTSDeleteSeries_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.deleteErr = errors.New("delete failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	if _, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu",
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSGetRetention
// ---------------------------------------------------------------------------

func TestTSGetRetention_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp.Policy == nil {
		t.Error("expected non-nil policy")
	}
}

func TestTSGetRetention_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.getRetentErr = errors.New("not found")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	if _, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSSetRetention
// ---------------------------------------------------------------------------

func TestTSSetRetention_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   int64(7 * 24 * time.Hour),
			ResolutionNs: int64(time.Minute),
			Aggregation:  transport.AggAvg,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestTSSetRetention_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.setRetentErr = errors.New("set failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu",
		Policy: &transport.TSRetentionPolicy{DurationNs: int64(time.Hour)},
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics
// ---------------------------------------------------------------------------

func TestTSListMetrics_Success(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 3 {
		t.Errorf("metrics: %d", len(resp.Metrics))
	}
}

func TestTSListMetrics_ServiceError(t *testing.T) {
	svc := newMockTSSvc()
	svc.listMetricErr = errors.New("list failed")
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	if _, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// TSRichQuery with all options exercised
func TestTSRichQuery_AllOptions(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Tags:        []transport.TSTag{{Key: "host", Value: "a"}},
		Aggregation: transport.AggPercentile,
		IntervalNs:  60_000_000_000,
		GroupBy:     []string{"host", "region"},
		Limit:       100,
		Percentile:  95.0,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Metric != "cpu" {
		t.Errorf("metric: %q", resp.Metric)
	}
}
