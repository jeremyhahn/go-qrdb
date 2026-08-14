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

//go:build ignore

// Command runner drives one target of the GORM conformance suite and
// reconciles its results against qrdb_allowlist_test.go's ratchet table,
// exiting non-zero the moment that table stops being an honest description
// of reality.
//
// qrdb_allowlist_test.go's table is ordinary, compiled Go — the type system
// already guarantees it cannot rot into something unparseable. What it does
// NOT guarantee is that it stays TRUE: an entry can keep excusing a test
// that was fixed months ago, silently, forever, exactly like a stale
// t.Skip would. This program is what turns the table from a skip list into
// a ratchet, by rerunning it against `go test -json` reality every time and
// failing loudly on four kinds of drift:
//
//  1. A test not in the allowlist failed — an unexpected (real) failure.
//  2. A test in the allowlist for this target PASSED — the entry is now a
//     lie; the run fails until it is deleted from qrdb_allowlist_test.go.
//  3. An allowlist entry (for this target) is past its Retire date —
//     regardless of whether the test still fails. An entry is a promise to
//     come back and look, not a permanent exemption.
//  4. This target's entry count exceeds GORM_SUITE.lock's budgets.<target>.
//     maxexpected — the budget is edited downward only, so growth here can
//     only mean someone raised the ceiling instead of fixing tests, which
//     is exactly the code-review-visible diff the budget exists to force.
//
// A fifth, closely related check is also enforced though not explicitly one
// of the four above: an allowlist entry (for this target) whose Test name
// never appeared in the run at all (renamed, deleted upstream, or a typo)
// is reported and treated the same as a stale pass — an entry matching
// nothing is just as much a lie as one matching a test that now passes.
//
// Usage (see the Makefile's conformance-test-gorm-* targets):
//
//	go run runner.go -target=node
//	go run runner.go -target=mysql
//	go run runner.go -target=pgsql
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const targetEnvVar = "GORM_QRDB_TARGET"
const dumpAllowlistEnvVar = "GORM_QRDB_DUMP_ALLOWLIST"

// expectedFailure mirrors qrdb_allowlist_test.go's type of the same name.
// It cannot import that type directly — qrdb_allowlist_test.go lives in a
// _test.go file, which is invisible to any program outside `go test` — so
// this is deliberately kept in exact field-for-field sync with the JSON
// dumpAllowlistAndExit produces from the real, compiled table.
type expectedFailure struct {
	Test    string    `json:"test"`
	Targets []string  `json:"targets"`
	Reason  string    `json:"reason"`
	Issue   string    `json:"issue"`
	Retire  time.Time `json:"retire"`
}

// lockFile is the subset of GORM_SUITE.lock this program reads.
type lockFile struct {
	Budgets map[string]struct {
		MaxExpected int `json:"maxexpected"`
	} `json:"budgets"`
}

// testEvent is one line of `go test -json` output.
type testEvent struct {
	Action  string `json:"Action"`
	Test    string `json:"Test"`
	Package string `json:"Package"`
}

