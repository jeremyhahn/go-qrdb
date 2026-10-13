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

// CI runs in two tiers (#674; handbook sdlc.md §5a, devops.md §1/§5/§8a; the
// pattern is go-quicraft#294). The PR tier runs on every event; the full tier
// runs on push to main, on workflow_dispatch and on a pull request labelled
// `ci:full`. CI Gate stays the one required check, and it passes a skipped job
// only when the job is tagged `full` and the run did not select that tier.
//
// Every property below is one edit from going false without anything else
// noticing:
//
//   - a job tagged `full` whose `if:` differs from FULL_TIER, so the gate
//     excuses a skip in a run that should have executed it;
//   - a PR-tier job that gains an `if:`, so it skips on some event and either
//     fails the gate there or, if retagged, stops gating pull requests;
//   - a job missing from the gate's needs or check lines (fail-open);
//   - a PR-tier job that needs a full-tier one, and so skips with it;
//   - a pull_request trigger without `labeled`, so adding `ci:full` starts
//     nothing;
//   - a pull_request workflow without the ref-keyed cancel-in-progress group
//     (devops.md §9b), or any workflow regaining a `schedule:` (owner ruling
//     2026-10-11: no workflow runs on a cron).
//
// The gate's own shell is executed, not read: TestCIGateScriptHonoursTheTiers
// runs ci-gate's step under bash with each combination of results.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// fullTierIf is the condition every full-tier job carries, and the
	// expression ci-gate evaluates into FULL_TIER.
	fullTierIf = `github.event_name == 'push' || github.event_name == 'workflow_dispatch' || contains(github.event.pull_request.labels.*.name, 'ci:full')`

	tierPR   = "pr"
	tierFull = "full"

	// refKeyedGroup is devops.md §9b's concurrency group: per workflow and
	// per ref, so a branch supersedes only itself.
	refKeyedGroup = "${{ github.workflow }}-${{ github.ref }}"

	// scheduleExempt is the one workflow allowed a `schedule:`. automerge.yml
	// is under owner hold and out of #674's scope.
	scheduleExempt = "automerge.yml"
)

// prTierJobs, fullTierJobs and fullTierJob pin the split itself, not just its
// consistency: moving a job between tiers is a deliberate edit to this file.
// integration-cluster is one job with three matrix legs; ci-gate reads its
// aggregate result.
var (
	prTierJobs   = []string{"lint", "build", "test", "test-sdk", "test-typescript", "security"}
	fullTierJobs = []string{"integration-standalone", "integration-cluster-tenancy", "integration-cluster", "trivy"}
)

// fullTierJob reports whether a job must be in the full tier. Every
// integration job is, however the Integration stage is split, and so is trivy
// (devops.md §8a: `trivy fs` runs in the full tier or nightly, not per PR).
func fullTierJob(name string) bool {
	return strings.HasPrefix(name, "integration") || name == "trivy"
}

// prTriggerTypes are the pull_request activity types ci.yml must declare.
// `labeled` is the one that makes adding `ci:full` start a full-tier run.
var prTriggerTypes = []string{"opened", "synchronize", "reopened", "labeled"}

// checkLine matches one call of ci-gate's check function:
// `check <job> "$RESULT_X" <tier>`.
var checkLine = regexp.MustCompile(`(?m)^\s*check\s+(\S+)\s+"\$(\w+)"\s*(\S*)\s*$`)

// needsResult matches the expression a RESULT_ variable must carry.
var needsResult = regexp.MustCompile(`^\$\{\{\s*needs\.([A-Za-z0-9_-]+)\.result\s*\}\}$`)

// gateCheck is one parsed check line of ci-gate.
type gateCheck struct {
	envVar string
	tier   string
}

// gateScript returns ci-gate's checking step: the one whose run defines the
// check function.
func gateScript(wf ciWorkflow) (run string, env map[string]string, ok bool) {
	gate, found := wf.Jobs[ciGateJob]
	if !found {
		return "", nil, false
	}
	for _, s := range gate.Steps {
		if strings.Contains(s.Run, "check()") {
			return s.Run, s.Env, true
		}
	}
	return "", nil, false
}

