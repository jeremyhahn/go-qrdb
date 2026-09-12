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
	"regexp"
	"strconv"
	"strings"
)

// Excerpt is what the failing job's log said, reduced to the lines a reader
// needs.
//
// The issue this mechanism exists for asked for a signal that NAMES THE
// FINDING: "a red nightly reaches nobody" is only half the defect, and an issue
// saying "the Trivy job failed, go read the log" is the other half wearing a
// notification. So the advisory id, or the failing test, is carried into the
// issue body itself.
type Excerpt struct {
	// FailedSteps are the step names whose conclusion was failure, from the
	// jobs API rather than from the log. Authoritative, and it survives a log
	// this extractor cannot make sense of.
	FailedSteps []string
	// Lines are the matched log lines, capped.
	Lines []string
	// Matched is how many lines matched before the cap, so the body can say
	// "25 of 312" rather than implying it showed everything.
	Matched int
	// Scanned is how many log lines were read at all. Zero with no Err means
	// an empty log, which is itself worth reporting.
	Scanned int
	// Err is why the log could not be read. A signal that silently omits the
	// findings section looks exactly like a job that failed for no stated
	// reason, so this is rendered rather than swallowed.
	Err error
}

// The caps. Both are about the ISSUE staying readable, not about cost: a body
// carrying three hundred table rows is one nobody scrolls to the end of, which
// is the same failure as not sending it.
const (
	maxExcerptLines  = 30
	maxExcerptColumn = 240
)

// gitea prefixes every log line with an RFC 3339 timestamp and a space. It is
// stripped before matching so that anchored patterns mean what they look like —
// `^--- FAIL:` against a raw line would match nothing, silently, and the
// excerpt would come back empty on exactly the logs it exists for.
var logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)

// ansiEscape strips SGR colour sequences. `go test` and trivy both colourise
// when they think they have a terminal, and the escapes render as mojibake
// inside a Markdown code fence.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// findingPatterns are the lines worth carrying into the issue.
//
// DELIBERATELY GENERIC AND DELIBERATELY FEW. A per-job parser would have to be
// updated whenever a tool changes its output, and the failure mode of a stale
// parser is an empty findings section — indistinguishable, to a reader, from a
// job that failed with nothing to say. These match the shapes that Go's test
// runner, trivy and the Actions runner itself emit on failure, and the
// rendering below states the count so an empty result reads as "nothing
// matched" rather than as "nothing was wrong".
var findingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^--- FAIL: `),
	regexp.MustCompile(`^\s*FAIL\b`),
	regexp.MustCompile(`^panic: `),
	regexp.MustCompile(`^::error`),
	regexp.MustCompile(`^\s*Total: \d+ \(`),             // trivy's per-target summary.
	regexp.MustCompile(`CVE-\d{4}-\d+|GHSA-[0-9a-z-]+`), // an advisory id anywhere on the line.
	regexp.MustCompile(`FIPS 140`),                      // the fips job's cause lines.
}

// ExtractFindings reduces a job log to the lines worth reading.
func ExtractFindings(log string) (lines []string, matched, scanned int) {
	seen := make(map[string]bool)
	for _, raw := range strings.Split(log, "\n") {
		scanned++
		line := strings.TrimRight(logTimestamp.ReplaceAllString(raw, ""), "\r \t")
		line = ansiEscape.ReplaceAllString(line, "")
		if line == "" || !matchesFinding(line) {
			continue
		}
		matched++
		if seen[line] {
			continue
		}
		seen[line] = true
		if len(lines) < maxExcerptLines {
			lines = append(lines, clampColumn(line))
		}
	}
	return lines, matched, scanned
}

func matchesFinding(line string) bool {
	for _, p := range findingPatterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

func clampColumn(line string) string {
	if len(line) <= maxExcerptColumn {
		return line
	}
	return line[:maxExcerptColumn] + " …"
}

// FailedSteps names the steps of a job whose conclusion was failure.
func FailedSteps(steps []Step) []string {
	var failed []string
	for _, s := range steps {
		if s.Conclusion == resultFailure {
			failed = append(failed, s.Name)
		}
	}
	return failed
}

// Render writes the "What the run said" section.
//
// Four distinct outcomes, and they are written differently on purpose. "The log
// could not be read", "the log was empty", "nothing matched" and "here are the
// findings" are four different facts, and collapsing any of them into silence
// is how a notification comes to look like a quiet night.
func (e Excerpt) Render() string {
	var b strings.Builder

	if len(e.FailedSteps) > 0 {
		b.WriteString("Failing step(s): ")
		for i, s := range e.FailedSteps {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("`")
			b.WriteString(s)
			b.WriteString("`")
		}
		b.WriteString("\n\n")
	}

	switch {
	case e.Err != nil:
		b.WriteString("**The job log could not be read: ")
		b.WriteString(e.Err.Error())
		b.WriteString("**\n\nThat is a defect in this signal, not an absence of findings. ")
		b.WriteString("Open the run and read the log directly.\n")
		return b.String()

	case e.Scanned == 0:
		b.WriteString("The job log is empty. A job that failed with no output usually ")
		b.WriteString("means the runner never got as far as the work — read the run itself.\n")
		return b.String()

	case e.Matched == 0:
		b.WriteString("No line of the ")
		b.WriteString(strconv.Itoa(e.Scanned))
		b.WriteString("-line log matched the patterns this signal looks for ")
		b.WriteString("(`--- FAIL:`, `FAIL`, `panic:`, `::error`, trivy's `Total:` line, ")
		b.WriteString("an advisory id, or `FIPS 140`). The job still failed. ")
		b.WriteString("Either the failure has a shape nothing here recognises — which is ")
		b.WriteString("worth fixing in `test/tools/nightlysignal` — or it failed outside ")
		b.WriteString("the work, in setup or teardown.\n")
		return b.String()
	}

	b.WriteString(strconv.Itoa(len(e.Lines)))
	b.WriteString(" of ")
	b.WriteString(strconv.Itoa(e.Matched))
	b.WriteString(" matching line(s), from a ")
	b.WriteString(strconv.Itoa(e.Scanned))
	b.WriteString("-line log:\n\n")
	// A four-backtick fence so a log line carrying a three-backtick run cannot
	// end the block early. Longer runs are collapsed to three for the same
	// reason; nothing else about the line is altered.
	b.WriteString("````text\n")
	for _, line := range e.Lines {
		b.WriteString(collapseFences(line))
		b.WriteString("\n")
	}
	b.WriteString("````\n")
	return b.String()
}

var longFence = regexp.MustCompile("`{4,}")

func collapseFences(line string) string {
	return longFence.ReplaceAllString(line, "```")
}
