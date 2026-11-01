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

package server

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// newMetricLimitServices builds the data-plane services over a memory MVCC
// store with time-series config tsCfg.
func newMetricLimitServices(t *testing.T, tsCfg qrdbconfig.TimeSeriesConfig) *DatabaseServices {
	t.Helper()
	stg := memory.NewStorage()
	ts, err := timeseries.New(stg)
	if err != nil {
		t.Fatalf("timeseries.New: %v", err)
	}
	eng, err := engine.NewMVCCEngine(stg, engine.WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{}, qrdbconfig.IndexConfig{}, tsCfg)
}

var metricLimitNow = time.Unix(1_700_000_000, 0)

// writeMetrics writes one point to each of n metrics named m-0..m-(n-1) and
// returns the first refusal.
func writeMetrics(svcs *DatabaseServices, n int) error {
	for i := 0; i < n; i++ {
		if err := svcs.TimeSeries.Write(context.Background(), "m-"+strconv.Itoa(i), 1, metricLimitNow, nil); err != nil {
			return err
		}
	}
	return nil
}

func TestBuildDataPlaneServices_MaxMetricsRefusesTheMetricPastTheCap(t *testing.T) {
	svcs := newMetricLimitServices(t, qrdbconfig.TimeSeriesConfig{MaxMetrics: 2})
	if err := writeMetrics(svcs, 2); err != nil {
		t.Fatalf("metrics under the cap: %v", err)
	}
	var le *service.MetricLimitError
	if err := svcs.TimeSeries.Write(context.Background(), "m-extra", 1, metricLimitNow, nil); !errors.As(err, &le) {
		t.Fatalf("third metric at max_metrics 2: err = %v (%T), want *service.MetricLimitError", err, err)
	}
	if err := svcs.TimeSeries.Write(context.Background(), "m-0", 2, metricLimitNow.Add(time.Second), nil); err != nil {
		t.Fatalf("a point on a metric that exists, at the cap: %v", err)
	}
}

func TestBuildDataPlaneServices_ZeroTimeSeriesConfigLeavesMetricsUnlimited(t *testing.T) {
	svcs := newMetricLimitServices(t, qrdbconfig.TimeSeriesConfig{})
	if err := writeMetrics(svcs, 8); err != nil {
		t.Fatalf("max_metrics 0 must not cap: %v", err)
	}
}

func TestWithTenantHostTimeSeriesConfig_SetsTheRegistryCap(t *testing.T) {
	r := &TenantHostRegistry{}
	WithTenantHostTimeSeriesConfig(qrdbconfig.TimeSeriesConfig{MaxMetrics: 5})(r)
	if r.tsCfg.MaxMetrics != 5 {
		t.Fatalf("tsCfg.MaxMetrics = %d, want 5", r.tsCfg.MaxMetrics)
	}
}

func TestWithTenantHostTimeSeriesConfig_ZeroLeavesTheRegistryUnlimited(t *testing.T) {
	r := &TenantHostRegistry{tsCfg: qrdbconfig.TimeSeriesConfig{MaxMetrics: 5}}
	WithTenantHostTimeSeriesConfig(qrdbconfig.TimeSeriesConfig{})(r)
	if r.tsCfg.MaxMetrics != 0 {
		t.Fatalf("tsCfg.MaxMetrics = %d, want 0", r.tsCfg.MaxMetrics)
	}
}

func TestWithTimeSeriesConfig_SetsTheFactoryCap(t *testing.T) {
	f := &RaftBackendFactory{}
	WithTimeSeriesConfig(qrdbconfig.TimeSeriesConfig{MaxMetrics: 7})(f)
	if f.tsCfg.MaxMetrics != 7 {
		t.Fatalf("tsCfg.MaxMetrics = %d, want 7", f.tsCfg.MaxMetrics)
	}
}

func TestWithTimeSeriesConfig_ZeroLeavesTheFactoryUnlimited(t *testing.T) {
	f := &RaftBackendFactory{tsCfg: qrdbconfig.TimeSeriesConfig{MaxMetrics: 7}}
	WithTimeSeriesConfig(qrdbconfig.TimeSeriesConfig{})(f)
	if f.tsCfg.MaxMetrics != 0 {
		t.Fatalf("tsCfg.MaxMetrics = %d, want 0", f.tsCfg.MaxMetrics)
	}
}
