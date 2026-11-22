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

package buildgates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The review lock waits in the job's budget (code-review.yml v5.12.5, ruling
// 1012; go-qrdb #1642's Codex review). These tests run code-review.yml's own
// lock_wait, read out of the workflow, with die() and both callers' lines as
// the workflow has them, under a controlled clock: date is a shell function
// that prints $NOW.
const (
	// lockBudgetMinutes is the review job's timeout-minutes.
	lockBudgetMinutes = 75
	// lockRefreshNeed is the refresh caller's reserve once it holds the lock:
	// the refresh call, the module fetch, the largest review and posting.
	lockRefreshNeed = 130 + 360 + 2730 + 440
	// lockReviewReserve is the review caller's reserve over codex's limit:
	// its kill grace, and posting and cleanup.
	lockReviewReserve = 30 + 440
	// lockReviewLimit and lockLargeReviewLimit are codex's limits for a head
	// under and at the large-head file count.
	lockReviewLimit      = 1500
	lockLargeReviewLimit = 2700
	// lockStart is the job's start time the fixtures write to job-start.
	lockStart = 1700000000
)

// lockWaitParts is the code under test, read out of code-review.yml.
type lockWaitParts struct {
	die, budget, lockWait   string
	refreshCall, reviewCall string
}

// readLockWait extracts die(), the JOB_BUDGET line, lock_wait() and its two
// callers' lines; each must be found exactly as many times as expected.
func readLockWait(t *testing.T) lockWaitParts {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", reviewWorkflow))
	if err != nil {
		t.Fatalf("read %s: %v", reviewWorkflow, err)
	}
	text := string(raw)
	var p lockWaitParts
	dies := regexp.MustCompile(`(?m)^          (die\(\) \{.*\})$`).FindAllStringSubmatch(text, -1)
	if len(dies) == 0 {
		t.Fatalf("%s's lib.sh defines no die()", reviewWorkflow)
	}
	p.die = dies[0][1]
	budgets := regexp.MustCompile(`(?m)^          (JOB_BUDGET=\$\(\( (\d+) \* 60 \)\))$`).FindAllStringSubmatch(text, -1)
	if len(budgets) != 1 {
		t.Fatalf("%s sets JOB_BUDGET=$(( <minutes> * 60 )) %d times, want 1", reviewWorkflow, len(budgets))
	}
	if budgets[0][2] != strconv.Itoa(lockBudgetMinutes) {
		t.Fatalf("JOB_BUDGET is %s minutes, want %d", budgets[0][2], lockBudgetMinutes)
	}
	p.budget = budgets[0][1]
	fns := regexp.MustCompile(`(?ms)^          lock_wait\(\) \{\n.*?^          \}\n`).FindAllString(text, -1)
	if len(fns) != 1 {
		t.Fatalf("%s defines lock_wait() %d times, want 1", reviewWorkflow, len(fns))
	}
	p.lockWait = fns[0]
	calls := regexp.MustCompile(`(?m)^ *(wait_s=\$\(lock_wait \$\(\(.*\)\)\))$`).FindAllStringSubmatch(text, -1)
	for _, c := range calls {
		if strings.Contains(c[1], "limit") {
			p.reviewCall = c[1]
		} else {
			p.refreshCall = c[1]
		}
	}
	if len(calls) != 2 || p.reviewCall == "" || p.refreshCall == "" {
		t.Fatalf("%s calls lock_wait %d times, want twice: once for the refresh, once with codex's limit: %q", reviewWorkflow, len(calls), calls)
	}
	return p
}

