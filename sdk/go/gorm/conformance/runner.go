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
	Test      string    `json:"test"`
	Targets   []string  `json:"targets"`
	Reason    string    `json:"reason"`
	Issue     string    `json:"issue"`
	Retire    time.Time `json:"retire"`
	AbortsRun bool      `json:"aborts_run"`
	Unstable  bool      `json:"unstable"`
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

	declared, err := listTests(*target)
	if err != nil {
		fatalf("list tests: %v", err)
	}

	outcomes, _, err := runTests(*target, *timeout, abortingTests(entries))
	if err != nil {
		fatalf("run tests: %v", err)
	}

	report := reconcile(*target, entries, outcomes, declared, budget.MaxExpected)

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

// listTests returns every top-level Test the suite declares, by asking the
// test binary rather than inferring it from a run.
//
// It is what makes the difference between "this entry names a test that no
// longer exists" and "this entry names a test the run never reached"
// observable at all. Without it, a run truncated by a panic reports every
// test after the panic as a missing entry, which reads as 35 stale entries
// when the truth is one aborted process.
func listTests(target string) (map[string]struct{}, error) {
	cmd := exec.Command("go", "test", "-tags", "gormconformance", "-list", ".*", "./...")
	cmd.Env = append(os.Environ(), targetEnvVar+"="+target)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go test -list: %w", err)
	}

	names := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") && !strings.Contains(line, " ") {
			names[line] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("go test -list reported no tests")
	}
	return names, nil
}

// abortingTests returns the names of the entries marked AbortsRun, which the
// runner excludes from the run with `go test -skip`. See the AbortsRun
// field's doc comment in qrdb_allowlist_test.go: one of these stops the test
// process from finishing, so leaving them in means measuring a prefix of the
// suite and reporting it as the whole.
func abortingTests(entries []expectedFailure) []string {
	var names []string
	for _, e := range entries {
		if e.AbortsRun {
			names = append(names, e.Test)
		}
	}
	sort.Strings(names)
	return names
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
func runTests(target, timeout string, exclude []string) (map[string]outcome, string, error) {
	args := []string{"test", "-tags", "gormconformance", "-json", "-count=1", "-timeout", timeout}
	if len(exclude) > 0 {
		args = append(args, "-skip", "^("+strings.Join(exclude, "|")+")$")
	}
	args = append(args, "./...")

	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), targetEnvVar+"="+target)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("StdoutPipe: %w", err)
	}
	cmd.Stderr = os.Stderr // qrdbd subprocess logs (wire targets) and go test's own diagnostics

	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start: %w", err)
	}

	// started holds tests that have begun but not yet reported a terminal
	// action. A panic kills the process mid-test, so anything left here at
	// the end names the test that aborted the run -- and every test after it
	// went unreported. Tracking it is what lets the reconciliation say the
	// run was TRUNCATED rather than silently presenting a partial result as
	// a complete one. It is derived from the events already being decoded,
	// so testEvent stays lean (see its doc comment).
	started := make(map[string]struct{})
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
		case "run":
			started[ev.Test] = struct{}{}
		case "pass", "fail", "skip":
			outcomes[ev.Test] = outcome{Test: ev.Test, Action: ev.Action}
			delete(started, ev.Test)
		}
	}
	if err := scanner.Err(); err != nil {
		cmd.Wait()
		return nil, "", fmt.Errorf("scan output: %w", err)
	}

	// go test itself exits non-zero whenever any test failed — expected
	// and routine for this suite today, not a runner error. Only a
	// process-level failure to even start/build is reported here; the
	// reconciliation below is what decides success or failure.
	_ = cmd.Wait()

	abortedOn := ""
	for name := range started {
		abortedOn = name // at most one: the process died inside it
	}

	return outcomes, abortedOn, nil
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
	Unstable       int       `json:"unstable_passed"`
	AbortSkipped   int       `json:"abort_skipped"`
	// Unmeasured names every declared test that produced no terminal outcome
	// and was not deliberately excluded. A non-empty list means the run ended
	// early, so every count above describes a prefix of the suite.
	Unmeasured     []string `json:"unmeasured,omitempty"`
	BudgetMax      int      `json:"budget_max"`
	BudgetUsed     int      `json:"budget_used"`
	BudgetExceeded bool     `json:"budget_exceeded"`

	UnexpectedFailures []string `json:"unexpected_failures,omitempty"`
	StalePasses        []string `json:"stale_passes,omitempty"`
	StaleNotObserved   []string `json:"stale_not_observed,omitempty"`
	ExpiredRetire      []string `json:"expired_retire,omitempty"`
}

