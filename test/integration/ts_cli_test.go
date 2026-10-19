// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// ts_cli_test.go drives the entire `qrdb ts` command tree -- write,
// write-batch, query, and aggregate -- through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) using
// the shared startMultiProtocolNode harness (multi_protocol_helpers_test.go).
// This closes Batch D's time-series gap identified by the CLI coverage
// audit: write/query/aggregate were previously exercised only via raw REST
// helpers (never the CLI binary), and write-batch had zero coverage of any
// kind.
//
// pkg/service/timeseries_service.go's TimeSeriesService backs every one of
// these operations natively (no xkms/quicbaas delegation, no stub), so this
// file asserts real, positive-path behavior throughout.
//
// Every subtest below the top-level per-protocol loop operates on a metric
// namespaced by protocol name (tscli_<proto>_<ts>) so the five protocol
// subtests -- which share one node -- never contend over the same metric's
// data points.
//
// This file itself only needs the "integration" build tag: pkg/service/
// timeseries_service.go and sdk/go/transport's TS types are compiled
// unconditionally (only cmd/qrdb's `ts` cobra subcommand tree is gated
// behind qrdb_ts). The qrdb_ts tag is instead carried by helpers_test.go's
// buildBinary/buildCLIBinary, which always compile the qrdbd daemon and
// qrdb CLI with "qrdb_ts qrdb_vector" regardless of this test package's own
// tags -- see the CLAUDE.md task brief's harness note.
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tsPointJSON mirrors transport.TSDataPoint, decoded from `ts query --output
// json` and used to build `ts write-batch --file` fixtures.
type tsPointJSON struct {
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	TimestampNs int64   `json:"timestamp_ns"`
	Tags        []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"tags,omitempty"`
}

// tsQueryResponseJSON mirrors transport.TSQueryResponse.
type tsQueryResponseJSON struct {
	Points []tsPointJSON `json:"points"`
}

// tsAggResultJSON mirrors transport.TSAggResult.
type tsAggResultJSON struct {
	TimestampNs int64   `json:"timestamp_ns"`
	Value       float64 `json:"value"`
	Count       int64   `json:"count"`
}

// tsAggregateResponseJSON mirrors transport.TSAggregateResponse.
type tsAggregateResponseJSON struct {
	Results []tsAggResultJSON `json:"results"`
}

// TestTS_CLI_PerProtocol drives the full `qrdb ts` lifecycle -- write (all
// its flag permutations), write-batch, query (all its flag permutations),
// and aggregate (every function) -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestTS_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTSLifecycleOverProtocol(t, node, proto)
		})
	}
}

// testTSLifecycleOverProtocol exercises the full ts command tree against a
// single protocol.
func testTSLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	metric := fmt.Sprintf("tscli_%s_%d", proto.name, time.Now().UnixNano())

	// A fixed, well-separated time window (nanoseconds since epoch) so the
	// query/aggregate range assertions below are exact regardless of the
	// wall-clock time this test runs at.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	t0 := base
	t1 := base + int64(time.Minute)
	t2 := base + int64(2*time.Minute)
	windowEnd := base + int64(3*time.Minute)

	// --- write -------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "ts", "write",
		"--metric", metric, "--value", "10", "--timestamp", strconv.FormatInt(t0, 10))
	if err != nil {
		t.Fatalf("[%s] ts write t0: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] ts write t0: expected OK, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "ts", "write",
		"--metric", metric, "--value", "20", "--timestamp", strconv.FormatInt(t1, 10),
		"--tag", "host=node-a", "--tag", "region=us")
	if err != nil {
		t.Fatalf("[%s] ts write t1 (with tags): %v\noutput: %s", proto.name, err, out)
	}

	// --timestamp omitted: the CLI defaults to time.Now(), which must still
	// succeed and land somewhere queryable.
	out, err = runCLIProto(t, node, proto, "ts", "write", "--metric", metric, "--value", "99")
	if err != nil {
		t.Fatalf("[%s] ts write (default timestamp): %v\noutput: %s", proto.name, err, out)
	}

	// A missing required --value must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "ts", "write", "--metric", metric)
	if err == nil {
		t.Fatalf("[%s] ts write with no --value succeeded, want error (output: %s)", proto.name, out)
	}

	// --- write-batch (JSON file) ------------------------------------------
	batchFile := filepath.Join(t.TempDir(), "ts-batch.json")
	batchPayload := fmt.Sprintf(`[
		{"metric":%q,"value":30,"timestamp_ns":%d,"tags":[{"key":"host","value":"node-b"}]},
		{"metric":%q,"value":40,"timestamp_ns":%d}
	]`, metric, t2, metric, t2+1)
	if werr := os.WriteFile(batchFile, []byte(batchPayload), 0o600); werr != nil {
		t.Fatalf("[%s] write ts write-batch fixture: %v", proto.name, werr)
	}
	out, err = runCLIProto(t, node, proto, "ts", "write-batch", "--file", batchFile)
	if err != nil {
		t.Fatalf("[%s] ts write-batch: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] ts write-batch: expected OK, got: %s", proto.name, out)
	}

	// write-batch from stdin ("-") must be equivalent to a file path (see
	// rebalance_cli_test.go's runCLIProtoStdin, which feeds a raw payload
	// string to the CLI subprocess's stdin).
	stdinPayload := fmt.Sprintf(`[{"metric":%q,"value":50,"timestamp_ns":%d}]`, metric, t2+2)
	out, err = runCLIProtoStdin(t, node, proto, stdinPayload, "ts", "write-batch", "--file", "-")
	if err != nil {
		t.Fatalf("[%s] ts write-batch --file -: %v\noutput: %s", proto.name, err, out)
	}

	// A missing required --file must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "ts", "write-batch")
	if err == nil {
		t.Fatalf("[%s] ts write-batch with no --file succeeded, want error (output: %s)", proto.name, out)
	}

	// --- query -------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "query",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10))
	if err != nil {
		t.Fatalf("[%s] ts query: %v\noutput: %s", proto.name, err, out)
	}
	var queryResp tsQueryResponseJSON
	if jerr := json.Unmarshal([]byte(out), &queryResp); jerr != nil {
		t.Fatalf("[%s] decode `ts query --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	// t0, t1, t2, t2+1, t2+2 = 5 points; the default-timestamp write from
	// earlier is excluded by the fixed [t0,windowEnd) range.
	if len(queryResp.Points) != 5 {
		t.Fatalf("[%s] ts query [t0,windowEnd) = %+v, want 5 points", proto.name, queryResp.Points)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "query",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10),
		"--tag", "host=node-a")
	if err != nil {
		t.Fatalf("[%s] ts query --tag host=node-a: %v\noutput: %s", proto.name, err, out)
	}
	var taggedResp tsQueryResponseJSON
	if jerr := json.Unmarshal([]byte(out), &taggedResp); jerr != nil {
		t.Fatalf("[%s] decode `ts query --tag --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(taggedResp.Points) != 1 || taggedResp.Points[0].Value != 20 {
		t.Fatalf("[%s] ts query --tag host=node-a = %+v, want exactly the t1 point (value=20)", proto.name, taggedResp.Points)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "query",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10), "--limit", "2")
	if err != nil {
		t.Fatalf("[%s] ts query --limit 2: %v\noutput: %s", proto.name, err, out)
	}
	var limitedResp tsQueryResponseJSON
	if jerr := json.Unmarshal([]byte(out), &limitedResp); jerr != nil {
		t.Fatalf("[%s] decode `ts query --limit --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(limitedResp.Points) != 2 {
		t.Fatalf("[%s] ts query --limit 2 = %+v, want exactly 2 points", proto.name, limitedResp.Points)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "query",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10), "--desc")
	if err != nil {
		t.Fatalf("[%s] ts query --desc: %v\noutput: %s", proto.name, err, out)
	}
	var descResp tsQueryResponseJSON
	if jerr := json.Unmarshal([]byte(out), &descResp); jerr != nil {
		t.Fatalf("[%s] decode `ts query --desc --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(descResp.Points) != 5 {
		t.Fatalf("[%s] ts query --desc = %+v, want 5 points", proto.name, descResp.Points)
	}
	for i := 1; i < len(descResp.Points); i++ {
		if descResp.Points[i-1].TimestampNs < descResp.Points[i].TimestampNs {
			t.Fatalf("[%s] ts query --desc points not descending: %+v", proto.name, descResp.Points)
		}
	}

	// A query with no matching points must succeed with an empty result set,
	// not error.
	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "query",
		"--metric", metric+"-does-not-exist", "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10))
	if err != nil {
		t.Fatalf("[%s] ts query (unknown metric): %v\noutput: %s", proto.name, err, out)
	}
	var emptyResp tsQueryResponseJSON
	if jerr := json.Unmarshal([]byte(out), &emptyResp); jerr != nil {
		t.Fatalf("[%s] decode `ts query` (unknown metric) JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(emptyResp.Points) != 0 {
		t.Fatalf("[%s] ts query (unknown metric) = %+v, want 0 points", proto.name, emptyResp.Points)
	}

	// --- aggregate -----------------------------------------------------
	// pkg/timeseries/storage.go's Aggregate buckets points by an
	// interval-width slice of ABSOLUTE Unix-epoch time (bucketIdx =
	// ts.UnixNano()/intervalNanos), not by an offset from --start. A bucket
	// width merely wider than [t0,windowEnd)'s span is not sufficient to
	// guarantee all 5 points land in one bucket unless t0 itself is
	// interval-aligned; base is UTC midnight, which IS aligned to a 24h
	// interval (Go's time package models UTC calendar days without leap
	// seconds), so a 24h bucket deterministically holds every point written
	// in this test regardless of wall-clock skew.
	fullInterval := strconv.FormatInt(int64(24*time.Hour), 10)
	for _, tc := range []struct {
		fn       string
		validate func(t *testing.T, results []tsAggResultJSON)
	}{
		{fn: "sum", validate: func(t *testing.T, r []tsAggResultJSON) {
			if len(r) != 1 || r[0].Value != 10+20+30+40+50 {
				t.Fatalf("[%s] ts aggregate sum = %+v, want a single bucket summing to 150", proto.name, r)
			}
		}},
		{fn: "avg", validate: func(t *testing.T, r []tsAggResultJSON) {
			if len(r) != 1 || r[0].Value != 30 {
				t.Fatalf("[%s] ts aggregate avg = %+v, want a single bucket averaging to 30", proto.name, r)
			}
		}},
		{fn: "min", validate: func(t *testing.T, r []tsAggResultJSON) {
			if len(r) != 1 || r[0].Value != 10 {
				t.Fatalf("[%s] ts aggregate min = %+v, want 10", proto.name, r)
			}
		}},
		{fn: "max", validate: func(t *testing.T, r []tsAggResultJSON) {
			if len(r) != 1 || r[0].Value != 50 {
				t.Fatalf("[%s] ts aggregate max = %+v, want 50", proto.name, r)
			}
		}},
		{fn: "count", validate: func(t *testing.T, r []tsAggResultJSON) {
			if len(r) != 1 || r[0].Count != 5 {
				t.Fatalf("[%s] ts aggregate count = %+v, want count=5", proto.name, r)
			}
		}},
	} {
		out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "aggregate",
			"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10),
			"--function", tc.fn, "--interval", fullInterval)
		if err != nil {
			t.Fatalf("[%s] ts aggregate --function %s: %v\noutput: %s", proto.name, tc.fn, err, out)
		}
		var aggResp tsAggregateResponseJSON
		if jerr := json.Unmarshal([]byte(out), &aggResp); jerr != nil {
			t.Fatalf("[%s] decode `ts aggregate --function %s --output json`: %v\noutput: %s", proto.name, tc.fn, jerr, out)
		}
		tc.validate(t, aggResp.Results)
	}

	// percentile is accepted by the CLI's function map but validated
	// separately (it is a real, distinct AggFunc, not merely an alias).
	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "aggregate",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10),
		"--function", "percentile", "--interval", fullInterval)
	if err != nil {
		t.Fatalf("[%s] ts aggregate --function percentile: %v\noutput: %s", proto.name, err, out)
	}

	// A narrower interval must produce more than one bucket.
	out, err = runCLIProto(t, node, proto, "--output", "json", "ts", "aggregate",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10),
		"--function", "sum", "--interval", strconv.FormatInt(int64(time.Minute), 10))
	if err != nil {
		t.Fatalf("[%s] ts aggregate --interval 1m: %v\noutput: %s", proto.name, err, out)
	}
	var perMinute tsAggregateResponseJSON
	if jerr := json.Unmarshal([]byte(out), &perMinute); jerr != nil {
		t.Fatalf("[%s] decode `ts aggregate --interval 1m --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(perMinute.Results) < 2 {
		t.Fatalf("[%s] ts aggregate --interval 1m = %+v, want more than 1 bucket over a 3-minute range", proto.name, perMinute.Results)
	}

	// An invalid --function must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "ts", "aggregate",
		"--metric", metric, "--start", strconv.FormatInt(t0, 10), "--end", strconv.FormatInt(windowEnd, 10),
		"--function", "not-a-function")
	if err == nil {
		t.Fatalf("[%s] ts aggregate --function not-a-function succeeded, want error (output: %s)", proto.name, out)
	}
}
