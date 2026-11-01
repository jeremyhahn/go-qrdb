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

package timeseries

import (
	"testing"
	"time"
)

// TestMetricExists_AgainstTheMetricIndex runs the probe against a real
// metric index: it finds a metric that exists, and not one that only
// prefixes, or sorts beside, the metrics that do.
func TestMetricExists_AgainstTheMetricIndex(t *testing.T) {
	s := newStorage(t)
	for _, m := range []string{"cp", "cpu.usage", "mem"} {
		if err := write(t, s, m, 1, time.Unix(1_700_000_000, 0), nil); err != nil {
			t.Fatalf("write %s: %v", m, err)
		}
	}
	for metric, want := range map[string]bool{
		"cp":        true,
		"cpu.usage": true,
		"mem":       true,
		"cpu":       false,
		"c":         false,
		"zzz":       false,
	} {
		req := MetricExistsRequest(metric)
		names, err := s.ListMetrics(req.Prefix, req.Limit)
		if err != nil {
			t.Fatalf("ListMetrics(%q): %v", metric, err)
		}
		if got := MetricExists(metric, names); got != want {
			t.Errorf("MetricExists(%q) = %v (names %v), want %v", metric, got, names, want)
		}
	}
}
