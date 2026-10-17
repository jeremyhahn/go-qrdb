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
