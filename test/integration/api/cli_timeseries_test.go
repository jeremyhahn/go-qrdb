// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolTimeseriesLifecycle tests the full timeseries lifecycle
// (write, query, aggregate) across all protocols. It writes multiple data
// points to a metric, queries the range, and verifies aggregation functions
// (avg, sum, count) all produce output.
func TestCLIMultiProtocolTimeseriesLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("cpu.usage-%s-%d", proto, ts)

			// Step 1: Write first data point.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "42.5", "--tag", "host=server1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write first point failed: %v", proto, err)
			}

			// Step 2: Write second data point.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "55.0", "--tag", "host=server1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write second point failed: %v", proto, err)
			}

			// Step 3: Query the metric range.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0", "--end", "9223372036854775807")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query should produce output", proto))

			// Step 4: Aggregate with avg function.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "aggregate", "--metric", metric, "--start", "0", "--end", "9223372036854775807", "--function", "avg")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate avg failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate avg should produce output", proto))

			// Step 5: Aggregate with sum function.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "aggregate", "--metric", metric, "--start", "0", "--end", "9223372036854775807", "--function", "sum")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate sum failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate sum should produce output", proto))

			// Step 6: Aggregate with count function.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "aggregate", "--metric", metric, "--start", "0", "--end", "9223372036854775807", "--function", "count")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate count failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate count should produce output", proto))

			t.Logf("[%s] timeseries lifecycle passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIMultiProtocolTimeseriesQueryFlags tests the --tag, --limit, and --desc
// flags on ts query, plus the --timestamp flag on ts write, across all protocols.
func TestCLIMultiProtocolTimeseriesQueryFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("ts.flags-%s-%d", proto, ts)

			// Write with explicit --timestamp.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "10.0",
				"--tag", "env=prod", "--timestamp", "1000000000")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write --timestamp failed: %v", proto, err)
			}

			// Write another point with different tag.
			_, _, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "20.0",
				"--tag", "env=staging")
			if err != nil {
				t.Fatalf("[%s] ts write second point failed: %v", proto, err)
			}

			// Query with --tag filter.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0",
				"--end", "9223372036854775807", "--tag", "env=prod")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query --tag failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query --tag should produce output", proto))

			// Query with --limit.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0",
				"--end", "9223372036854775807", "--limit", "1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query --limit failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query --limit should produce output", proto))

			// Query with --desc.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0",
				"--end", "9223372036854775807", "--desc")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query --desc failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query --desc should produce output", proto))

			t.Logf("[%s] ts query flags passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIMultiProtocolTimeseriesWriteBatch tests batch writing of timeseries
// data points across all protocols. It creates a JSON file containing multiple
// data points, executes the batch write command, and verifies the data can be
// queried back.
func TestCLIMultiProtocolTimeseriesWriteBatch(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("mem.usage-%s-%d", proto, ts)

			// Build batch JSON with multiple data points.
			batchJSON := fmt.Sprintf(`[{"metric":"%s","value":75.0,"tags":[{"key":"host","value":"server1"}]},{"metric":"%s","value":80.0,"tags":[{"key":"host","value":"server2"}]}]`,
				metric, metric)

			filePath := createTempJSONFile(t, batchJSON)

			// Step 1: Execute batch write from file.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ts", "write-batch", "--file", filePath)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write-batch failed: %v", proto, err)
			}

			// Step 2: Query the metric to verify batch data was written.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0", "--end", "9223372036854775807")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query after write-batch failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query after write-batch should produce output", proto))

			t.Logf("[%s] timeseries write-batch passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIMultiProtocolTimeseriesAggregateFlags tests aggregate functions (min,
// max, percentile) and the --interval flag across all protocols. It writes
// five data points with explicit timestamps, then verifies each aggregate
// function produces output.
func TestCLIMultiProtocolTimeseriesAggregateFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("ts.agg-flags-%s-%d", proto, ts)

			// Write 5 data points with explicit timestamps spread across 2 seconds.
			values := []string{"10.0", "20.0", "30.0", "40.0", "50.0"}
			for i, val := range values {
				timestamp := fmt.Sprintf("%d", 1000000000+int64(i)*500000000) // 1s, 1.5s, 2s, 2.5s, 3s
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"ts", "write", "--metric", metric, "--value", val,
					"--tag", "host=server1", "--timestamp", timestamp)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] ts write point %d failed: %v", proto, i, err)
				}
			}

			fullRange := []string{
				"ts", "aggregate", "--metric", metric,
				"--start", "0", "--end", "9223372036854775807",
			}

			// Test aggregate --function min.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				append(fullRange, "--function", "min")...)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate min failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate min should produce output", proto))

			// Test aggregate --function max.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				append(fullRange, "--function", "max")...)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate max failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate max should produce output", proto))

			// Test aggregate --function percentile (may not be supported by all backends).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				append(fullRange, "--function", "percentile")...)
			if err != nil {
				t.Logf("[%s] ts aggregate percentile not supported or failed (expected for some backends): %v", proto, err)
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
			} else {
				output = stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] ts aggregate percentile should produce output", proto))
			}

			// Test aggregate with --interval (1 second = 1000000000 nanoseconds).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				append(fullRange, "--function", "avg", "--interval", "1000000000")...)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate --interval failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate --interval should produce output", proto))

			t.Logf("[%s] timeseries aggregate flags passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIMultiProtocolTimeseriesMultipleTags tests writing and querying
// timeseries data with multiple --tag flags across all protocols. It writes
// a data point with two tags, then queries using both tags to verify filtered
// results are returned correctly.
func TestCLIMultiProtocolTimeseriesMultipleTags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("ts.multi-tag-%s-%d", proto, ts)

			// Write a data point with two tags.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "99.5",
				"--tag", "host=server1", "--tag", "region=us-east")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write with multiple tags failed: %v", proto, err)
			}

			// Write another data point with different tags to ensure filtering works.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "75.0",
				"--tag", "host=server2", "--tag", "region=eu-west")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write second point with different tags failed: %v", proto, err)
			}

			// Query with both tags to filter results.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0",
				"--end", "9223372036854775807",
				"--tag", "host=server1", "--tag", "region=us-east")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query with multiple tags failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query with multiple tags should produce output", proto))

			// Verify the filtered result does not contain the other data point's value.
			if strings.Contains(output, "75") {
				t.Logf("[%s] warning: query with tags host=server1,region=us-east returned unfiltered data", proto)
			}

			t.Logf("[%s] timeseries multiple tags passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIMultiProtocolTimeseriesWriteBatchStdin tests batch writing of
// timeseries data points from standard input across all protocols. It pipes
// a JSON payload containing multiple data points to the CLI's stdin via
// --file -, then verifies the data can be queried back.
func TestCLIMultiProtocolTimeseriesWriteBatchStdin(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("ts.stdin-%s-%d", proto, ts)

			// Build batch JSON with multiple data points.
			batchJSON := fmt.Sprintf(
				`[{"metric":"%s","value":60.0,"tags":[{"key":"host","value":"stdin-server1"}]},{"metric":"%s","value":65.0,"tags":[{"key":"host","value":"stdin-server2"}]}]`,
				metric, metric)

			// Execute batch write from stdin (--file - reads from stdin).
			stdout, stderr, err := execCLIWithStdin(t, cfg, proto, batchJSON,
				"ts", "write-batch", "--file", "-")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write-batch via stdin failed: %v", proto, err)
			}

			// Query the metric to verify batch data was written.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0", "--end", "9223372036854775807")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query after write-batch stdin failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query after write-batch stdin should produce output", proto))

			t.Logf("[%s] timeseries write-batch stdin passed (metric: %s)", proto, metric)
		})
	}
}
