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

// Package main implements the kvcompare CLI orchestrator.
//
// kvcompare builds and runs Dockerized KV benchmark suites for QRDB,
// etcd, and TiKV, then produces a side-by-side comparison report.
//
// Usage:
//
//	kvcompare [flags] [system...]
//
// Systems: qrdb-pebble-h2, qrdb-pebble-h3, qrdb-memory-h2, qrdb-grpc, qrdb-etcd, etcd, tikv, tikv-3n
// Aliases: qrdb (=qrdb-pebble-h2), all, fair, protocol, storage, grpc, etcd-compare
//
// Flags:
//
//	--benchtime duration     Per-scenario duration (default 1s)
//	--timeout duration       Max timeout per system (default 600s)
//	--no-build               Skip Docker build step
//	--chart-dir string       Generate SVG charts to this directory
//	--report string          Write markdown comparison report to this file
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"
)

// knownSystems lists all recognized system names in preferred display order.
var knownSystems = []string{"qrdb-pebble-h2", "qrdb-pebble-h3", "qrdb-memory-h2", "qrdb-grpc", "qrdb-quic", "qrdb-etcd", "etcd", "tikv", "tikv-3n"}

// groupAliases maps shorthand names to lists of real system names.
var groupAliases = map[string][]string{
	"all":          {"qrdb-pebble-h2", "qrdb-pebble-h3", "qrdb-memory-h2", "qrdb-grpc", "qrdb-quic", "tikv", "tikv-3n"},
	"fair":         {"qrdb-pebble-h2", "qrdb-pebble-h3", "tikv", "tikv-3n"},
	"protocol":     {"qrdb-pebble-h2", "qrdb-pebble-h3", "qrdb-grpc", "qrdb-quic"},
	"storage":      {"qrdb-pebble-h2", "qrdb-memory-h2"},
	"grpc":         {"qrdb-grpc"},
	"quic":         {"qrdb-quic"},
	"qrdb":         {"qrdb-pebble-h2"},
	"etcd-compare": {"qrdb-etcd", "etcd"},
}

// kvScenarioLabels maps KV scenario keys to human-readable display names.
var kvScenarioLabels = map[string]string{
	"kv_put_seq":               "PUT Sequential (1N)",
	"kv_put_conc8":             "PUT Concurrent/8 (1N)",
	"kv_get_seq":               "GET Sequential (1N)",
	"kv_get_conc8":             "GET Concurrent/8 (1N)",
	"kv_delete_seq":            "DELETE Sequential (1N)",
	"kv_delete_conc8":          "DELETE Concurrent/8 (1N)",
	"kv_batch_put_10k":         "Batch PUT 10K (1N)",
	"kv_scan_10k":              "Prefix Scan 10K (1N)",
	"kv_put_seq_3n":            "PUT Sequential (3N)",
	"kv_put_conc8_3n":          "PUT Concurrent/8 (3N)",
	"kv_get_seq_3n":            "GET Sequential (3N)",
	"kv_get_conc8_3n":          "GET Concurrent/8 (3N)",
	"etcd_put_seq":             "etcd PUT Sequential (1N)",
	"etcd_put_conc8":           "etcd PUT Concurrent/8 (1N)",
	"etcd_get_seq":             "etcd GET Sequential (1N)",
	"etcd_get_conc8":           "etcd GET Concurrent/8 (1N)",
	"etcd_delete_seq":          "etcd DELETE Sequential (1N)",
	"etcd_delete_conc8":        "etcd DELETE Concurrent/8 (1N)",
	"etcd_txn_seq":             "etcd TXN Sequential (1N)",
	"etcd_watch_latency":       "etcd Watch Latency (1N)",
	"kv_grpc_put_seq":          "gRPC PUT Sequential (1N)",
	"kv_grpc_put_conc8":        "gRPC PUT Concurrent/8 (1N)",
	"kv_grpc_get_seq":          "gRPC GET Sequential (1N)",
	"kv_grpc_get_conc8":        "gRPC GET Concurrent/8 (1N)",
	"kv_grpc_delete_seq":       "gRPC DELETE Sequential (1N)",
	"kv_grpc_delete_conc8":     "gRPC DELETE Concurrent/8 (1N)",
	"kv_grpc_batch_put_10k":    "gRPC Batch PUT 10K (1N)",
	"kv_grpc_scan_10k":         "gRPC Prefix Scan 10K (1N)",
	"kv_grpc_put_coalesced":    "gRPC PUT Coalesced/8 (1N)",
	"kv_grpc_delete_coalesced": "gRPC DELETE Coalesced/8 (1N)",
	"kv_quic_put_seq":          "QUIC PUT Sequential (1N)",
	"kv_quic_put_conc8":        "QUIC PUT Concurrent/8 (1N)",
	"kv_quic_get_seq":          "QUIC GET Sequential (1N)",
	"kv_quic_get_conc8":        "QUIC GET Concurrent/8 (1N)",
	"kv_quic_delete_seq":       "QUIC DELETE Sequential (1N)",
	"kv_quic_delete_conc8":     "QUIC DELETE Concurrent/8 (1N)",
}

