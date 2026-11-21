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

// code_review_gate_test.go covers the v5.12.6 Codex gate of
// .github/workflows/code-review.yml (ruling 1046): the gate no longer waits
// for a twin run's review. A head with no APPROVED verdict while a twin's
// `Codex review` job is queued or in progress fails at once, naming the twin,
// and the twin's own gate posts the head's check after its review ends.
//
// The step's shell is read out of the workflow (never copied) and run under
// bash with a fake curl and a fake sleep first on PATH, so the test follows
// the workflow as it changes. It is a Go port of the handbook template's
// testdata/code-review-v5126-tests.py.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	gateStepName = "Require a Codex approval of the head, or a no-review change"
	gateAPI      = "https://api.test/repos/owner/repo"
	gateHead     = "1111111111111111111111111111111111111111"
	gateBase     = "2222222222222222222222222222222222222222"
	gateReviewer = "codex-reviewer"
	gatePR       = "7"
	gateTitle    = "Code Review: PR #7 into main at " + gateHead
)

// gateFakeCurl answers from canned bodies by URL glob: each line of
// $FAKE_ROUTES is "glob<TAB>code<TAB>body file", first match wins. It writes
// the body to the -o file (or stdout), the code for -w, and logs every path.
const gateFakeCurl = `#!/usr/bin/env bash
set -u
out= fmt= url=
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -w) fmt=$2; shift 2 ;;
    -X|-H|--proto|--max-time|--retry|--data-binary) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
path=${url#"$FAKE_BASE"}
printf '%s\n' "$path" >> "$FAKE_LOG"
code=404 body=$FAKE_NOROUTE
while IFS=$'\t' read -r glob c b; do
  if [[ $path == $glob ]]; then code=$c; body=$b; break; fi
done < "$FAKE_ROUTES"
if [ -n "$out" ]; then cat "$body" > "$out"; else cat "$body"; fi
[ -z "$fmt" ] || printf '%s' "$code"
`

const gateFakeSleep = "#!/bin/sh\necho \"sleep $*\" >> \"$FAKE_LOG\"\necho 'the gate slept' >&2\nexit 99\n"

