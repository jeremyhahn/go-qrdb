//go:build qrdb_ts

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

package standalone

import (
	"context"
	"errors"
	"strconv"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTimeSeriesConfigServer starts a memory-backed server with time-series
// config tc, shut down at test end.
func newTimeSeriesConfigServer(t *testing.T, tc qrdbconfig.TimeSeriesConfig) *Server {
	t.Helper()
	srv, err := NewServer(context.Background(), &ServerConfig{Backend: database.BackendMemory, TimeSeries: tc})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

const tsConfigTimestampNs = 1_700_000_000_000_000_000

// writeClientMetric writes one point to metric through srv's client.
func writeClientMetric(srv *Server, metric string) error {
	return srv.Client().TimeSeries().TSWrite(context.Background(), &transport.TSDataPoint{Metric: metric, Value: 1, TimestampNs: tsConfigTimestampNs})
}

// writeClientMetrics writes one point to each of n metrics named
// m-0..m-(n-1) through srv's client and returns the first refusal.
func writeClientMetrics(srv *Server, n int) error {
	for i := 0; i < n; i++ {
		if err := writeClientMetric(srv, "m-"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}

func TestNewServer_MaxMetricsRefusesTheMetricPastTheCap(t *testing.T) {
	srv := newTimeSeriesConfigServer(t, qrdbconfig.TimeSeriesConfig{MaxMetrics: 2})
	if err := writeClientMetrics(srv, 2); err != nil {
		t.Fatalf("metrics under the cap: %v", err)
	}
	var le *service.MetricLimitError
	if err := writeClientMetric(srv, "m-extra"); !errors.As(err, &le) {
		t.Fatalf("third metric at max_metrics 2: err = %v (%T), want *service.MetricLimitError", err, err)
	}
	if err := writeClientMetric(srv, "m-1"); err != nil {
		t.Fatalf("a point on a metric that exists, at the cap: %v", err)
	}
}

func TestNewServer_ZeroTimeSeriesConfigLeavesMetricsUnlimited(t *testing.T) {
	srv := newTimeSeriesConfigServer(t, qrdbconfig.TimeSeriesConfig{})
	if err := writeClientMetrics(srv, 8); err != nil {
		t.Fatalf("max_metrics 0 must not cap: %v", err)
	}
}