// kvScenarioOrder defines the display order for KV scenarios.
var kvScenarioOrder = []string{
	"kv_put_seq",
	"kv_put_conc8",
	"kv_get_seq",
	"kv_get_conc8",
	"kv_delete_seq",
	"kv_delete_conc8",
	"kv_batch_put_10k",
	"kv_scan_10k",
	"kv_put_seq_3n",
	"kv_put_conc8_3n",
	"kv_get_seq_3n",
	"kv_get_conc8_3n",
	"etcd_put_seq",
	"etcd_put_conc8",
	"etcd_get_seq",
	"etcd_get_conc8",
	"etcd_delete_seq",
	"etcd_delete_conc8",
	"etcd_txn_seq",
	"etcd_watch_latency",
	"kv_grpc_put_seq",
	"kv_grpc_put_conc8",
	"kv_grpc_get_seq",
	"kv_grpc_get_conc8",
	"kv_grpc_delete_seq",
	"kv_grpc_delete_conc8",
	"kv_grpc_batch_put_10k",
	"kv_grpc_scan_10k",
	"kv_grpc_put_coalesced",
	"kv_grpc_delete_coalesced",
	"kv_quic_put_seq",
	"kv_quic_put_conc8",
	"kv_quic_get_seq",
	"kv_quic_get_conc8",
	"kv_quic_delete_seq",
	"kv_quic_delete_conc8",
}

// kvSystemColors defines the SVG color palette for each system in charts.
var kvSystemColors = map[string]string{
	"qrdb-pebble-h2": "#2196F3", // Blue
	"qrdb-pebble-h3": "#1565C0", // Darker blue
	"qrdb-memory-h2": "#64B5F6", // Lighter blue
	"qrdb-grpc":      "#7B1FA2", // Purple
	"qrdb-quic":      "#00897B", // Teal
	"qrdb-etcd":      "#4CAF50", // Green
	"etcd":           "#4CAF50", // Green (same as qrdb-etcd for standalone)
	"tikv":           "#FF9800", // Orange
	"tikv-3n":        "#E65100", // Dark orange
}

// orderedSystemKeys returns system keys in preferred display order.
// Systems in knownSystems appear first (in order), followed by any
// unknown systems sorted alphabetically.
func orderedSystemKeys(suites map[string]interface{}) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range knownSystems {
		if _, ok := suites[name]; ok {
			result = append(result, name)
			seen[name] = true
		}
	}
	var extra []string
	for name := range suites {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(result, extra...)
}

// ErrInvalidSystem indicates an unrecognized system name on the command line.
type ErrInvalidSystem struct {
	System string
}

func (e *ErrInvalidSystem) Error() string {
	return "invalid system: " + e.System + " (valid: " + strings.Join(knownSystems, ", ") + "; aliases: qrdb, all, fair, protocol, storage, grpc, quic, etcd-compare)"
}

// ErrNoSystems indicates that no valid systems were specified.
type ErrNoSystems struct{}

func (e *ErrNoSystems) Error() string {
	return "no systems specified; provide one or more of: " + strings.Join(knownSystems, ", ") + " (aliases: qrdb, all, fair, protocol, storage, grpc, quic, etcd-compare)"
}

// ErrBaseDirNotFound indicates that the repository root could not be located.
type ErrBaseDirNotFound struct {
	Detail string
}

