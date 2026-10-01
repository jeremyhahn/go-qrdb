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

package rowlock

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// Request outcomes, the values of the outcome label on
// qrdb_sql_row_lock_requests_total.
const (
	outcomeGranted  = "granted"
	outcomeWaited   = "waited"
	outcomeTimeout  = "timeout"
	outcomeNowait   = "nowait"
	outcomeDeadlock = "deadlock"
	outcomeLost     = "lost"
	outcomeCanceled = "canceled"

	// outcomeUnsearched is a requester made the deadlock victim because the
	// lock table held more than MaxDetectionRecords records, so the
	// wait-for graph was not searched. It is kept apart from deadlock: a
	// rising count means the lock table outgrew the search bound, not that
	// the application deadlocks.
	outcomeUnsearched = "unsearched"
)

// requestsTotal counts lock requests by mode and outcome.
var requestsTotal = sync.OnceValue(func() *metrics.CounterVec {
	return metrics.NewCounterVec(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "sql",
		Name:      "row_lock_requests_total",
		Help:      "SQL row-lock requests by lock mode and outcome.",
	}, []string{"mode", "outcome"})
})

// waitSecondsHist observes how long requests that waited spent waiting.
var waitSecondsHist = sync.OnceValue(func() *metrics.HistogramVec {
	return metrics.NewHistogramVec(metrics.HistogramOpts{
		Namespace: "qrdb",
		Subsystem: "sql",
		Name:      "row_lock_wait_seconds",
		Help:      "Time SQL row-lock requests that had to wait spent waiting.",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
	}, []string{"mode"})
})

// waitersGauge is the number of requests currently waiting on this node.
var waitersGauge = sync.OnceValue(func() *metrics.Gauge {
	return metrics.NewGauge(metrics.GaugeOpts{
		Namespace: "qrdb",
		Subsystem: "sql",
		Name:      "row_lock_waiters",
		Help:      "SQL row-lock requests currently waiting on this node.",
	})
})

// requests returns the counter for mode and outcome.
func requests(mode Mode, outcome string) *metrics.Counter {
	return requestsTotal().With(mode.String(), outcome)
}

// waitSeconds returns the wait histogram for mode.
func waitSeconds(mode Mode) *metrics.Histogram {
	return waitSecondsHist().With(mode.String())
}

// waiters returns the waiting-requests gauge.
func waiters() *metrics.Gauge {
	return waitersGauge()
}
