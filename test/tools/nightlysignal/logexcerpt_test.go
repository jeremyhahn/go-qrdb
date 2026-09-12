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

package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// The shapes below are transcribed from the real logs of nightly run 16597 —
// the run whose grpc advisory reached nobody — and of the fips job that has
// been red since 2026-08-22. Synthesising them would test the patterns against
// the author's memory of the format rather than against the format.
const trivyRedLog = `2026-09-11T03:01:18.6967972Z 2026-09-11T03:01:18Z	INFO	[vulndb] Need to update DB
2026-09-11T03:01:33.8421685Z Report Summary
2026-09-11T03:01:33.8425795Z go.mod (gomod)
2026-09-11T03:01:33.8425997Z Total: 1 (HIGH: 1, CRITICAL: 0)
2026-09-11T03:01:33.8427052Z │ google.golang.org/grpc │ CVE-2026-84445 │ HIGH     │ affected │ v1.83.1           │ 1.83.2 │ grpc-go: denial of service │
2026-09-11T03:01:33.8428000Z sdk/go/go.mod (gomod)
2026-09-11T03:01:33.8428100Z Total: 1 (HIGH: 1, CRITICAL: 0)
2026-09-11T03:01:33.8428200Z │ google.golang.org/grpc │ CVE-2026-84445 │ HIGH     │ affected │ v1.83.1           │ 1.83.2 │ grpc-go: denial of service │
2026-09-11T03:01:34.0000000Z ##[error]Process completed with exit code 1.
`

const fipsRedLog = `2026-09-11T03:20:00.0000000Z === RUN   TestNoiseBootstrap
2026-09-11T03:20:01.0000000Z     noise_test.go:44: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode
2026-09-11T03:20:01.0000001Z --- FAIL: TestNoiseBootstrap (1.00s)
2026-09-11T03:20:02.0000000Z FAIL	github.com/jeremyhahn/go-qrdb/pkg/clusterenroll	1.204s
2026-09-11T03:20:03.0000000Z ok  	github.com/jeremyhahn/go-qrdb/pkg/seal	31.002s
`

func TestExtractFindingsPullsTheAdvisoryOutOfATrivyLog(t *testing.T) {
	lines, matched, scanned := ExtractFindings(trivyRedLog)

	if scanned < 9 {
		t.Fatalf("scanned = %d, want every line of the log", scanned)
	}
	joined := strings.Join(lines, "\n")
	mustContain(t, joined, "CVE-2026-84445")
	mustContain(t, joined, "Total: 1 (HIGH: 1, CRITICAL: 0)")
	mustContain(t, joined, "google.golang.org/grpc")

	// The INFO and "Report Summary" lines are not findings and must not be
	// carried: an excerpt that includes everything is a log, not an excerpt.
	if strings.Contains(joined, "Need to update DB") || strings.Contains(joined, "Report Summary") {
		t.Errorf("excerpt carried non-finding lines:\n%s", joined)
	}

	// The advisory line appears twice in the log (go.mod and sdk/go/go.mod) and
	// once in the excerpt: a duplicate says nothing the first did not, and two
	// of them crowd out a different finding under the cap.
	if matched <= len(lines) {
		t.Errorf("matched = %d, lines = %d: the duplicate was not counted", matched, len(lines))
	}
	if countOccurrences(joined, "CVE-2026-84445") != 1 {
		t.Errorf("the duplicated advisory line was not deduplicated:\n%s", joined)
	}
}

func TestExtractFindingsPullsTheFailingTestAndItsCause(t *testing.T) {
	lines, _, _ := ExtractFindings(fipsRedLog)
	joined := strings.Join(lines, "\n")

	mustContain(t, joined, "--- FAIL: TestNoiseBootstrap")
	mustContain(t, joined, "FAIL\tgithub.com/jeremyhahn/go-qrdb/pkg/clusterenroll")
	// The CAUSE line, which carries no FAIL at all: without the `FIPS 140`
	// pattern the issue would name the test and not the reason.
	mustContain(t, joined, "not allowed in FIPS 140-only mode")

	// A passing package is not a finding.
	if strings.Contains(joined, "pkg/seal") {
		t.Errorf("a passing package was reported as a finding:\n%s", joined)
	}
}

// The anchored patterns are applied AFTER the runner's timestamp prefix is
// stripped. Without that step `^--- FAIL:` matches nothing and the excerpt
// comes back empty on exactly the logs it exists for — silently.
func TestExtractFindingsStripsTheRunnerTimestamp(t *testing.T) {
	lines, _, _ := ExtractFindings("2026-09-11T03:20:01.0000001Z --- FAIL: TestX (1.00s)\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want the one failure", lines)
	}
	if !strings.HasPrefix(lines[0], "--- FAIL: TestX") {
		t.Errorf("timestamp not stripped: %q", lines[0])
	}
}