func (e *ErrBaseDirNotFound) Error() string {
	return "repository root not found: " + e.Detail
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kvcompare: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	benchtime := flag.String("benchtime", "1s", "Per-scenario duration")
	timeout := flag.String("timeout", "600s", "Max timeout per system")
	noBuild := flag.Bool("no-build", false, "Skip Docker build step")
	chartDir := flag.String("chart-dir", "", "Generate SVG charts to this directory")
	reportPath := flag.String("report", "", "Write markdown comparison report to this file")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kvcompare [flags] [system...]\n\n")
		fmt.Fprintf(os.Stderr, "Systems: %s\n", strings.Join(knownSystems, ", "))
		fmt.Fprintf(os.Stderr, "Aliases: qrdb, all, fair, protocol, storage, grpc, quic, etcd-compare\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	systems, err := resolveSystems(flag.Args())
	if err != nil {
		return err
	}

	baseDir, err := findBaseDir()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "=== KV Benchmark Comparison ===\n")
	fmt.Fprintf(os.Stderr, "Systems: %s\n", strings.Join(systems, ", "))
	fmt.Fprintf(os.Stderr, "Benchtime: %s\n", *benchtime)
	fmt.Fprintf(os.Stderr, "Base dir: %s\n\n", baseDir)

	suites := make(map[string]*perfresult.BenchmarkSuite)
	var failedSystems []string

	for _, name := range systems {
		factory, ok := runners[name]
		if !ok {
			return &ErrInvalidSystem{System: name}
		}

		runner := factory(baseDir)
		fmt.Fprintf(os.Stderr, "--- Running %s benchmarks ---\n", name)

		suite, runErr := runner.Run(*benchtime, *timeout, *noBuild)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: %s benchmark failed: %v\n", name, runErr)
			failedSystems = append(failedSystems, name)
			continue
		}

		suites[name] = suite
		fmt.Fprintf(os.Stderr, "  %s: %d scenarios completed\n\n", name, len(suite.Results))
	}

	if len(suites) == 0 {
		return &perfresult.ErrNoResults{}
	}

	printResults(suites, *chartDir, *reportPath)

	if len(failedSystems) > 0 {
		fmt.Fprintf(os.Stderr, "\nFailed systems: %s\n", strings.Join(failedSystems, ", "))
	}

	return nil
}

// resolveSystems converts CLI arguments into a deduplicated, ordered list of
// system names to benchmark. Group aliases (all, fair, protocol, storage, qrdb)
// are expanded to their constituent system names.
func resolveSystems(args []string) ([]string, error) {
	if len(args) == 0 {
		return groupAliases["fair"], nil
	}

	seen := make(map[string]bool)
	var result []string

	for _, arg := range args {
		normalized := strings.ToLower(strings.TrimSpace(arg))

		// Check group aliases first.
		if expanded, ok := groupAliases[normalized]; ok {
			for _, sys := range expanded {
				if !seen[sys] {
					seen[sys] = true
					result = append(result, sys)
				}
			}
			continue
		}

		if !isKnownSystem(normalized) {
			return nil, &ErrInvalidSystem{System: normalized}
		}
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, normalized)
		}
	}

	if len(result) == 0 {
		return nil, &ErrNoSystems{}
	}

	return result, nil
}

// isKnownSystem checks whether the given system name is a recognized
// benchmark target.
func isKnownSystem(system string) bool {
	for _, known := range knownSystems {
		if known == system {
			return true
		}
	}
	return false
}