func main() {
	target := flag.String("target", "", "qrdb target: node, mysql, or pgsql")
	timeout := flag.String("timeout", "20m", "go test -timeout value for the reconciled run")
	flag.Parse()

	if *target != "node" && *target != "mysql" && *target != "pgsql" {
		fatalf("-target must be one of node, mysql, pgsql (got %q)", *target)
	}

	moduleDir, err := os.Getwd()
	if err != nil {
		fatalf("Getwd: %v", err)
	}

	lock, err := loadLock(filepath.Join(moduleDir, "GORM_SUITE.lock"))
	if err != nil {
		fatalf("load GORM_SUITE.lock: %v", err)
	}
	budget, ok := lock.Budgets[*target]
	if !ok {
		fatalf("GORM_SUITE.lock: budgets has no entry for target %q", *target)
	}

	allEntries, err := dumpAllowlist()
	if err != nil {
		fatalf("dump allowlist: %v", err)
	}
	entries := forTarget(allEntries, *target)

	outcomes, err := runTests(*target, *timeout)
	if err != nil {
		fatalf("run tests: %v", err)
	}

	report := reconcile(*target, entries, outcomes, budget.MaxExpected)

	if err := writeSummary(moduleDir, report); err != nil {
		fatalf("write summary: %v", err)
	}

	printSummary(report)

	if report.hasViolations() {
		os.Exit(1)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "runner: FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func loadLock(path string) (*lockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &lock, nil
}

// dumpAllowlist execs the gormconformance test binary with
// GORM_QRDB_DUMP_ALLOWLIST=1 and parses the JSON array it prints — the
// compiled table in qrdb_allowlist_test.go, read out of the one process
// that can actually see it. See TestMain's dumpAllowlistAndExit.
//
// It runs with `go test -json` (not plain `go test`) specifically so the
// test binary's raw stdout is guaranteed to reach this process: without
// -json, `go test` only echoes a passing test binary's stdout in verbose
// mode, and dumpAllowlistAndExit exits 0 (a "pass") before any Test*
// function — let alone -v's "=== RUN" machinery — ever runs.
func dumpAllowlist() ([]expectedFailure, error) {
	cmd := exec.Command("go", "test", "-tags", "gormconformance", "-json", "-count=1", "./...")
	cmd.Env = append(os.Environ(), dumpAllowlistEnvVar+"=1")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go test (dump allowlist): %w\n%s", err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("go test (dump allowlist): %w", err)
	}

	// Every "output" event's Output field is a raw line the test binary
	// wrote to its own stdout; TestMain writes nothing else before
	// dumpAllowlistAndExit's single json.Encoder.Encode call, so
	// concatenating them reconstructs exactly that one JSON array.
	var raw strings.Builder
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var ev testEvent2
		if err := dec.Decode(&ev); err != nil {
			return nil, fmt.Errorf("decode go test -json output: %w", err)
		}
		if ev.Action == "output" {
			raw.WriteString(ev.Output)
		}
	}

	// json.NewDecoder (not json.Unmarshal) deliberately: `go test`'s own
	// test2json wrapper appends a trailing "output" event of its own
	// (the "PASS\nok  \tpkg\t0.01s\n" summary line) after the test
	// binary's real output, and a Decoder stops at the first complete
	// JSON value instead of demanding the whole buffer be exactly one.
	var entries []expectedFailure
	if err := json.NewDecoder(strings.NewReader(raw.String())).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse allowlist JSON: %w\nraw output:\n%s", err, out)
	}
	return entries, nil
}

// testEvent2 is testEvent plus the Output field, used only by dumpAllowlist
// (runTests never needs Output, so testEvent stays lean for the much larger
// volume of events it decodes).
type testEvent2 struct {
	Action string `json:"Action"`
	Output string `json:"Output"`
}

// forTarget returns the entries whose Targets include target.
func forTarget(entries []expectedFailure, target string) []expectedFailure {
	var out []expectedFailure
	for _, e := range entries {
		for _, t := range e.Targets {
			if t == target {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// outcome is the terminal action (pass/fail/skip) most recently observed
// for a single go test -json Test name.
type outcome struct {
	Test   string
	Action string // "pass", "fail", or "skip"
}

// runTests execs `go test -tags gormconformance -json` with
// GORM_QRDB_TARGET=target and streams its output, returning the terminal
// outcome of every TOP-LEVEL Test (no "/" in the name) it reported. Output
// is streamed rather than buffered whole so a hung test still surfaces
// partial progress if the run is killed externally.
func runTests(target, timeout string) (map[string]outcome, error) {
	cmd := exec.Command("go", "test", "-tags", "gormconformance", "-json", "-count=1", "-timeout", timeout, "./...")
	cmd.Env = append(os.Environ(), targetEnvVar+"="+target)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("StdoutPipe: %w", err)
	}
	cmd.Stderr = os.Stderr // qrdbd subprocess logs (wire targets) and go test's own diagnostics

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	outcomes := make(map[string]outcome)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // a few upstream tests log large SQL statements
	for scanner.Scan() {
		var ev testEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // a non-JSON line (should not happen with -json, but never fatal here)
		}
		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue // top-level only — see runTests' doc comment
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			outcomes[ev.Test] = outcome{Test: ev.Test, Action: ev.Action}
		}
	}
	if err := scanner.Err(); err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("scan output: %w", err)
	}

	// go test itself exits non-zero whenever any test failed — expected
	// and routine for this suite today, not a runner error. Only a
	// process-level failure to even start/build is reported here; the
	// reconciliation below is what decides success or failure.
	_ = cmd.Wait()

	return outcomes, nil
}

