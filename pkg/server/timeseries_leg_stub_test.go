//go:build !qrdb_ts

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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// assertTimeSeriesLeg is the untagged twin of the leg in timeseries_leg_test.go
// -- see that file for why the leg is split at all.
//
// It is deliberately NOT a no-op. A helper that did nothing here would be a skip
// guard wearing a build constraint, and it would leave the untagged build
// asserting nothing about an engine it deliberately omits. So this twin asserts
// what the untagged build actually promises: the engine is reachable through the
// same seam and REFUSES on both verbs, naming the tag that would enable it,
// rather than panicking, hanging, or silently accepting a write it cannot store.
//
// The READ is asserted FIRST, and the order is load-bearing rather than
// stylistic. A refused write leaves the shard unable to serve any subsequent
// linearizable read -- a plain KV.Get on the same shard afterwards takes 5m6s
// and returns "quicraft: timeout" (go-qrdb#520, measured; the read path itself
// is healthy, refusing in 47.89us when no failed Apply precedes it). Querying
// after the write would therefore import a five-minute stall into every
// "go test ./pkg/server/" run, which is the exact cost this split exists to
// remove. Querying before it costs 47us and covers the verb honestly.
//
// When go-qrdb#520 is fixed the ordering stops mattering, and the latency is the
// assertion worth adding here then.
func assertTimeSeriesLeg(ctx context.Context, t *testing.T, ts service.TimeSeriesServicer, label, metric string) {
	t.Helper()

	at := time.Now()
	if _, err := ts.Query(ctx, metric, at.Add(-time.Minute), at.Add(time.Minute)); err == nil {
		t.Fatalf("%sTimeSeries.Query succeeded without -tags qrdb_ts; the stub engine must refuse, not report an empty result as if the metric had no points", label)
	} else {
		assertNamesTimeSeriesTag(t, err, label, "Query")
	}

	err := ts.Write(ctx, metric, 0.5, at, map[string]string{"h": "1"})
	if err == nil {
		t.Fatalf("%sTimeSeries.Write succeeded without -tags qrdb_ts; the stub engine must refuse, not silently drop the point", label)
	}
	assertNamesTimeSeriesTag(t, err, label, "Write")
}

// assertNamesTimeSeriesTag requires a refusal to name the build tag that would
// lift it, which is the only part of the message a caller can act on.
//
// Matched on the message rather than with errors.Is because the write's refusal
// is raised inside the Raft state machine's Apply and reaches the caller across
// that boundary, where the sentinel's identity does not survive. The tag name is
// what timeseries.ErrNotCompiled promises to carry, and it does survive.
func assertNamesTimeSeriesTag(t *testing.T, err error, label, verb string) {
	t.Helper()
	if !strings.Contains(err.Error(), "qrdb_ts") {
		t.Fatalf("%sTimeSeries.%s refused with %v; the error must name the qrdb_ts build tag so the caller knows what is missing", label, verb, err)
	}
}