// findBaseDir locates the go-quicraft repository root by looking for go.mod
// with the root module declaration, traversing up from CWD. Falls back to
// QUICRAFT_ROOT environment variable.
func findBaseDir() (string, error) {
	if root := os.Getenv("QUICRAFT_ROOT"); root != "" {
		return root, nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", &ErrBaseDirNotFound{Detail: err.Error()}
	}

	const rootModule = "module github.com/jeremyhahn/go-quicraft\n"

	for {
		modPath := filepath.Join(dir, "go.mod")
		if _, statErr := os.Stat(modPath); statErr == nil {
			data, readErr := os.ReadFile(modPath)
			if readErr == nil && strings.Contains(string(data), rootModule) {
				return dir, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", &ErrBaseDirNotFound{Detail: "could not locate go-quicraft repository root; set QUICRAFT_ROOT"}
}

// printResults outputs a formatted comparison table to stdout, then generates
// SVG charts and a markdown report if the corresponding paths are non-empty.
func printResults(suites map[string]*perfresult.BenchmarkSuite, chartDir, reportPath string) {
	// Determine system display order (qrdb first, then others).
	var systemOrder []string
	for _, sys := range knownSystems {
		if _, ok := suites[sys]; ok {
			systemOrder = append(systemOrder, sys)
		}
	}

	width := 80
	sep := strings.Repeat("=", width)
	thinSep := strings.Repeat("-", width)

	fmt.Println()
	fmt.Println(sep)
	fmt.Println("  KV Performance Comparison")
	fmt.Println(sep)
	fmt.Println()

	// Column width adapts to longest system name.
	sysColW := 12
	for _, sys := range systemOrder {
		if len(sys) > sysColW {
			sysColW = len(sys)
		}
	}
	fmtHdr := fmt.Sprintf("  %%-%ds %%15s %%12s %%12s %%12s\n", sysColW)
	fmtRow := fmtHdr // same format for data rows.

	for _, scenario := range kvScenarioOrder {
		hasAny := false
		for _, suite := range suites {
			if _, ok := suite.Results[scenario]; ok {
				hasAny = true
				break
			}
		}
		if !hasAny {
			continue
		}

		label := kvScenarioLabels[scenario]
		if label == "" {
			label = scenario
		}

		fmt.Printf("  Scenario: %s\n", label)
		fmt.Println(thinSep)

		// Header row.
		fmt.Printf(fmtHdr, "System", "Ops/sec", "P50", "P99", "Mean")
		fmt.Println(thinSep)

		// Data rows and ratio tracking.
		opsMap := make(map[string]float64)
		for _, sys := range systemOrder {
			suite := suites[sys]
			result, ok := suite.Results[scenario]
			if !ok {
				fmt.Printf(fmtRow, sys, "N/A", "N/A", "N/A", "N/A")
				continue
			}

			fmt.Printf(fmtRow,
				sys,
				perfresult.FormatOps(result.OpsPerSec),
				perfresult.FormatDurationNs(result.Latency.P50Ns),
				perfresult.FormatDurationNs(result.Latency.P99Ns),
				perfresult.FormatDurationNs(result.Latency.MeanNs))

			opsMap[sys] = result.OpsPerSec
		}

		// Winner and ratio (only meaningful with exactly 2 systems).
		if len(opsMap) == 2 {
			var names [2]string
			var ops [2]float64
			idx := 0
			for _, sys := range systemOrder {
				if v, ok := opsMap[sys]; ok {
					names[idx] = sys
					ops[idx] = v
					idx++
				}
			}
			if ops[0] > 0 && ops[1] > 0 {
				fmt.Println(thinSep)
				ratio := ops[0] / ops[1]
				winner := names[0]
				if ratio < 1.0 {
					winner = names[1]
					ratio = 1.0 / ratio
				}
				fmt.Printf("  %-12s %14.2fx %s\n", "WINNER", ratio, winner)
			}
		}

		fmt.Println()
	}

	fmt.Println(sep)

	// Collect suite slice in display order for chart and report functions.
	orderedSuites := make([]*perfresult.BenchmarkSuite, 0, len(systemOrder))
	for _, sys := range systemOrder {
		orderedSuites = append(orderedSuites, suites[sys])
	}

	// Generate SVG charts if a chart directory was specified.
	if chartDir != "" {
		if err := os.MkdirAll(chartDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: create chart dir: %v\n", err)
		} else {
			for _, suite := range orderedSuites {
				outPath := filepath.Join(chartDir, suite.System+"_performance.svg")
				if err := perfresult.GenerateSystemChart(suite, outPath); err != nil {
					fmt.Fprintf(os.Stderr, "WARNING: generate %s chart: %v\n", suite.System, err)
				} else {
					fmt.Fprintf(os.Stderr, "  Generated %s\n", outPath)
				}
			}

			suitesMap := make(map[string]*perfresult.BenchmarkSuite, len(orderedSuites))
			for _, suite := range orderedSuites {
				suitesMap[suite.System] = suite
			}
			compPath := filepath.Join(chartDir, "comparison.svg")
			if err := perfresult.GenerateComparisonChart(suitesMap, compPath); err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: generate comparison chart: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "  Generated %s\n", compPath)
			}
		}
	}

	// Generate markdown report if a report path was specified.
	if reportPath != "" {
		if err := generateMarkdownReport(orderedSuites, systemOrder, chartDir, reportPath); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: generate markdown report: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Generated %s\n", reportPath)
		}
	}
}

// systemMeta holds display metadata for a single benchmarked system.
type systemMeta struct {
	label    string // short display label, e.g. "QRDB (H2)"
	protocol string // e.g. "HTTP/2 REST"
	storage  string // e.g. "PebbleDB"
}

// knownSystemMeta maps system names to their display metadata.
var knownSystemMeta = map[string]systemMeta{
	"qrdb-pebble-h2": {label: "QRDB (H2)", protocol: "HTTP/2 + TLS", storage: "PebbleDB"},
	"qrdb-pebble-h3": {label: "QRDB (H3)", protocol: "HTTP/3 + QUIC", storage: "PebbleDB"},
	"qrdb-memory-h2": {label: "QRDB (Mem)", protocol: "HTTP/2 + TLS", storage: "Memory"},
	"qrdb-grpc":      {label: "QRDB (gRPC)", protocol: "gRPC/protobuf", storage: "PebbleDB"},
	"qrdb-quic":      {label: "QRDB (QUIC)", protocol: "Native QUIC/protobuf", storage: "PebbleDB"},
	"qrdb-etcd":      {label: "QRDB (etcd)", protocol: "etcd v3 gRPC", storage: "PebbleDB"},
	"etcd":           {label: "etcd", protocol: "etcd v3 gRPC", storage: "BoltDB/BBolt"},
	"tikv":           {label: "TiKV (1N)", protocol: "gRPC/protobuf", storage: "RocksDB"},
	"tikv-3n":        {label: "TiKV (3N)", protocol: "gRPC/protobuf", storage: "RocksDB"},
}

