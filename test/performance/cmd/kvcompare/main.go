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
// kvcompare builds and runs Dockerized KV benchmark suites for QRDB
// and TiKV, then produces a side-by-side comparison report.
//
// Usage:
//
//	kvcompare [flags] [system...]
//
// Systems: qrdb-pebble-h2, qrdb-pebble-h3, qrdb-memory-h2, tikv, tikv-3n
// Aliases: qrdb (=qrdb-pebble-h2), all, fair, protocol, storage
//
// Flags:
//
//	--benchtime duration     Per-scenario duration (default 1s)
//	--timeout duration       Max timeout per system (default 600s)
//	--no-build               Skip Docker build step
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"
)

// knownSystems lists all recognized system names in preferred display order.
var knownSystems = []string{"qrdb-pebble-h2", "qrdb-pebble-h3", "qrdb-memory-h2", "tikv", "tikv-3n"}

// groupAliases maps shorthand names to lists of real system names.
var groupAliases = map[string][]string{
	"all":      {"qrdb-pebble-h2", "qrdb-pebble-h3", "qrdb-memory-h2", "tikv", "tikv-3n"},
	"fair":     {"qrdb-pebble-h2", "qrdb-pebble-h3", "tikv", "tikv-3n"},
	"protocol": {"qrdb-pebble-h2", "qrdb-pebble-h3"},
	"storage":  {"qrdb-pebble-h2", "qrdb-memory-h2"},
	"qrdb":     {"qrdb-pebble-h2"},
}

// kvScenarioLabels maps KV scenario keys to human-readable display names.
var kvScenarioLabels = map[string]string{
	"kv_put_seq":       "PUT Sequential (1N)",
	"kv_put_conc8":     "PUT Concurrent/8 (1N)",
	"kv_get_seq":       "GET Sequential (1N)",
	"kv_get_conc8":     "GET Concurrent/8 (1N)",
	"kv_delete_seq":    "DELETE Sequential (1N)",
	"kv_delete_conc8":  "DELETE Concurrent/8 (1N)",
	"kv_batch_put_10k": "Batch PUT 10K (1N)",
	"kv_scan_10k":      "Prefix Scan 10K (1N)",
	"kv_put_seq_3n":    "PUT Sequential (3N)",
	"kv_put_conc8_3n":  "PUT Concurrent/8 (3N)",
	"kv_get_seq_3n":    "GET Sequential (3N)",
	"kv_get_conc8_3n":  "GET Concurrent/8 (3N)",
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
}

// ErrInvalidSystem indicates an unrecognized system name on the command line.
type ErrInvalidSystem struct {
	System string
}

func (e *ErrInvalidSystem) Error() string {
	return "invalid system: " + e.System + " (valid: " + strings.Join(knownSystems, ", ") + "; aliases: qrdb, all, fair, protocol, storage)"
}

// ErrNoSystems indicates that no valid systems were specified.
type ErrNoSystems struct{}

func (e *ErrNoSystems) Error() string {
	return "no systems specified; provide one or more of: " + strings.Join(knownSystems, ", ") + " (aliases: qrdb, all, fair, protocol, storage)"
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

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kvcompare [flags] [system...]\n\n")
		fmt.Fprintf(os.Stderr, "Systems: %s\n", strings.Join(knownSystems, ", "))
		fmt.Fprintf(os.Stderr, "Aliases: qrdb, all, fair, protocol, storage\n\n")
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

	fmt.Fprintf(os.Stderr, "=== QRDB vs TiKV KV Benchmark ===\n")
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

	printResults(suites)

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

// printResults outputs a formatted comparison table to stdout.
func printResults(suites map[string]*perfresult.BenchmarkSuite) {
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
}