// gateChecks parses the check lines of ci-gate's script, keyed by job.
func gateChecks(run string) map[string]gateCheck {
	out := map[string]gateCheck{}
	for _, m := range checkLine.FindAllStringSubmatch(run, -1) {
		out[m[1]] = gateCheck{envVar: m[2], tier: m[3]}
	}
	return out
}

// prTypes returns the activity types ci.yml's pull_request trigger declares.
func prTypes(on yaml.Node) []string {
	if on.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(on.Content); i += 2 {
		if on.Content[i].Value != "pull_request" {
			continue
		}
		pr := on.Content[i+1]
		for j := 0; pr.Kind == yaml.MappingNode && j+1 < len(pr.Content); j += 2 {
			if pr.Content[j].Value == "types" {
				return needsList(*pr.Content[j+1])
			}
		}
	}
	return nil
}

// ciTierProblems validates wf's tiers against ci-gate and returns one message
// per violation, so the real gate can report them all and the self-test can
// prove each one reddens.
func ciTierProblems(wf ciWorkflow) []string {
	var problems []string

	types := prTypes(wf.On)
	for _, want := range prTriggerTypes {
		if !slices.Contains(types, want) {
			problems = append(problems, "ci.yml's pull_request trigger does not list type "+want+
				" (has "+strings.Join(types, ",")+"): adding `ci:full` would not start a full-tier run")
		}
	}

	run, env, ok := gateScript(wf)
	if !ok {
		return append(problems, "ci.yml has no "+ciGateJob+" step defining check(): the tiers have no gate")
	}
	if got := env["FULL_TIER"]; got != "${{ "+fullTierIf+" }}" {
		problems = append(problems, ciGateJob+"'s FULL_TIER is "+strconv.Quote(got)+
			", not the full-tier condition: the gate would excuse skips the jobs' `if:` does not produce")
	}

	checks := gateChecks(run)
	gateNeeds := needsList(wf.Jobs[ciGateJob].Needs)
	tierOf := map[string]string{}

	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if name == ciGateJob {
			continue
		}
		job := wf.Jobs[name]
		if !slices.Contains(gateNeeds, name) {
			problems = append(problems, ciGateJob+" does not list "+name+
				" in needs: a job that can pass and is not required is a fail-open gate")
		}
		c, found := checks[name]
		if !found {
			problems = append(problems, ciGateJob+" has no check line for "+name+
				": its result would never fail the gate")
			continue
		}
		if m := needsResult.FindStringSubmatch(env[c.envVar]); m == nil || m[1] != name {
			problems = append(problems, ciGateJob+" checks "+name+" through $"+c.envVar+
				", which is "+strconv.Quote(env[c.envVar])+", not ${{ needs."+name+".result }}")
		}
		tierOf[name] = c.tier
		switch c.tier {
		case tierFull:
			if job.If != fullTierIf {
				problems = append(problems, "job "+name+" is tagged full in "+ciGateJob+
					" but its `if:` is "+strconv.Quote(job.If)+", not the full-tier condition")
			}
			if slices.Contains(prTierJobs, name) {
				problems = append(problems, "job "+name+" is a PR-tier job tagged full: it would stop "+
					"gating pull requests")
			}
		case tierPR:
			if job.If != "" {
				problems = append(problems, "job "+name+" is PR tier but carries `if: "+job.If+
					"`: a PR-tier job must run on every event, and "+ciGateJob+" fails it when skipped")
			}
			if fullTierJob(name) {
				problems = append(problems, "job "+name+" is tagged pr but belongs to the full tier "+
					"(every integration job and trivy, devops.md §8a)")
			}
		default:
			problems = append(problems, "job "+name+"'s check line in "+ciGateJob+" has tier "+
				strconv.Quote(c.tier)+"; want pr or full")
		}
	}

	for name := range checks {
		if _, found := wf.Jobs[name]; !found {
			problems = append(problems, ciGateJob+" checks "+name+", which is not a job in ci.yml")
		}
	}

	// A full-tier job may need a PR-tier one; never the reverse, or the PR
	// tier skips with it and the gate fails every plain pull request.
	for _, name := range names {
		if tierOf[name] != tierPR {
			continue
		}
		for _, dep := range needsList(wf.Jobs[name].Needs) {
			if tierOf[dep] == tierFull {
				problems = append(problems, "PR-tier job "+name+" needs full-tier job "+dep)
			}
		}
	}
	return problems
}

func readCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	return parseWorkflow(t, string(b))
}

// TestCIGateTiersMatchTheJobs pins the two tiers to the gate.
func TestCIGateTiersMatchTheJobs(t *testing.T) {
	wf := readCIWorkflow(t)
	for _, p := range ciTierProblems(wf) {
		t.Error(p)
	}
	// Discovery floor: every assertion above is inside a loop, and a loop
	// over nothing reports success.
	run, _, ok := gateScript(wf)
	if !ok {
		t.Fatalf("ci.yml has no %s step defining check()", ciGateJob)
	}
	checks := gateChecks(run)
	tiers := map[string]int{}
	for _, c := range checks {
		tiers[c.tier]++
	}
	if tiers[tierPR] < len(prTierJobs) || tiers[tierFull] < len(fullTierJobs) {
		t.Fatalf("%s tags %d jobs pr and %d full; want at least %d and %d: either the parse "+
			"found too little to check or a pinned job left its tier", ciGateJob, tiers[tierPR], tiers[tierFull],
			len(prTierJobs), len(fullTierJobs))
	}
	for tier, names := range map[string][]string{tierPR: prTierJobs, tierFull: fullTierJobs} {
		for _, name := range names {
			if _, found := wf.Jobs[name]; !found {
				t.Errorf("%s-tier job %s is not in ci.yml", tier, name)
			}
			if got := checks[name].tier; got != tier {
				t.Errorf("job %s is tagged %q in %s, want %q", name, got, ciGateJob, tier)
			}
		}
	}
}

// tierFixture is a minimal ci.yml with one job per tier, wired correctly.
// Each case of TestCITierProblems_CanFail breaks exactly one property.
const tierFixture = `
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
    types: [opened, synchronize, reopened, labeled]
  workflow_dispatch:
jobs:
  build:
    steps:
      - run: make build
  integration-x:
    if: ` + fullTierIf + `
    needs: [build]
    steps:
      - run: make integration-test-x
  ci-gate:
    if: always()
    needs: [build, integration-x]
    steps:
      - env:
          RESULT_BUILD: ${{ needs.build.result }}
          RESULT_INTEGRATION_X: ${{ needs.integration-x.result }}
          FULL_TIER: ${{ ` + fullTierIf + ` }}
        run: |
          check() { :; }
          check build "$RESULT_BUILD" pr
          check integration-x "$RESULT_INTEGRATION_X" full
`