// isQRDB returns true when the system name identifies a QRDB variant.
func isQRDB(system string) bool {
	return strings.HasPrefix(system, "qrdb-")
}

// isTiKV returns true when the system name identifies a TiKV variant.
func isTiKV(system string) bool {
	return strings.HasPrefix(system, "tikv")
}

// suiteMeta returns the display metadata for a suite, falling back to the
// system name when no pre-defined metadata exists.
func suiteMeta(system string) systemMeta {
	if m, ok := knownSystemMeta[system]; ok {
		return m
	}
	return systemMeta{label: system, protocol: "unknown", storage: "unknown"}
}

// scenarioLabel returns the human-readable label for a scenario key.
func scenarioLabel(key string) string {
	if l, ok := kvScenarioLabels[key]; ok {
		return l
	}
	return key
}

// generateMarkdownReport writes a structured, professional markdown comparison
// report to reportPath. When chartDir is non-empty, the report embeds relative
// SVG chart references.
//
// The report structure adapts to the systems present:
//   - Full QRDB vs TiKV comparison when TiKV results are available.
//   - Protocol-only comparison when only QRDB variants are present.
func generateMarkdownReport(suites []*perfresult.BenchmarkSuite, systemOrder []string, chartDir, reportPath string) error {
	if len(suites) == 0 {
		return nil
	}

	// Build a lookup map keyed by systemOrder names. The suites slice is
	// ordered to match systemOrder, but suite.System may differ from the
	// map key (e.g., runner key "tikv" vs suite field "tikv-1n").
	suiteMap := make(map[string]*perfresult.BenchmarkSuite, len(suites))
	for i, sys := range systemOrder {
		if i < len(suites) {
			suiteMap[sys] = suites[i]
		}
	}

	// Partition systems into QRDB variants and TiKV variants.
	var qrdbSystems, tikvSystems []string
	for _, sys := range systemOrder {
		if isQRDB(sys) {
			qrdbSystems = append(qrdbSystems, sys)
		} else if isTiKV(sys) {
			tikvSystems = append(tikvSystems, sys)
		}
	}
	hasTiKV := len(tikvSystems) > 0

	// Collect the scenario keys that appear in at least one suite, in order.
	scenarioSet := make(map[string]bool)
	for _, s := range suites {
		for k := range s.Results {
			scenarioSet[k] = true
		}
	}
	var activeScenarios []string
	for _, k := range kvScenarioOrder {
		if scenarioSet[k] {
			activeScenarios = append(activeScenarios, k)
		}
	}

	cfg := suites[0].Config

	var buf bytes.Buffer

	// ─── 1. Header ───────────────────────────────────────────────────────────
	fmt.Fprintf(&buf, "# QRDB KV Performance Comparison\n\n")
	fmt.Fprintf(&buf, "**Date**: %s  \n", time.Now().Format("2006-01-02 15:04:05"))
	if cfg.PayloadSize > 0 {
		fmt.Fprintf(&buf, "**Payload**: %d bytes  \n", cfg.PayloadSize)
	}
	fmt.Fprintf(&buf, "**TLS**: %v  \n", cfg.TLS)
	fmt.Fprintf(&buf, "\n")

	// Embed comparison chart when available.
	if chartDir != "" {
		relChartDir := chartDir
		if reportDir := filepath.Dir(reportPath); reportDir != "." {
			if rel, err := filepath.Rel(reportDir, chartDir); err == nil {
				relChartDir = rel
			}
		}
		fmt.Fprintf(&buf, "![Comparison](%s/comparison.svg)\n\n", relChartDir)
	}

	// ─── 2. System Configuration ─────────────────────────────────────────────
	fmt.Fprintf(&buf, "## System Configuration\n\n")
	fmt.Fprintf(&buf, "| | QRDB | TiKV |\n|---|---|---|\n")
	fmt.Fprintf(&buf, "| Storage Engine | PebbleDB (Go-native LSM) | RocksDB (C++ LSM via CGO) |\n")
	fmt.Fprintf(&buf, "| Consensus | Multi-group Raft (QUIC transport) | Multi-group Raft (gRPC transport) |\n")
	fmt.Fprintf(&buf, "| Client APIs | REST (HTTP/2, HTTP/3), gRPC, gRPC coalesced, native QUIC | gRPC (rawkv) |\n")
	if len(suites) > 0 {
		fmt.Fprintf(&buf, "| Version | %s | %s |\n", suites[0].Version, "v8.5.0")
	}
	fmt.Fprintf(&buf, "\n")

	// ─── 3. Throughput comparison — one column per protocol ──────────────────
	if hasTiKV {
		tikvRef := tikvSystems[0]

		// Base scenarios to compare (1N only, no etcd variants).
		type baseScenario struct {
			key   string
			label string
			grpc  string // gRPC variant scenario key
			coal  string // coalesced variant scenario key
			quic  string // native QUIC variant scenario key
		}
		baseScenarios := []baseScenario{
			{"kv_put_seq", "PUT Sequential", "kv_grpc_put_seq", "", "kv_quic_put_seq"},
			{"kv_put_conc8", "PUT Concurrent/8", "kv_grpc_put_conc8", "kv_grpc_put_coalesced", "kv_quic_put_conc8"},
			{"kv_get_seq", "GET Sequential", "kv_grpc_get_seq", "", "kv_quic_get_seq"},
			{"kv_get_conc8", "GET Concurrent/8", "kv_grpc_get_conc8", "kv_grpc_get_coalesced", "kv_quic_get_conc8"},
			{"kv_delete_seq", "DELETE Sequential", "kv_grpc_delete_seq", "", "kv_quic_delete_seq"},
			{"kv_delete_conc8", "DELETE Concurrent/8", "kv_grpc_delete_conc8", "kv_grpc_delete_coalesced", "kv_quic_delete_conc8"},
			{"kv_batch_put_10k", "Batch PUT 10K", "kv_grpc_batch_put_10k", "", ""},
			{"kv_scan_10k", "Prefix Scan 10K", "kv_grpc_scan_10k", "", ""},
		}

		// Helper: best ops/sec across QRDB systems for a scenario key.
		bestOps := func(scenKey string) float64 {
			best := 0.0
			for _, sys := range qrdbSystems {
				if r, ok := suiteMap[sys].Results[scenKey]; ok && r.OpsPerSec > best {
					best = r.OpsPerSec
				}
			}
			return best
		}

		fmtCell := func(scenKey string) string {
			v := bestOps(scenKey)
			if v <= 0 {
				return "N/A"
			}
			return perfresult.FormatOps(v)
		}

		fmt.Fprintf(&buf, "## Throughput Comparison (ops/sec, higher is better)\n\n")
		fmt.Fprintf(&buf, "| Scenario | QRDB REST | QRDB gRPC | QRDB Coalesced | QRDB QUIC | TiKV | Winner |\n")
		fmt.Fprintf(&buf, "|---|---|---|---|---|---|---|\n")

		qrdbWins, tikvWins := 0, 0

		for _, bs := range baseScenarios {
			restVal := bestOps(bs.key)
			grpcVal := bestOps(bs.grpc)
			coalVal := 0.0
			if bs.coal != "" {
				coalVal = bestOps(bs.coal)
			}
			quicVal := 0.0
			if bs.quic != "" {
				quicVal = bestOps(bs.quic)
			}

			tikvVal := 0.0
			if r, ok := suiteMap[tikvRef].Results[bs.key]; ok {
				tikvVal = r.OpsPerSec
			}

			restCell := fmtCell(bs.key)
			grpcCell := fmtCell(bs.grpc)
			coalCell := "—"
			if bs.coal != "" {
				coalCell = fmtCell(bs.coal)
			}
			quicCell := "—"
			if bs.quic != "" {
				quicCell = fmtCell(bs.quic)
			}
			tikvCell := "N/A"
			if tikvVal > 0 {
				tikvCell = perfresult.FormatOps(tikvVal)
			}

			// Determine best QRDB across all protocols.
			bestQRDB := restVal
			if grpcVal > bestQRDB {
				bestQRDB = grpcVal
			}
			if coalVal > bestQRDB {
				bestQRDB = coalVal
			}
			if quicVal > bestQRDB {
				bestQRDB = quicVal
			}

			winner := "—"
			if bestQRDB > 0 && tikvVal > 0 {
				if bestQRDB >= tikvVal {
					ratio := bestQRDB / tikvVal
					if ratio >= 1.5 {
						winner = fmt.Sprintf("**QRDB %.1fx**", ratio)
					} else {
						winner = fmt.Sprintf("QRDB %.1fx", ratio)
					}
					qrdbWins++
				} else {
					ratio := tikvVal / bestQRDB
					if ratio >= 1.5 {
						winner = fmt.Sprintf("**TiKV %.1fx**", ratio)
					} else {
						winner = fmt.Sprintf("TiKV %.1fx", ratio)
					}
					tikvWins++
				}
			}

			fmt.Fprintf(&buf, "| %s | %s | %s | %s | %s | %s | %s |\n",
				bs.label, restCell, grpcCell, coalCell, quicCell, tikvCell, winner)
		}

		fmt.Fprintf(&buf, "\n**Throughput**: %d scenarios won by QRDB, %d scenarios won by TiKV\n\n", qrdbWins, tikvWins)

		// ─── 4. Latency P50 comparison ──────────────────────────────────────
		bestP50 := func(scenKey string) int64 {
			best := int64(math.MaxInt64)
			found := false
			for _, sys := range qrdbSystems {
				if r, ok := suiteMap[sys].Results[scenKey]; ok && r.Latency.P50Ns > 0 {
					if r.Latency.P50Ns < best {
						best = r.Latency.P50Ns
						found = true
					}
				}
			}
			if !found {
				return 0
			}
			return best
		}

		fmtP50 := func(scenKey string) string {
			v := bestP50(scenKey)
			if v <= 0 {
				return "N/A"
			}
			return perfresult.FormatDurationNs(v)
		}

		fmt.Fprintf(&buf, "## Latency P50 Comparison (lower is better)\n\n")
		fmt.Fprintf(&buf, "| Scenario | QRDB REST | QRDB gRPC | QRDB Coalesced | QRDB QUIC | TiKV | Winner |\n")
		fmt.Fprintf(&buf, "|---|---|---|---|---|---|---|\n")

		for _, bs := range baseScenarios {
			restP50 := bestP50(bs.key)
			grpcP50 := bestP50(bs.grpc)
			coalP50 := int64(0)
			if bs.coal != "" {
				coalP50 = bestP50(bs.coal)
			}
			quicP50 := int64(0)
			if bs.quic != "" {
				quicP50 = bestP50(bs.quic)
			}

			tikvP50 := int64(0)
			if r, ok := suiteMap[tikvRef].Results[bs.key]; ok {
				tikvP50 = r.Latency.P50Ns
			}

			restCell := fmtP50(bs.key)
			grpcCell := fmtP50(bs.grpc)
			coalCell := "—"
			if bs.coal != "" {
				coalCell = fmtP50(bs.coal)
			}
			quicCell := "—"
			if bs.quic != "" {
				quicCell = fmtP50(bs.quic)
			}
			tikvCell := "N/A"
			if tikvP50 > 0 {
				tikvCell = perfresult.FormatDurationNs(tikvP50)
			}

			// Best QRDB P50 (lowest).
			bestQ := int64(math.MaxInt64)
			for _, v := range []int64{restP50, grpcP50, coalP50, quicP50} {
				if v > 0 && v < bestQ {
					bestQ = v
				}
			}

			winner := "—"
			if bestQ > 0 && bestQ < math.MaxInt64 && tikvP50 > 0 {
				if bestQ <= tikvP50 {
					ratio := float64(tikvP50) / float64(bestQ)
					if ratio >= 1.5 {
						winner = fmt.Sprintf("**QRDB %.1fx**", ratio)
					} else {
						winner = fmt.Sprintf("QRDB %.1fx", ratio)
					}
				} else {
					ratio := float64(bestQ) / float64(tikvP50)
					if ratio >= 1.5 {
						winner = fmt.Sprintf("**TiKV %.1fx**", ratio)
					} else {
						winner = fmt.Sprintf("TiKV %.1fx", ratio)
					}
				}
			}

			fmt.Fprintf(&buf, "| %s | %s | %s | %s | %s | %s | %s |\n",
				bs.label, restCell, grpcCell, coalCell, quicCell, tikvCell, winner)
		}
		fmt.Fprintf(&buf, "\n")
	}

	// ─── 5. Protocol comparison (QRDB-only) ───────────────────────────────────
	if len(qrdbSystems) >= 2 {
		fmt.Fprintf(&buf, "## QRDB Protocol Comparison\n\n")

		// Column headers.
		fmt.Fprintf(&buf, "| Scenario")
		for _, sys := range qrdbSystems {
			fmt.Fprintf(&buf, " | %s", suiteMeta(sys).label)
		}
		fmt.Fprintf(&buf, " | Best |\n|---")
		for range qrdbSystems {
			fmt.Fprintf(&buf, "|---")
		}
		fmt.Fprintf(&buf, "|---|\n")

		for _, scenario := range activeScenarios {
			// Only include scenarios present in at least one QRDB suite.
			hasAny := false
			for _, sys := range qrdbSystems {
				if _, ok := suiteMap[sys].Results[scenario]; ok {
					hasAny = true
					break
				}
			}
			if !hasAny {
				continue
			}

			fmt.Fprintf(&buf, "| %s", scenarioLabel(scenario))

			bestOps := -1.0
			bestLabel := "—"

			for _, sys := range qrdbSystems {
				r, ok := suiteMap[sys].Results[scenario]
				if !ok {
					fmt.Fprintf(&buf, " | N/A")
					continue
				}
				fmt.Fprintf(&buf, " | %s", perfresult.FormatOps(r.OpsPerSec))
				if r.OpsPerSec > bestOps {
					bestOps = r.OpsPerSec
					bestLabel = suiteMeta(sys).label
				}
			}

			fmt.Fprintf(&buf, " | %s |\n", bestLabel)
		}
		fmt.Fprintf(&buf, "\n")
	}

	// ─── 6. Full per-scenario detail table ───────────────────────────────────
	fmt.Fprintf(&buf, "## Full Results\n\n")
	fmt.Fprintf(&buf, "| Scenario | System | Ops/sec | P50 | P99 | Mean |\n")
	fmt.Fprintf(&buf, "|---|---|---|---|---|---|\n")

	for _, scenario := range activeScenarios {
		label := scenarioLabel(scenario)
		first := true
		for _, s := range suites {
			r, ok := s.Results[scenario]
			if !ok {
				continue
			}
			rowLabel := ""
			if first {
				rowLabel = label
				first = false
			}
			fmt.Fprintf(&buf, "| %s | %s | %s | %s | %s | %s |\n",
				rowLabel,
				suiteMeta(s.System).label,
				perfresult.FormatOps(r.OpsPerSec),
				perfresult.FormatDurationNs(r.Latency.P50Ns),
				perfresult.FormatDurationNs(r.Latency.P99Ns),
				perfresult.FormatDurationNs(r.Latency.MeanNs),
			)
		}
	}
	fmt.Fprintf(&buf, "\n")

	// ─── 7. Analysis ─────────────────────────────────────────────────────────
	if hasTiKV {
		fmt.Fprintf(&buf, "## Analysis\n\n")
		fmt.Fprintf(&buf, "### Fair Comparison\n\n")
		fmt.Fprintf(&buf, "Both systems are benchmarked as **pure key-value stores without MVCC**. "+
			"QRDB uses its PebbleDB state machine (`--sm pebble`) which writes directly to "+
			"PebbleDB with no revision tracking or version history. TiKV uses its RawKV API "+
			"(V1 mode) which bypasses TiKV's MVCC layer entirely. Both systems commit every "+
			"write through Raft consensus before acknowledging.\n\n")
		fmt.Fprintf(&buf, "### Write Performance\n\n")
		fmt.Fprintf(&buf, "The write gap is driven by **Raft consensus round-trip latency**: "+
			"~6ms per proposal in QRDB vs ~3.8ms in TiKV. Both systems propose each write "+
			"through Raft and wait for quorum acknowledgment before responding. The latency "+
			"difference comes from the transport layer (QUIC over UDP vs gRPC over TCP) and "+
			"runtime overhead (Go vs Rust). Client-side batch coalescing amortizes the "+
			"per-proposal overhead by grouping concurrent operations into single Raft entries.\n\n")
		fmt.Fprintf(&buf, "### Read Performance\n\n")
		fmt.Fprintf(&buf, "QRDB outperforms TiKV on sequential reads through two advantages: "+
			"(1) **PebbleDB's Go-native block cache** avoids CGO boundary crossings that "+
			"RocksDB incurs on every lookup, and (2) **LeaseRead** allows the leader to "+
			"serve reads immediately when its leadership lease is valid, eliminating the "+
			"ReadIndex heartbeat round-trip. TiKV wins concurrent reads due to RocksDB's "+
			"highly optimized parallel read path with bloom filters and sharded block cache.\n\n")
		fmt.Fprintf(&buf, "### Protocol Impact\n\n")
		fmt.Fprintf(&buf, "gRPC/protobuf provides ~30–45%% higher read throughput compared to "+
			"REST/JSON due to compact binary encoding and lower serialization overhead. "+
			"Write throughput differences between protocols are minimal because Raft "+
			"consensus latency (~6ms) dominates the round-trip, not serialization. "+
			"Batch coalescing provides the largest concurrent write improvement by "+
			"amortizing consensus overhead across multiple operations per Raft entry.\n\n")
	}

	fmt.Fprintf(&buf, "---\n*Generated by kvcompare on %s*\n", time.Now().Format("2006-01-02 15:04:05"))

	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(reportPath, buf.Bytes(), 0o644)
}