func (r report) hasViolations() bool {
	// A truncated run is a violation in its own right. Its counts describe a
	// prefix of the suite, so passing it would be reporting coverage the run
	// did not have -- the failure mode the Panics field exists to prevent,
	// reappearing for a test not yet marked with it.
	return r.UnexpectedFail > 0 || r.StaleCount > 0 || len(r.ExpiredRetire) > 0 ||
		r.BudgetExceeded || len(r.Unmeasured) > 0
}

func reconcile(target string, entries []expectedFailure, outcomes map[string]outcome, declared map[string]struct{}, maxExpected int) report {
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

	// A test excluded from the run because it aborts the process can be
	// neither a stale pass nor a missing entry: it did not run. It is
	// counted and printed on its own line instead.
	skipped := make(map[string]struct{})
	for _, e := range entries {
		if e.AbortsRun {
			skipped[e.Test] = struct{}{}
		}
	}
	r.AbortSkipped = len(skipped)

	seen := make(map[string]struct{}, len(entries))
	for name, oc := range outcomes {
		r.Ran++
		switch oc.Action {
		case "skip":
			r.Skipped++
		case "pass":
			// An Unstable entry passing is not evidence that it is fixed --
			// see the field's doc comment -- so it is counted as a pass and
			// left in place rather than reported stale.
			if entry, ok := allowed[name]; ok && !entry.Unstable {
				r.StalePasses = append(r.StalePasses, name)
				seen[name] = struct{}{}
			} else {
				if ok {
					r.Unstable++
					seen[name] = struct{}{}
				}
				r.Passed++
			}
		case "fail":
			if _, isAllowed := allowed[name]; isAllowed {
				r.ExpectedFail++
				seen[name] = struct{}{}
			} else {
				r.UnexpectedFailures = append(r.UnexpectedFailures, name)
			}
		}
	}

	// A declared test with no terminal outcome, that was not deliberately
	// excluded, was never measured -- the run ended before reaching it.
	for name := range declared {
		if _, ok := outcomes[name]; ok {
			continue
		}
		if _, excluded := skipped[name]; excluded {
			continue
		}
		r.Unmeasured = append(r.Unmeasured, name)
	}

	now := time.Now()
	for _, e := range entries {
		if _, ok := seen[e.Test]; !ok && !e.AbortsRun {
			// "Not observed" now means exactly one thing: the suite does not
			// declare this test at all. A test that exists but went
			// unmeasured is reported as truncation instead, above.
			if _, exists := declared[e.Test]; !exists {
				r.StaleNotObserved = append(r.StaleNotObserved, e.Test)
			}
		}
		if !e.Retire.After(now) {
			r.ExpiredRetire = append(r.ExpiredRetire, fmt.Sprintf("%s (retired %s)", e.Test, e.Retire.Format("2006-01-02")))
		}
	}

	sort.Strings(r.Unmeasured)
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
	fmt.Fprintf(w, "\ngorm-conformance[%s]: ran=%d passed=%d expected-fail=%d unexpected-fail=%d skipped=%d abort-skipped=%d unstable-passed=%d stale=%d budget=%d/%d\n",
		r.Target, r.Ran, r.Passed, r.ExpectedFail, r.UnexpectedFail, r.Skipped, r.AbortSkipped, r.Unstable, r.StaleCount, r.BudgetUsed, r.BudgetMax)

	if len(r.Unmeasured) > 0 {
		fmt.Fprintf(w, "  RUN TRUNCATED: %d declared tests produced no result, so every count above\n", len(r.Unmeasured))
		fmt.Fprintf(w, "    describes a PREFIX of the suite. A test that aborts the process rather than\n")
		fmt.Fprintf(w, "    failing must be marked AbortsRun: true in qrdb_allowlist_test.go.\n")
		printList(w, "unmeasured", r.Unmeasured)
	}

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