// report is the reconciliation result for one target.
type report struct {
	Target         string    `json:"target"`
	GeneratedAt    time.Time `json:"generated_at"`
	Ran            int       `json:"ran"`
	Passed         int       `json:"passed"`
	ExpectedFail   int       `json:"expected_fail"`
	UnexpectedFail int       `json:"unexpected_fail"`
	Skipped        int       `json:"skipped"`
	StaleCount     int       `json:"stale"`
	BudgetMax      int       `json:"budget_max"`
	BudgetUsed     int       `json:"budget_used"`
	BudgetExceeded bool      `json:"budget_exceeded"`

	UnexpectedFailures []string `json:"unexpected_failures,omitempty"`
	StalePasses        []string `json:"stale_passes,omitempty"`
	StaleNotObserved   []string `json:"stale_not_observed,omitempty"`
	ExpiredRetire      []string `json:"expired_retire,omitempty"`
}

func (r report) hasViolations() bool {
	return r.UnexpectedFail > 0 || r.StaleCount > 0 || len(r.ExpiredRetire) > 0 || r.BudgetExceeded
}

func reconcile(target string, entries []expectedFailure, outcomes map[string]outcome, maxExpected int) report {
	allowed := make(map[string]expectedFailure, len(entries))
	for _, e := range entries {
		allowed[e.Test] = e
	}

	r := report{
		Target:      target,
		GeneratedAt: time.Now().UTC(),
		BudgetMax:   maxExpected,
		BudgetUsed:  len(entries),
	}
	r.BudgetExceeded = len(entries) > maxExpected

	seen := make(map[string]struct{}, len(entries))
	for name, oc := range outcomes {
		r.Ran++
		_, isAllowed := allowed[name]
		switch oc.Action {
		case "skip":
			r.Skipped++
		case "pass":
			if isAllowed {
				r.StalePasses = append(r.StalePasses, name)
				seen[name] = struct{}{}
			} else {
				r.Passed++
			}
		case "fail":
			if isAllowed {
				r.ExpectedFail++
				seen[name] = struct{}{}
			} else {
				r.UnexpectedFailures = append(r.UnexpectedFailures, name)
			}
		}
	}

	now := time.Now()
	for _, e := range entries {
		if _, ok := seen[e.Test]; !ok {
			if _, ranAtAll := outcomes[e.Test]; !ranAtAll {
				r.StaleNotObserved = append(r.StaleNotObserved, e.Test)
			}
		}
		if !e.Retire.After(now) {
			r.ExpiredRetire = append(r.ExpiredRetire, fmt.Sprintf("%s (retired %s)", e.Test, e.Retire.Format("2006-01-02")))
		}
	}

	sort.Strings(r.UnexpectedFailures)
	sort.Strings(r.StalePasses)
	sort.Strings(r.StaleNotObserved)
	sort.Strings(r.ExpiredRetire)

	r.UnexpectedFail = len(r.UnexpectedFailures)
	r.StaleCount = len(r.StalePasses) + len(r.StaleNotObserved)

	return r
}

func writeSummary(moduleDir string, r report) error {
	dir := filepath.Join(moduleDir, "coverage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("gorm-conformance-%s.json", r.Target))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func printSummary(r report) {
	w := os.Stdout
	fmt.Fprintf(w, "\ngorm-conformance[%s]: ran=%d passed=%d expected-fail=%d unexpected-fail=%d skipped=%d stale=%d budget=%d/%d\n",
		r.Target, r.Ran, r.Passed, r.ExpectedFail, r.UnexpectedFail, r.Skipped, r.StaleCount, r.BudgetUsed, r.BudgetMax)

	printList(w, "unexpected failures (not in allowlist)", r.UnexpectedFailures)
	printList(w, "stale allowlist entries now PASSING (remove from qrdb_allowlist_test.go)", r.StalePasses)
	printList(w, "stale allowlist entries matching no observed test (renamed/removed upstream?)", r.StaleNotObserved)
	printList(w, "allowlist entries past their Retire date", r.ExpiredRetire)

	if r.BudgetExceeded {
		fmt.Fprintf(w, "  budget exceeded: %d allowlist entries for %s, GORM_SUITE.lock allows %d\n", r.BudgetUsed, r.Target, r.BudgetMax)
	}

	if r.hasViolations() {
		fmt.Fprintln(w, "\ngorm-conformance: RATCHET VIOLATION — see above")
	} else {
		fmt.Fprintln(w, "\ngorm-conformance: ratchet holds (this does not mean the suite is green — see expected-fail count)")
	}
}

func printList(w io.Writer, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s (%d):\n", label, len(items))
	for _, item := range items {
		fmt.Fprintf(w, "    - %s\n", item)
	}
}
