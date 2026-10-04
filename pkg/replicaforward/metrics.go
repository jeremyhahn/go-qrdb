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

package replicaforward

import (
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// Forward outcomes, the outcome label of qrdb_replica_forward_total.
const (
	// outcomeOK: the first target served.
	outcomeOK = "ok"
	// outcomeRetriedOK: a target after the first served.
	outcomeRetriedOK = "retried_ok"
	// outcomeRefused: no target served; the caller got the refusal.
	outcomeRefused = "refused"
	// outcomeError: the forward failed after it may have reached a target,
	// or the member records could not be read.
	outcomeError = "error"
)

// forwardTotal counts forwards by protocol and outcome.
var forwardTotal = sync.OnceValue(func() *metrics.CounterVec {
	return metrics.NewCounterVec(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "replica_forward",
		Name:      "total",
		Help:      "Requests a node holding no replica forwarded to a replica, by protocol and outcome (ok, retried_ok, refused, error).",
	}, []string{"protocol", "outcome"})
})

// forwardDuration times forwards by protocol, from the first send to the
// answer or the refusal.
var forwardDuration = sync.OnceValue(func() *metrics.HistogramVec {
	return metrics.NewHistogramVec(metrics.HistogramOpts{
		Namespace: "qrdb",
		Subsystem: "replica_forward",
		Name:      "duration_seconds",
		Help:      "Time a forward to a replica took, by protocol.",
		Buckets:   metrics.Buckets.Seconds(),
	}, []string{"protocol"})
})

// observe records one forward's outcome and duration.
func observe(proto cluster.APIProtocol, outcome string, start time.Time) {
	forwardTotal().With(string(proto), outcome).Inc()
	forwardDuration().With(string(proto)).Observe(time.Since(start).Seconds())
}
