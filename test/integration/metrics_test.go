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

//go:build integration

package integration_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scrapeMetrics GETs /metrics over mTLS and returns the exposition text.
func scrapeMetrics(t *testing.T, ca *testCA, port int) string {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/metrics", port))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// sampleValue returns the value of the first unlabelled sample named name in
// the exposition text, and whether there was one.
func sampleValue(t *testing.T, exposition, name string) (float64, bool) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(exposition))
	for sc.Scan() {
		value, ok := strings.CutPrefix(sc.Text(), name+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("sample %s: %v", name, err)
		}
		return v, true
	}
	return 0, false
}

// TestMetricsEndpointServesRaftWALFsyncLatency boots a raft qrdbd, writes
// through Raft, and scrapes /metrics with the default gatherer: the WAL fsync
// histogram go-quicraft records must be served and must have counted the
// write's fsyncs, beside qrdb's own storage sync histogram and the Go
// runtime series, and only to an authenticated caller.
func TestMetricsEndpointServesRaftWALFsyncLatency(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	exposition := scrapeMetrics(t, ca, adminPort)

	count, ok := sampleValue(t, exposition, "quicraft_logdb_fsync_latency_seconds_count")
	if !ok {
		t.Fatal("/metrics does not serve quicraft_logdb_fsync_latency_seconds")
	}
	if count == 0 {
		t.Fatal("quicraft_logdb_fsync_latency_seconds counted no fsync after a committed write")
	}
	for _, family := range []string{
		"# TYPE qrdb_storage_sync_duration_seconds histogram",
		"# TYPE quicraft_raft_commit_latency_seconds histogram",
		"# TYPE go_goroutines gauge",
	} {
		if !strings.Contains(exposition, family) {
			t.Fatalf("/metrics lacks %q", family)
		}
	}

	// The route needs an authenticated caller: one without a client
	// certificate is refused, at the handshake or with a non-200.
	resp, err := caHTTPClient(ca.Pool, 10*time.Second).Get(fmt.Sprintf("https://127.0.0.1:%d/metrics", adminPort))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("GET /metrics without a client certificate = 200")
		}
	}
}

// sampleSum returns the sum of every sample of the family named name in the
// exposition text, labelled or not, and whether there was one.
func sampleSum(t *testing.T, exposition, name string) (float64, bool) {
	t.Helper()
	var sum float64
	found := false
	sc := bufio.NewScanner(strings.NewReader(exposition))
	for sc.Scan() {
		line := sc.Text()
		rest, ok := strings.CutPrefix(line, name)
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '{') {
			continue
		}
		if rest[0] == '{' {
			end := strings.LastIndexByte(rest, '}')
			if end < 0 {
				t.Fatalf("sample %s: unterminated labels in %q", name, line)
			}
			rest = rest[end+1:]
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			t.Fatalf("sample %s: %v", name, err)
		}
		sum += v
		found = true
	}
	return sum, found
}

// TestMetricsEndpointServesRaftHostSeries boots a three-node raft cluster:
// every node's /metrics serves go-quicraft's election, proposal and ReadIndex
// series, the cluster's election counters have counted the campaign that
// elected its leaders, and on every CLI transport a write and a linearizable
// read advance the cluster's proposal and ReadIndex histograms. The counts
// are summed over the three nodes because a shard's leader, which proposes,
// need not be the node the CLI talks to. A cluster, not a single node:
// go-quicraft counts an election when the node sends its vote requests, and
// a single voter sends none.
func TestMetricsEndpointServesRaftHostSeries(t *testing.T) {
	t.Parallel()
	nodes, _ := startMultiProtocolCluster(t, 0)

	const (
		proposals = "quicraft_raft_proposal_latency_seconds_count"
		readIndex = "quicraft_raft_read_index_latency_seconds_count"
		elections = "quicraft_raft_elections_total"
	)
	families := []string{proposals, readIndex, elections}
	// scrape returns each family's sum over every node's /metrics.
	scrape := func() map[string]float64 {
		t.Helper()
		sums := make(map[string]float64, len(families))
		for i, node := range nodes {
			exposition := scrapeMetrics(t, node.ca, node.restPort)
			for _, name := range families {
				v, ok := sampleSum(t, exposition, name)
				if !ok {
					t.Fatalf("node %d: /metrics does not serve %s", i, name)
				}
				sums[name] += v
			}
		}
		return sums
	}

	after := scrape()
	if after[elections] == 0 {
		t.Fatalf("%s = 0 on every node of a cluster that elected a leader", elections)
	}
	for _, proto := range multiProtocols {
		key := "metrics-host-series-" + proto.name
		before := after
		if out, err := runCLIProto(t, nodes[0], proto, "kv", "put", key, "v"); err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if out, err := runCLIProto(t, nodes[0], proto, "kv", "get", key, "--consistency", "linearizable"); err != nil {
			t.Fatalf("[%s] kv get %s --consistency linearizable: %v\noutput: %s", proto.name, key, err, out)
		}
		after = scrape()
		for _, name := range []string{proposals, readIndex} {
			if after[name] <= before[name] {
				t.Fatalf("[%s] %s %v -> %v across a committed write and a linearizable read", proto.name, name, before[name], after[name])
			}
		}
	}
}