// TestCITierProblems_CanFail proves each check in ciTierProblems reddens on
// the defect it names (go-qrdb#500: a guard that cannot go red is not a gate).
func TestCITierProblems_CanFail(t *testing.T) {
	if p := ciTierProblems(parseWorkflow(t, tierFixture)); len(p) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", p)
	}
	replace := func(old, new string) string {
		if !strings.Contains(tierFixture, old) {
			t.Fatalf("fixture has no %q; the case would test nothing", old)
		}
		return strings.Replace(tierFixture, old, new, 1)
	}
	cases := map[string]string{
		"pull_request without labeled": replace("reopened, labeled]", "reopened]"),
		"full job with a different if": replace("    if: "+fullTierIf+"\n    needs: [build]",
			"    if: github.event_name == 'push'\n    needs: [build]"),
		"full job with no if":       replace("    if: "+fullTierIf+"\n    needs: [build]", "    needs: [build]"),
		"pr job with an if":         replace("  build:\n    steps:", "  build:\n    if: github.event_name == 'push'\n    steps:"),
		"full job tagged pr":        replace(`"$RESULT_INTEGRATION_X" full`, `"$RESULT_INTEGRATION_X" pr`),
		"pr job tagged full":        replace(`"$RESULT_BUILD" pr`, `"$RESULT_BUILD" full`),
		"untagged check line":       replace(`"$RESULT_BUILD" pr`, `"$RESULT_BUILD"`),
		"FULL_TIER differs":         replace("FULL_TIER: ${{ "+fullTierIf, "FULL_TIER: ${{ github.event_name == 'push'"),
		"job missing from needs":    replace("needs: [build, integration-x]", "needs: [build]"),
		"job missing a check line":  replace("          check integration-x \"$RESULT_INTEGRATION_X\" full\n", ""),
		"check reads the wrong job": replace("RESULT_INTEGRATION_X: ${{ needs.integration-x.result }}", "RESULT_INTEGRATION_X: ${{ needs.build.result }}"),
		"pr job needs a full job":   replace("  build:\n    steps:", "  build:\n    needs: [integration-x]\n    steps:"),
		"check names no job":        replace("          check build", "          check ghost \"$RESULT_BUILD\" pr\n          check build"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if p := ciTierProblems(parseWorkflow(t, body)); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}

// runGate executes ci-gate's script under bash the way a runner does, with
// every RESULT_ variable set from results (default success) and FULL_TIER.
func runGate(t *testing.T, run string, env map[string]string, results map[string]string, fullTier string) int {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required to execute %s's script: %v", ciGateJob, err)
	}
	vars := []string{"PATH=" + os.Getenv("PATH"), "FULL_TIER=" + fullTier}
	for k, v := range env {
		m := needsResult.FindStringSubmatch(v)
		if m == nil {
			continue
		}
		result, set := results[m[1]]
		if !set {
			result = "success"
		}
		vars = append(vars, k+"="+result)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-eo", "pipefail", "-c", run) // #nosec G204 -- the script is ci.yml's own gate step
	cmd.Env = vars
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("run %s's script: %v\n%s", ciGateJob, err, out)
	return -1
}

// TestCIGateScriptHonoursTheTiers runs ci-gate's real script: a skip passes
// only for a full-tier job outside a full-tier run; everything else that is
// not success fails, cancelled included.
func TestCIGateScriptHonoursTheTiers(t *testing.T) {
	wf := readCIWorkflow(t)
	run, env, ok := gateScript(wf)
	if !ok {
		t.Fatalf("ci.yml has no %s step defining check()", ciGateJob)
	}
	checks := gateChecks(run)
	var full, pr []string
	for name, c := range checks {
		switch c.tier {
		case tierFull:
			full = append(full, name)
		case tierPR:
			pr = append(pr, name)
		}
	}
	if len(full) == 0 || len(pr) == 0 {
		t.Fatalf("%s has %d full and %d pr check lines; this test needs both", ciGateJob, len(full), len(pr))
	}
	sort.Strings(full)
	sort.Strings(pr)
	allFull := func(result string) map[string]string {
		m := map[string]string{}
		for _, j := range full {
			m[j] = result
		}
		return m
	}

	cases := []struct {
		name     string
		results  map[string]string
		fullTier string
		wantExit bool
	}{
		{"everything succeeded, full tier", nil, "true", false},
		{"everything succeeded, PR tier", nil, "false", false},
		{"full tier skipped on a PR-tier run", allFull("skipped"), "false", false},
		{"full tier skipped on a full-tier run", allFull("skipped"), "true", true},
		{"one full job skipped on a full-tier run", map[string]string{full[0]: "skipped"}, "true", true},
		{"full job failed on a PR-tier run", map[string]string{full[0]: "failure"}, "false", true},
		{"full job cancelled on a PR-tier run", map[string]string{full[0]: "cancelled"}, "false", true},
		{"full job cancelled on a full-tier run", map[string]string{full[0]: "cancelled"}, "true", true},
		{"PR job skipped on a PR-tier run", map[string]string{pr[0]: "skipped"}, "false", true},
		{"PR job cancelled", map[string]string{pr[0]: "cancelled"}, "false", true},
		{"PR job failed", map[string]string{pr[0]: "failure"}, "true", true},
		{"PR job result unreadable", map[string]string{pr[0]: ""}, "false", true},
		{"FULL_TIER unreadable", nil, "", true},
		{"FULL_TIER not a boolean", allFull("skipped"), "maybe", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := runGate(t, run, env, tc.results, tc.fullTier)
			if tc.wantExit && code == 0 {
				t.Fatalf("%s passed; want it to fail", ciGateJob)
			}
			if !tc.wantExit && code != 0 {
				t.Fatalf("%s failed with exit %d; want it to pass", ciGateJob, code)
			}
		})
	}
}

