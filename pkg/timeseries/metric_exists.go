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

// MetricExistsRequest is the metric-index read that answers whether metric
// exists without listing the index: a TSListMetricsRequest scanned from
// metric's own index key and limited to one name. The index is ordered by
// key and metric's key sorts before every other key it prefixes, so the
// scan reads at most one key; MetricExists decides from what it returns.
func MetricExistsRequest(metric string) TSListMetricsRequest {
	return TSListMetricsRequest{Prefix: metric, Limit: 1}
}

// MetricExists reports whether names, the answer to
// MetricExistsRequest(metric), names metric itself rather than a longer
// metric it prefixes.
func MetricExists(metric string, names []string) bool {
	return metric != "" && len(names) > 0 && names[0] == metric
}
