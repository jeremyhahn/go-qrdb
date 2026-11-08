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

package engine

import (
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// MetricLimitResult reports whether err is a time-series write refused by
// the shard's metric cap (timeseries.AdmitMetrics) and, if it is, the
// application result that carries the refusal back to the proposer. The
// refusal is a deterministic consequence of committed state, so it is an
// application result rather than an error that would stall consensus.
func MetricLimitResult(err error) (sm.Result, bool) {
	var limit *timeseries.MetricLimitError
	if !errors.As(err, &limit) {
		return sm.Result{}, false
	}
	return sm.Result{Data: protocol.EncodeLimitRefusal(protocol.LimitMetrics, limit.Max)}, true
}

// TSBatchMetrics returns the metric name of every point in points, the
// names a batch write asks timeseries.AdmitMetrics to admit.
func TSBatchMetrics(points []protocol.TSWriteParams) []string {
	metrics := make([]string, len(points))
	for i, p := range points {
		metrics[i] = p.Metric
	}
	return metrics
}