// workflowFile is the part of any workflow the trigger rules read.
type workflowFile struct {
	On          yaml.Node `yaml:"on"`
	Concurrency yaml.Node `yaml:"concurrency"`
}

// workflowTriggerProblems checks one workflow file against devops.md §9b and
// the no-schedule ruling.
func workflowTriggerProblems(name string, wf workflowFile) []string {
	var problems []string
	triggers := onTriggers(wf.On)
	if slices.Contains(triggers, "schedule") && name != scheduleExempt {
		problems = append(problems, name+" runs on `schedule:`: no workflow runs on a cron (owner ruling "+
			"2026-10-11); keep workflow_dispatch and let the coordinator's nightly dispatch it")
	}
	if !slices.Contains(triggers, "pull_request") {
		return problems
	}
	var group, cancel string
	if wf.Concurrency.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(wf.Concurrency.Content); i += 2 {
			switch wf.Concurrency.Content[i].Value {
			case "group":
				group = wf.Concurrency.Content[i+1].Value
			case "cancel-in-progress":
				cancel = wf.Concurrency.Content[i+1].Value
			}
		}
	}
	if group != refKeyedGroup {
		problems = append(problems, name+" runs on pull_request but its concurrency group is "+
			strconv.Quote(group)+", not "+refKeyedGroup+" (devops.md §9b)")
	}
	// The literal: Gitea treats any non-empty cancel-in-progress as true, so
	// an expression is not a way to turn it off, and `false` cancels anyway.
	if cancel != "true" {
		problems = append(problems, name+" runs on pull_request but cancel-in-progress is "+
			strconv.Quote(cancel)+", not the literal true (devops.md §9b)")
	}
	return problems
}

// TestPullRequestWorkflowsCancelSupersededRuns scans every workflow file on
// both planes.
func TestPullRequestWorkflowsCancelSupersededRuns(t *testing.T) {
	root := repoRoot(t)
	scanned, withPR := 0, 0
	for _, dir := range workflowDirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !isWorkflowFile(e.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			var wf workflowFile
			if err := yaml.Unmarshal(b, &wf); err != nil {
				t.Fatalf("parse %s: %v", e.Name(), err)
			}
			scanned++
			if slices.Contains(onTriggers(wf.On), "pull_request") {
				withPR++
			}
			for _, p := range workflowTriggerProblems(e.Name(), wf) {
				t.Error(p)
			}
		}
	}
	// ci.yml and release-lockstep.yml, at least.
	if scanned < minWorkflowFiles || withPR < 2 {
		t.Fatalf("scanned %d workflows, %d on pull_request; below the floors of %d and 2", scanned, withPR, minWorkflowFiles)
	}
}

// TestWorkflowTriggerProblems_CanFail proves each trigger rule reddens.
func TestWorkflowTriggerProblems_CanFail(t *testing.T) {
	const good = "on:\n  pull_request:\n  workflow_dispatch:\nconcurrency:\n  group: " + refKeyedGroup + "\n  cancel-in-progress: true\n"
	parse := func(body string) workflowFile {
		var wf workflowFile
		if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return wf
	}
	if p := workflowTriggerProblems("x.yml", parse(good)); len(p) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", p)
	}
	if p := workflowTriggerProblems(scheduleExempt, parse("on:\n  schedule:\n    - cron: '0 3 * * *'\n")); len(p) != 0 {
		t.Fatalf("%s is exempt from the schedule rule, got %v", scheduleExempt, p)
	}
	cases := map[string]string{
		"schedule":                "on:\n  schedule:\n    - cron: '0 3 * * *'\n  workflow_dispatch:\n",
		"no concurrency":          "on:\n  pull_request:\n",
		"group not ref-keyed":     strings.Replace(good, refKeyedGroup, "${{ github.workflow }}", 1),
		"cancel-in-progress off":  strings.Replace(good, "cancel-in-progress: true", "cancel-in-progress: false", 1),
		"cancel-in-progress gone": strings.Replace(good, "  cancel-in-progress: true\n", "", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if p := workflowTriggerProblems("x.yml", parse(body)); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
