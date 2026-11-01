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

import "testing"

func TestMetricExistsRequest_ScansOneNameFromTheMetric(t *testing.T) {
	req := MetricExistsRequest("cpu.usage")
	if req.Prefix != "cpu.usage" || req.Limit != 1 {
		t.Fatalf("MetricExistsRequest = %+v, want prefix cpu.usage limit 1", req)
	}
}

func TestMetricExists(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		names  []string
		want   bool
	}{
		{name: "the metric itself", metric: "cpu", names: []string{"cpu"}, want: true},
		{name: "a longer metric it prefixes", metric: "cpu", names: []string{"cpu.usage"}, want: false},
		{name: "nothing", metric: "cpu", names: nil, want: false},
		{name: "empty metric", metric: "", names: []string{""}, want: false},
	}
	for _, c := range cases {
		if got := MetricExists(c.metric, c.names); got != c.want {
			t.Errorf("%s: MetricExists(%q, %v) = %v, want %v", c.name, c.metric, c.names, got, c.want)
		}
	}
}