type gateWorkflow struct {
	Jobs map[string]struct {
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Env            map[string]string `yaml:"env"`
		Steps          []struct {
			Name string            `yaml:"name"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// gateStep is the gate job's timeout, its step's run: block, and the env the
// step sees (the job's env: overlaid with the step's).
type gateStep struct {
	timeout int
	script  string
	env     map[string]string
}

func loadGateStep(t *testing.T) gateStep {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "code-review.yml"))
	if err != nil {
		t.Fatalf("read code-review.yml: %v", err)
	}
	var wf gateWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("code-review.yml is not parseable YAML: %v", err)
	}
	job, ok := wf.Jobs["gate"]
	if !ok {
		t.Fatal("code-review.yml has no gate job")
	}
	for _, s := range job.Steps {
		if s.Name != gateStepName {
			continue
		}
		env := map[string]string{}
		for k, v := range job.Env {
			env[k] = v
		}
		for k, v := range s.Env {
			env[k] = v
		}
		if strings.TrimSpace(s.Run) == "" {
			t.Fatalf("step %q has no run: block", gateStepName)
		}
		return gateStep{timeout: job.TimeoutMinutes, script: s.Run, env: env}
	}
	t.Fatalf("the gate job has no step %q", gateStepName)
	return gateStep{}
}

// gateCase is one run of the gate. verdict is APPROVED, REQUEST_CHANGES or
// "" (no review of the head).
type gateCase struct {
	runID        string
	verdict      string
	queued       []string          // twin runs listed as queued
	inProgress   []string          // twin runs listed as in_progress
	twinJobs     map[string]string // twin id -> its Codex review job status (default in_progress)
	runsCode     int               // default 200
	jobsCode     int               // default 200
	reviewResult string            // default skipped
}

type gateResult struct {
	rc       int
	out      string
	requests []string
	elapsed  time.Duration
}

var gateExprRe = regexp.MustCompile(`\$\{\{.*?\}\}`)

func runCodexGate(t *testing.T, step gateStep, c gateCase) gateResult {
	t.Helper()
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not on PATH")
	}
	if c.runID == "" {
		c.runID = "77"
	}
	if c.runsCode == 0 {
		c.runsCode = 200
	}
	if c.jobsCode == 0 {
		c.jobsCode = 200
	}
	if c.reviewResult == "" {
		c.reviewResult = "skipped"
	}

	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.Mkdir(binDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, text := range map[string]string{"curl": gateFakeCurl, "sleep": gateFakeSleep} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(text), 0o700); err != nil { // #nosec G306 -- the stub must be executable; PATH resolves it for the step
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	writeData := func(name, body string) string {
		t.Helper()
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	mustJSON := func(v any) string {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	runsBody := func(ids []string) string {
		runs := []map[string]any{}
		for _, id := range ids {
			var n int64
			if _, err := fmt.Sscan(id, &n); err != nil {
				t.Fatalf("run id %q: %v", id, err)
			}
			runs = append(runs, map[string]any{"id": n, "display_title": gateTitle})
		}
		return mustJSON(map[string]any{"workflow_runs": runs})
	}
	var reviews []map[string]any
	if c.verdict != "" {
		reviews = append(reviews, map[string]any{
			"id": 1, "state": c.verdict, "commit_id": gateHead,
			"user":         map[string]any{"login": gateReviewer},
			"submitted_at": "2026-11-20T00:00:00Z",
		})
	}
	if reviews == nil {
		reviews = []map[string]any{}
	}

	type route struct {
		glob string
		code int
		body string
	}
	routes := []route{
		{"/compare/" + gateBase + "..." + gateHead, 200, mustJSON(map[string]any{"files": []any{map[string]any{"filename": "pkg/x.go"}}})},
		{"/actions/runs/" + c.runID, 200, `{"workflow_id":5}`},
		{"/actions/workflows/5/runs?*status=queued*", c.runsCode, runsBody(append([]string{c.runID}, c.queued...))},
		{"/actions/workflows/5/runs?*status=in_progress*", c.runsCode, runsBody(c.inProgress)},
	}
	for _, id := range append(append([]string{}, c.queued...), c.inProgress...) {
		st := c.twinJobs[id]
		if st == "" {
			st = "in_progress"
		}
		routes = append(routes, route{"/actions/runs/" + id + "/jobs?*", c.jobsCode, mustJSON(map[string]any{"jobs": []any{
			map[string]any{"name": "scope", "status": "completed"},
			map[string]any{"name": "Codex review", "status": st},
		}})})
	}
	routes = append(routes,
		route{"/pulls/" + gatePR, 200, mustJSON(map[string]any{"head": map[string]any{"sha": gateHead}, "labels": []any{map[string]any{"name": "code-review"}}})},
		route{"/pulls/" + gatePR + "/reviews?*page=1", 200, mustJSON(reviews)},
		route{"/pulls/" + gatePR + "/reviews?*", 200, "[]"},
	)
	var table strings.Builder
	for i, r := range routes {
		fmt.Fprintf(&table, "%s\t%d\t%s\n", r.glob, r.code, writeData(fmt.Sprintf("route-%d.json", i), r.body))
	}
	routesFile := writeData("routes.tsv", table.String())
	noRoute := writeData("noroute.json", `{"message":"no route"}`)
	logFile := writeData("requests.log", "")
	scriptFile := writeData("gate.sh", step.script)

	// Every ${{ }} in the step's env gets a test value; literal values (the
	// path lists) are the workflow's own.
	values := map[string]string{
		"GH_API_TOKEN":  "test-token",
		"FORGE":         "github",
		"REVIEWER":      gateReviewer,
		"REPO_API":      gateAPI,
		"PR_NUM":        gatePR,
		"EVENT_SHA":     gateHead,
		"BASE_SHA":      gateBase,
		"BASE_REF":      "main",
		"SCOPE_SKIP":    "false",
		"SCOPE_RESULT":  "success",
		"REVIEW_RESULT": c.reviewResult,
	}
	env := []string{
		"PATH=" + binDir + string(os.PathListSeparator) + filepath.Dir(jqPath) + ":/usr/bin:/bin",
		"HOME=" + tmp,
		"FAKE_LOG=" + logFile,
		"FAKE_ROUTES=" + routesFile,
		"FAKE_NOROUTE=" + noRoute,
		"FAKE_BASE=" + gateAPI,
		"GITHUB_RUN_ID=" + c.runID,
	}
	for k, v := range step.env {
		if gateExprRe.MatchString(v) {
			tv, ok := values[k]
			if !ok {
				t.Fatalf("the gate's env %s=%q has no test value; give it one here", k, v)
			}
			v = tv
		}
		env = append(env, k+"="+v)
	}
	for _, k := range []string{"REVIEW_RESULT", "EVENT_SHA", "PR_NUM", "REPO_API", "REVIEWER"} {
		if _, ok := step.env[k]; !ok {
			t.Fatalf("the gate's env has no %s; this test no longer drives the step it reads", k)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bashPath, scriptFile) // #nosec G204 -- bash from exec.LookPath, script is this test's TempDir copy of the workflow step
	cmd.Dir = tmp
	cmd.Env = env
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	rc := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run the gate step: %v", err)
		}
		rc = ee.ExitCode()
	}
	logged, err := os.ReadFile(logFile) // #nosec G304 -- this test's TempDir request log
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	return gateResult{rc: rc, out: string(out), requests: strings.Split(strings.TrimSpace(string(logged)), "\n"), elapsed: elapsed}
}

// wantGate asserts the exit code, that the gate never slept, and that it
// answered promptly (fake API, so well inside a second; 30 s is the bound).
func wantGate(t *testing.T, r gateResult, rc int, contains ...string) {
	t.Helper()
	if r.rc != rc {
		t.Fatalf("gate exited %d, want %d; output:\n%s", r.rc, rc, r.out)
	}
	for _, req := range r.requests {
		if strings.HasPrefix(req, "sleep") {
			t.Fatalf("the gate called sleep (%q); v5.12.6 has no twin wait. Output:\n%s", req, r.out)
		}
	}
	if r.elapsed > 30*time.Second {
		t.Fatalf("the gate took %v; it must answer at once", r.elapsed)
	}
	for _, s := range contains {
		if !strings.Contains(r.out, s) {
			t.Fatalf("gate output lacks %q:\n%s", s, r.out)
		}
	}
}

func TestCodeReviewGateTwinHandoff(t *testing.T) {
	step := loadGateStep(t)

	t.Run("T/no wait left", func(t *testing.T) {
		if step.timeout != 10 {
			t.Errorf("gate timeout-minutes = %d, want 10 (v5.12.6: no twin wait)", step.timeout)
		}
		for _, s := range []string{"sleep", "4500"} {
			if strings.Contains(step.script, s) {
				t.Errorf("the gate step holds %q; v5.12.6 removed the twin wait", s)
			}
		}
	})

	t.Run("Q/twin reviewing, head not approved fails at once", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			verdict string
			c       gateCase
		}{
			{"queued, no review", "", gateCase{queued: []string{"88"}, twinJobs: map[string]string{"88": "queued"}}},
			{"in_progress, no review", "", gateCase{inProgress: []string{"88"}, twinJobs: map[string]string{"88": "in_progress"}}},
			{"queued, changes requested", "REQUEST_CHANGES", gateCase{queued: []string{"88"}, twinJobs: map[string]string{"88": "queued"}}},
			{"in_progress, changes requested", "REQUEST_CHANGES", gateCase{inProgress: []string{"88"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tc.c.verdict = tc.verdict
				wantGate(t, runCodexGate(t, step, tc.c), 3, "runs in run 88")
			})
		}
	})

	t.Run("A/twin reviewing, head approved passes", func(t *testing.T) {
		wantGate(t, runCodexGate(t, step, gateCase{verdict: "APPROVED", queued: []string{"88"}}), 0, "approved "+gateHead)
		wantGate(t, runCodexGate(t, step, gateCase{verdict: "APPROVED", inProgress: []string{"88"}}), 0, "approved "+gateHead)
	})

	t.Run("N/no twin", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			c    gateCase
		}{
			{"no runs", gateCase{}},
			{"twin review completed", gateCase{inProgress: []string{"88"}, twinJobs: map[string]string{"88": "completed"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := tc.c
				c.verdict = "APPROVED"
				wantGate(t, runCodexGate(t, step, c), 0, "approved "+gateHead)
				c.verdict = "REQUEST_CHANGES"
				wantGate(t, runCodexGate(t, step, c), 3, "requested changes")
				c.verdict = ""
				r := runCodexGate(t, step, c)
				wantGate(t, r, 3, "no "+gateReviewer+" approval")
				if strings.Contains(r.out, "runs in run") {
					t.Fatalf("no twin is reviewing, yet the gate named one:\n%s", r.out)
				}
			})
		}
	})

	t.Run("E/API errors fail closed", func(t *testing.T) {
		wantGate(t, runCodexGate(t, step, gateCase{verdict: "APPROVED", runsCode: 500}), 3, "HTTP 500")
		wantGate(t, runCodexGate(t, step, gateCase{verdict: "APPROVED", queued: []string{"88"}, jobsCode: 500}), 3, "cannot read the jobs of run 88")
	})

	t.Run("H/handoff to the reviewing twin", func(t *testing.T) {
		// Run 77 skipped its review (a twin event) while run 88 reviews.
		first := gateCase{runID: "77", queued: []string{"88"}, twinJobs: map[string]string{"88": "queued"}}
		for _, tc := range []struct {
			name, verdict, reviewResult string
			wantSecond                  int
		}{
			{"review approves", "APPROVED", "success", 0},
			{"review requests changes", "REQUEST_CHANGES", "success", 3},
			{"review job failed over an earlier approval", "APPROVED", "failure", 3},
		} {
			t.Run(tc.name, func(t *testing.T) {
				wantGate(t, runCodexGate(t, step, first), 3, "runs in run 88")
				// Run 88's gate, after its review posted: run 77's review job
				// (skipped) is completed, so 88 sees no twin and posts the
				// verdict as the head's check.
				second := gateCase{
					runID: "88", verdict: tc.verdict, reviewResult: tc.reviewResult,
					inProgress: []string{"77"}, twinJobs: map[string]string{"77": "completed"},
				}
				r := runCodexGate(t, step, second)
				wantGate(t, r, tc.wantSecond)
				if strings.Contains(r.out, "runs in run") {
					t.Fatalf("the reviewing run handed off again:\n%s", r.out)
				}
			})
		}
	})
}