func TestExtractFindingsStripsColourEscapes(t *testing.T) {
	lines, _, _ := ExtractFindings("2026-09-11T03:20:01.0Z \x1b[31m--- FAIL: TestX\x1b[0m (1.00s)\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one", lines)
	}
	if strings.Contains(lines[0], "\x1b") {
		t.Errorf("escape survived: %q", lines[0])
	}
}

func TestExtractFindingsCapsLinesAndColumns(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxExcerptLines*3; i++ {
		b.WriteString("--- FAIL: Test" + strconv.Itoa(i) + " (0.01s)\n")
	}
	b.WriteString("--- FAIL: TestWide " + strings.Repeat("x", maxExcerptColumn*2) + "\n")

	lines, matched, _ := ExtractFindings(b.String())
	if len(lines) != maxExcerptLines {
		t.Errorf("lines = %d, want the cap %d", len(lines), maxExcerptLines)
	}
	if matched != maxExcerptLines*3+1 {
		t.Errorf("matched = %d, want every matching line counted even past the cap", matched)
	}
	for _, line := range lines {
		if len(line) > maxExcerptColumn+len(" …") {
			t.Errorf("line longer than the column cap: %d bytes", len(line))
		}
	}
}

func TestExtractFindingsOnAnEmptyLog(t *testing.T) {
	lines, matched, scanned := ExtractFindings("")
	if len(lines) != 0 || matched != 0 {
		t.Errorf("lines=%v matched=%d, want nothing", lines, matched)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want the single empty line", scanned)
	}
}

func TestFailedStepsNamesOnlyTheFailures(t *testing.T) {
	steps := []Step{
		{Name: "Run actions/checkout", Conclusion: resultSuccess},
		{Name: "Trivy gate (fail on CRITICAL/HIGH)", Conclusion: resultFailure},
		{Name: "Upload Trivy SARIF", Conclusion: resultSkipped},
	}
	got := FailedSteps(steps)
	if len(got) != 1 || got[0] != "Trivy gate (fail on CRITICAL/HIGH)" {
		t.Errorf("FailedSteps = %v, want the one failing step", got)
	}
	if FailedSteps(nil) != nil {
		t.Error("FailedSteps(nil) should be nil")
	}
}

// Four outcomes, four different renderings. Collapsing any of them into silence
// is how a notification comes to look like a quiet night — which is the defect
// this whole tool is about.
func TestExcerptRenderSaysWhichOfTheFourItIs(t *testing.T) {
	cases := []struct {
		name    string
		excerpt Excerpt
		want    string
		absent  string
	}{
		{
			name:    "log unreadable",
			excerpt: Excerpt{Err: errors.New("HTTP 404"), Scanned: 0},
			want:    "could not be read",
			absent:  "No line of the",
		},
		{
			name:    "log empty",
			excerpt: Excerpt{Scanned: 0},
			want:    "job log is empty",
		},
		{
			name:    "nothing matched",
			excerpt: Excerpt{Scanned: 900, Matched: 0},
			want:    "No line of the 900-line log matched",
		},
		{
			name:    "findings",
			excerpt: Excerpt{Scanned: 900, Matched: 3, Lines: []string{"--- FAIL: TestX"}},
			want:    "1 of 3 matching line(s), from a 900-line log",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.excerpt.Render()
			mustContain(t, out, tc.want)
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("rendering contains %q, which belongs to a different outcome:\n%s", tc.absent, out)
			}
		})
	}
}

// An unreadable log must say it is a defect in the signal, not report an empty
// findings section that reads as "the job failed for no reason".
func TestExcerptRenderCallsAnUnreadableLogADefect(t *testing.T) {
	out := Excerpt{Err: errors.New("HTTP 500")}.Render()
	mustContain(t, out, "defect in this signal, not an absence of findings")
	mustContain(t, out, "HTTP 500")
}

func TestExcerptRenderNamesTheFailingSteps(t *testing.T) {
	out := Excerpt{
		FailedSteps: []string{"Trivy gate (fail on CRITICAL/HIGH)", "Install Trivy CLI"},
		Scanned:     10, Matched: 1, Lines: []string{"--- FAIL: TestX"},
	}.Render()
	mustContain(t, out, "Failing step(s): `Trivy gate (fail on CRITICAL/HIGH)`, `Install Trivy CLI`")
}

// A log line carrying a backtick fence must not end the code block early and
// let the rest of the excerpt render as prose.
func TestExcerptRenderSurvivesBacktickFencesInTheLog(t *testing.T) {
	out := Excerpt{
		Scanned: 2, Matched: 1,
		Lines: []string{"--- FAIL: TestX ```` and ``` inside"},
	}.Render()
	if strings.Count(out, "````") != 2 {
		t.Errorf("the four-backtick fence is not balanced:\n%s", out)
	}
	mustContain(t, out, "``` and ``` inside")
}

func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}
