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

package statemachine

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// tsMetricCapRefusal decides a time-series write naming metrics against the
// shard's metric cap (protocol.AppendTSMetricCap) in the apply path, over the
// store the write applies to, so every replica reaches the same verdict in log
// order (go-qrdb#1088). It returns the refusal result and true when the write
// would take the store past maxMetrics distinct metric names; a maxMetrics of
// zero is no cap, and a write naming only held metrics is always admitted.
//
// The refusal is an application result, not an error, so consensus advances.
// ListMetrics returns names in key order, so an exact name is the first one
// its own prefix lists.
func tsMetricCapRefusal(ts TimeSeriesStore, metrics []string, maxMetrics uint32) (sm.Result, bool, error) {
	if maxMetrics == 0 {
		return sm.Result{}, false, nil
	}
	fresh := make(map[string]struct{}, len(metrics))
	for _, metric := range metrics {
		if metric == "" {
			continue
		}
		if _, seen := fresh[metric]; seen {
			continue
		}
		names, err := ts.ListMetrics(metric, 1)
		if err != nil {
			return sm.Result{}, false, &StorageError{Op: "ts-metric-cap", Inner: err}
		}
		if len(names) == 0 || names[0] != metric {
			fresh[metric] = struct{}{}
		}
	}
	if len(fresh) == 0 {
		return sm.Result{}, false, nil
	}
	refusal := sm.Result{Data: protocol.EncodeLimitRefusal(protocol.LimitMetrics, maxMetrics)}
	if uint64(len(fresh)) > uint64(maxMetrics) {
		return refusal, true, nil
	}
	room := int(maxMetrics) - len(fresh)
	held, err := ts.ListMetrics("", room+1)
	if err != nil {
		return sm.Result{}, false, &StorageError{Op: "ts-metric-cap", Inner: err}
	}
	if len(held) > room {
		return refusal, true, nil
	}
	return sm.Result{}, false, nil
}

// tsBatchMetrics returns the metric name of every point in points.
func tsBatchMetrics(points []protocol.TSWriteParams) []string {
	metrics := make([]string, len(points))
	for i, p := range points {
		metrics[i] = p.Metric
	}
	return metrics
}