// runLockWait runs one caller's line with the job started at the given
// job-start contents (no file when start is nil) and the clock at now. It
// returns wait_s, the recorded error and whether the caller failed.
func runLockWait(t *testing.T, p lockWaitParts, call string, limit int, start *string, now int) (wait, errText string, failed bool) {
	t.Helper()
	work := t.TempDir()
	if start != nil {
		if err := os.WriteFile(filepath.Join(work, "job-start"), []byte(*start), 0o600); err != nil {
			t.Fatalf("write job-start: %v", err)
		}
	}
	script := strings.Join([]string{
		"set -euo pipefail",
		"shopt -s inherit_errexit",
		"WORK=" + strconv.Quote(work),
		"CODEX_MASTER=/review",
		p.die,
		`date() { [ "$#" -eq 1 ] && [ "$1" = +%s ] || { echo "unexpected date $*" >&2; exit 98; }; echo "$NOW"; }`,
		p.budget,
		p.lockWait,
		"limit=" + strconv.Itoa(limit),
		call,
		`echo "$wait_s"`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "lock-wait.sh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	cmd := exec.Command("bash", path) // #nosec G204 -- the harness is this test's own file
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NOW=" + strconv.Itoa(now)}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("the harness failed other than by die (exit 1): %v\n%s", err, errOut.String())
		}
	}
	recorded, _ := os.ReadFile(filepath.Join(work, "error"))
	return strings.TrimSpace(out.String()), string(recorded), err != nil
}

// TestReviewLockBudget_Waits: each caller waits for the lock only as long as
// the job's budget, less the time already spent and the time it needs once it
// holds the lock, so a review that takes the lock still posts its verdict
// inside timeout-minutes.
func TestReviewLockBudget_Waits(t *testing.T) {
	p := readLockWait(t)
	budget := lockBudgetMinutes * 60
	start := strconv.Itoa(lockStart)
	cases := []struct {
		name         string
		call         string
		limit, need  int
		elapsed      int
		wantWaitSecs int
	}{
		{"refresh, fresh job", p.refreshCall, lockReviewLimit, lockRefreshNeed, 0, budget - lockRefreshNeed},
		{"refresh, mid job", p.refreshCall, lockReviewLimit, lockRefreshNeed, 600, budget - lockRefreshNeed - 600},
		{"refresh, one second left", p.refreshCall, lockReviewLimit, lockRefreshNeed, budget - lockRefreshNeed - 1, 1},
		{"review, normal limit, fresh job", p.reviewCall, lockReviewLimit, lockReviewLimit + lockReviewReserve, 0, budget - lockReviewLimit - lockReviewReserve},
		{"review, normal limit, mid job", p.reviewCall, lockReviewLimit, lockReviewLimit + lockReviewReserve, 900, budget - lockReviewLimit - lockReviewReserve - 900},
		{"review, normal limit, one second left", p.reviewCall, lockReviewLimit, lockReviewLimit + lockReviewReserve, budget - lockReviewLimit - lockReviewReserve - 1, 1},
		{"review, large limit, fresh job", p.reviewCall, lockLargeReviewLimit, lockLargeReviewLimit + lockReviewReserve, 0, budget - lockLargeReviewLimit - lockReviewReserve},
		{"review, large limit, mid job", p.reviewCall, lockLargeReviewLimit, lockLargeReviewLimit + lockReviewReserve, 600, budget - lockLargeReviewLimit - lockReviewReserve - 600},
		{"review, large limit, one second left", p.reviewCall, lockLargeReviewLimit, lockLargeReviewLimit + lockReviewReserve, budget - lockLargeReviewLimit - lockReviewReserve - 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := lockStart + c.elapsed
			wait, errText, failed := runLockWait(t, p, c.call, c.limit, &start, now)
			if failed {
				t.Fatalf("the caller died: %s", errText)
			}
			got, err := strconv.Atoi(wait)
			if err != nil || got != c.wantWaitSecs {
				t.Fatalf("wait_s = %q, want %d", wait, c.wantWaitSecs)
			}
			// The wait, then the reserve, ends inside the job's budget.
			if end := now + got + c.need; end != lockStart+budget {
				t.Fatalf("the lock wait ends the job at %d, %d s past its start; the budget is %d s", end, end-lockStart, budget)
			}
		})
	}
}

// TestReviewLockBudget_Exhausted: with no second left to wait, each caller
// dies before it calls flock, and the reason is recorded for the PR comment.
func TestReviewLockBudget_Exhausted(t *testing.T) {
	p := readLockWait(t)
	budget := lockBudgetMinutes * 60
	start := strconv.Itoa(lockStart)
	cases := []struct {
		name    string
		call    string
		limit   int
		elapsed int
	}{
		{"refresh, no second left", p.refreshCall, lockReviewLimit, budget - lockRefreshNeed},
		{"refresh, past the budget", p.refreshCall, lockReviewLimit, budget + 60},
		{"review, normal limit, no second left", p.reviewCall, lockReviewLimit, budget - lockReviewLimit - lockReviewReserve},
		{"review, large limit, no second left", p.reviewCall, lockLargeReviewLimit, budget - lockLargeReviewLimit - lockReviewReserve},
		{"review, large limit, the normal limit's time left", p.reviewCall, lockLargeReviewLimit, budget - lockReviewLimit - lockReviewReserve - 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wait, errText, failed := runLockWait(t, p, c.call, c.limit, &start, lockStart+c.elapsed)
			if !failed {
				t.Fatalf("the caller waited %s s with its budget spent; want it to die", wait)
			}
			if !strings.Contains(errText, "no time is left in the job's") {
				t.Fatalf("recorded %q, want the no-time-left reason", errText)
			}
		})
	}
}

// TestReviewLockBudget_BadStart: a missing or malformed job-start makes each
// caller die with its reason; it never waits on a guessed clock.
func TestReviewLockBudget_BadStart(t *testing.T) {
	p := readLockWait(t)
	str := func(s string) *string { return &s }
	cases := []struct {
		name  string
		start *string
		want  string
	}{
		{"missing", nil, "cannot read the job's start time"},
		{"empty", str(""), "is not a number"},
		{"letters", str("soon"), "is not a number"},
		{"negative", str("-5"), "is not a number"},
		{"fraction", str("1700000000.5"), "is not a number"},
		{"leading space", str(" 1700000000"), "is not a number"},
		{"two lines", str("1700000000\n1700000001"), "is not a number"},
		{"exponent", str("17e8"), "is not a number"},
	}
	for _, call := range []struct{ name, line string }{{"refresh", p.refreshCall}, {"review", p.reviewCall}} {
		for _, c := range cases {
			t.Run(call.name+"/"+c.name, func(t *testing.T) {
				wait, errText, failed := runLockWait(t, p, call.line, lockReviewLimit, c.start, lockStart)
				if !failed {
					t.Fatalf("the caller waited %s s on a %s job-start; want it to die", wait, c.name)
				}
				if !strings.Contains(errText, c.want) {
					t.Fatalf("recorded %q, want %q", errText, c.want)
				}
			})
		}
	}
}

// TestReviewLockBudget_MatchesTheWorkflow: the constants above are the
// workflow's: the review job's timeout-minutes is JOB_BUDGET, and codex's
// limits are the normal and large ones the tests run.
func TestReviewLockBudget_MatchesTheWorkflow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", reviewWorkflow))
	if err != nil {
		t.Fatalf("read %s: %v", reviewWorkflow, err)
	}
	text := string(raw)
	i := strings.Index(text, "\n    runs-on: codex ")
	if i < 0 {
		t.Fatalf("%s has no runs-on: codex job", reviewWorkflow)
	}
	m := regexp.MustCompile(`(?m)^    timeout-minutes: (\d+)$`).FindStringSubmatch(text[i:])
	if m == nil || m[1] != strconv.Itoa(lockBudgetMinutes) {
		t.Fatalf("the codex review job's timeout-minutes is %v, want %d, JOB_BUDGET's minutes", m, lockBudgetMinutes)
	}
	l := regexp.MustCompile(`(?s)\n *large=\d+\n *limit=(\d+)\n.{0,200}?\n *limit=(\d+)\n`).FindStringSubmatch(text)
	if l == nil || l[1] != strconv.Itoa(lockReviewLimit) || l[2] != strconv.Itoa(lockLargeReviewLimit) {
		t.Fatalf("codex's limits are %v, want %d and %d at a large head", l, lockReviewLimit, lockLargeReviewLimit)
	}
}
