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
// pattern is go-quicraft#294), and a third for the soak. The PR tier runs on
// every event; the full tier runs on every event but a pull request, and on a
// pull request labelled `ci:full` (ci-template v1.23's CI_FULL_TIER); the
// nightly tier runs on schedule and workflow_dispatch only (v1.24), and since
// ci.yml has no schedule (Company Agent m433) that is dispatch alone. CI Gate
// stays the one required check, and it passes a skipped job only when the job
// is tagged `full` or `nightly` and the run did not select that tier. The code
// tier (DS-3160) runs on every event but a docs-only pull request, and the gate
// passes its skip only when the changes job said docs-only (DOCS_ONLY true).
//
// Every property below is one edit from going false without anything else
// noticing:
//
//   - a job tagged `full` whose `if:` differs from CI_FULL_TIER, or a
//     FULL_TIER that is not CI_FULL_TIER, so the gate excuses a skip in a run
//     that should have executed it;
//   - a job tagged `nightly` whose `if:` differs from NIGHTLY, the same way;
//   - a job tagged `code` whose `if:` is not exactly codeTierIf, that does not
//     need changes, or a DOCS_ONLY that is not changes' docs_only output;
//   - a PR-tier job that gains an `if:`, so it skips on some event and either
//     fails the gate there or, if retagged, stops gating pull requests;
//   - a job missing from the gate's needs or check lines (fail-open);
//   - a PR-tier job that needs a code, full-tier or nightly one, or a code
//     job that needs a full-tier or nightly one, and so skips with it;
//   - a pull_request trigger with `labeled`, so every label change starts a
//     run that cancels the PR's run in progress (#853);
//   - ci.yml without its concurrency block (a main group per event only, so
//     a newer main push cancels the run in flight, Company Agent m772; a
//     literal cancel-in-progress, Company Agent m701);
//   - any other pull_request workflow without the ref-keyed concurrency group
//     whose cancel-in-progress spares main (devops.md §9, ci-template
//     v1.22.1), or any workflow regaining a `schedule:` (owner ruling
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
	// fullTierIf is the condition every full-tier job carries (ci-template
	// v1.23): every event but a pull request, and a pull request labelled
	// ci:full. ci.yml's env evaluates it into CI_FULL_TIER, which ci-gate
	// reads into FULL_TIER (fullTierGateEnv); a job-level `if:` cannot read
	// env, so each full-tier job repeats it.
	fullTierIf = `github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'ci:full')`

	// fullTierGateEnv is ci-gate's FULL_TIER: the workflow's CI_FULL_TIER.
	fullTierGateEnv = "${{ env.CI_FULL_TIER }}"

	// nightlyIf is the `if:` of every nightly job (v1.24) and ci-gate's
	// NIGHTLY, spelled as ci.yml spells both.
	nightlyIf = "${{ github.event_name == 'schedule' || github.event_name == 'workflow_dispatch' }}"

	// codeTierIf is the `if:` of every code-tier job (DS-3160), spelled
	// exactly so: every event but a docs-only pull request. docs_only is
	// false off pull_request and on ci:full, so a code job runs on main,
	// dispatch and ci:full.
	codeTierIf = "needs.changes.outputs.docs_only != 'true'"

	// codeTierGateEnv is ci-gate's DOCS_ONLY: the changes job's output.
	codeTierGateEnv = "${{ needs.changes.outputs.docs_only }}"

	// changesJob decides whether a pull request is docs-only; every code
	// job needs it, or its `if:` reads nothing.
	changesJob = "changes"

	tierPR      = "pr"
	tierCode    = "code"
	tierFull    = "full"
	tierNightly = "nightly"

	// ciGateJob is the one required check.
	ciGateJob = "ci-gate"

	// refKeyedGroup is devops.md §9b's concurrency group: per workflow and
	// per ref, so a branch supersedes only itself.
	refKeyedGroup = "${{ github.workflow }}-${{ github.ref }}"

	// cancelOffMain is ci-template v1.22.1's cancel-in-progress (devops.md
	// §9): a PR ref's newer run cancels the older, a run on main never is.
	cancelOffMain = "${{ github.ref != 'refs/heads/main' }}"

	// ciWorkflowName is the one workflow on ci-template v1.25's concurrency block
	// (Company Agent m701, m857); every other pull_request workflow keeps the
	// v1.22.1 pair above.
	ciWorkflowName = "ci.yml"

	// mainKeyedGroup is ci.yml's concurrency group: a PR ref is one group,
	// and on main the group carries the event only (Company Agent m772, m774
	// option (i)), so a newer push on main cancels the main run of the same
	// event in flight. It reverses m636 (2), which gave each main commit its
	// own group.
	mainKeyedGroup = "${{ github.workflow }}-${{ github.ref == 'refs/heads/main' && format('main-{0}', github.event_name) || github.ref }}"

	// cancelLiteral is ci-template v1.23's cancel-in-progress. Gitea 1.27.1
	// never released go-qrdb main runs 57063 and 57200 while it was an
	// expression, so no expression reaches it.
	cancelLiteral = "true"

	// scheduleExempt is the one workflow allowed a `schedule:`. automerge.yml
	// is under owner hold and out of #674's scope.
	scheduleExempt = "automerge.yml"
)

// prTierJobs, codeTierJobs, fullTierJobs, nightlyTierJobs and fullTierJob pin
// the split itself, not just its consistency: moving a job between tiers is a
// deliberate edit to this file. integration-features (featureJob) is one job
// with a matrix leg per feature; ci-gate reads its aggregate result. changes
// (v1.24) is PR tier: it decides whether a pull request is docs-only, and image
// needs it. arch-gates stays PR tier because make check-public-releasable
// scans docs too; test-typescript builds and tests nothing a docs-only pull
// request changes (DS-3160).
var (
	prTierJobs      = []string{changesJob, "image", "arch-gates"}
	codeTierJobs    = []string{"test-typescript"}
	fullTierJobs    = []string{"integration-standalone", featureJob, fipsJob}
	nightlyTierJobs = []string{"soak"}
)

// fipsJob runs the unit suites under GODEBUG=fips140=only (#20,
// docs/seal/fips140-mode.md).
const fipsJob = "fips"

// fullTierJob reports whether a job must be in the full tier. Every
// integration job is, however the Integration stage is split, and so is trivy
// (devops.md §8a: `trivy fs` runs in the full tier or nightly, not per PR),
// and so is fips: it reruns both unit suites under the FIPS module, which no
// pull request's budget holds.
func fullTierJob(name string) bool {
	return strings.HasPrefix(name, "integration") || name == "trivy" || name == fipsJob
}

// prTriggerTypes are the pull_request activity types ci.yml must declare.
// `labeled` is deliberately absent (#853): Gitea maps every label change to it,
// so each `task:*` or `code-review` change started a run that cancelled the
// PR's run in progress.
var prTriggerTypes = []string{"opened", "synchronize", "reopened"}

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

// ciWorkflow is the slice of ci.yml the tier rules read.
type ciWorkflow struct {
	Jobs map[string]struct {
		If    string    `yaml:"if"`
		Needs yaml.Node `yaml:"needs"`
		Steps []struct {
			Run string            `yaml:"run"`
			Env map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
	On  yaml.Node         `yaml:"on"`
	Env map[string]string `yaml:"env"`
}

func parseWorkflow(t *testing.T, body string) ciWorkflow {
	t.Helper()
	var wf ciWorkflow
	if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatalf("parse workflow fixture: %v", err)
	}
	return wf
}

// needsList returns the job names a `needs:` declares, in either the
// scalar or the sequence spelling.
func needsList(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	default:
		return nil
	}
}

// onTriggers returns the event names a workflow's top-level `on:` declares,
// whichever of the three shapes the workflow spells it in: a mapping
// (`on: {push: ..., pull_request: ...}`), a sequence (`on: [push,
// pull_request]`), or a single scalar (`on: push`).
func onTriggers(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	case yaml.MappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out
	default:
		return nil
	}
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
				" (has "+strings.Join(types, ",")+")")
		}
	}
	if slices.Contains(types, "labeled") {
		problems = append(problems, "ci.yml's pull_request trigger lists type labeled (has "+
			strings.Join(types, ",")+"): every label change would start a run that cancels the PR's run in progress (#853)")
	}

	run, env, ok := gateScript(wf)
	if !ok {
		return append(problems, "ci.yml has no "+ciGateJob+" step defining check(): the tiers have no gate")
	}
	if got := wf.Env["CI_FULL_TIER"]; got != "${{ "+fullTierIf+" }}" {
		problems = append(problems, "ci.yml's CI_FULL_TIER is "+strconv.Quote(got)+
			", not the full-tier condition: the steps it gates and the gate's FULL_TIER would disagree with the jobs' `if:`")
	}
	if got := env["FULL_TIER"]; got != fullTierGateEnv {
		problems = append(problems, ciGateJob+"'s FULL_TIER is "+strconv.Quote(got)+", not "+fullTierGateEnv+
			": the gate would excuse skips the jobs' `if:` does not produce")
	}
	if got := env["NIGHTLY"]; got != nightlyIf {
		problems = append(problems, ciGateJob+"'s NIGHTLY is "+strconv.Quote(got)+
			", not the nightly condition: the gate would excuse skips the nightly jobs' `if:` does not produce")
	}
	if got := env["DOCS_ONLY"]; got != codeTierGateEnv {
		problems = append(problems, ciGateJob+"'s DOCS_ONLY is "+strconv.Quote(got)+", not "+codeTierGateEnv+
			": the gate would excuse skips the code jobs' `if:` does not produce")
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
					"(every integration job, trivy and fips, devops.md §8a)")
			}
			if slices.Contains(codeTierJobs, name) {
				problems = append(problems, "job "+name+" is a code-tier job tagged pr: "+ciGateJob+
					" would fail every docs-only pull request that skips it")
			}
		case tierCode:
			if job.If != codeTierIf {
				problems = append(problems, "job "+name+" is tagged code in "+ciGateJob+
					" but its `if:` is "+strconv.Quote(job.If)+", not the code-tier condition "+strconv.Quote(codeTierIf))
			}
			if !slices.Contains(needsList(job.Needs), changesJob) {
				problems = append(problems, "code-tier job "+name+" does not need "+changesJob+
					": its `if:` would read no docs_only output")
			}
			if slices.Contains(prTierJobs, name) || fullTierJob(name) {
				problems = append(problems, "job "+name+" is a PR-tier or full-tier job tagged code: it would "+
					"stop gating docs-only pull requests or main")
			}
		case tierNightly:
			if job.If != nightlyIf {
				problems = append(problems, "job "+name+" is tagged nightly in "+ciGateJob+
					" but its `if:` is "+strconv.Quote(job.If)+", not the nightly condition")
			}
			if slices.Contains(prTierJobs, name) || fullTierJob(name) {
				problems = append(problems, "job "+name+" is a PR-tier or full-tier job tagged nightly: it would "+
					"stop gating pull requests or main")
			}
		default:
			problems = append(problems, "job "+name+"'s check line in "+ciGateJob+" has tier "+
				strconv.Quote(c.tier)+"; want pr, code, full or nightly")
		}
	}

	for name := range checks {
		if _, found := wf.Jobs[name]; !found {
			problems = append(problems, ciGateJob+" checks "+name+", which is not a job in ci.yml")
		}
	}

	// A job may need one that runs on every run it does; never one that can
	// skip where it must run, or it skips with it and the gate fails. A PR-tier
	// job runs on every run, a code-tier job on every run but a docs-only pull
	// request, so a PR-tier job needs no code, full or nightly job and a
	// code-tier job no full or nightly one.
	narrower := map[string][]string{
		tierPR:   {tierCode, tierFull, tierNightly},
		tierCode: {tierFull, tierNightly},
	}
	for _, name := range names {
		for _, dep := range needsList(wf.Jobs[name].Needs) {
			if t := tierOf[dep]; slices.Contains(narrower[tierOf[name]], t) {
				problems = append(problems, tierOf[name]+"-tier job "+name+" needs "+t+"-tier job "+dep)
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
	if tiers[tierPR] < len(prTierJobs) || tiers[tierCode] < len(codeTierJobs) ||
		tiers[tierFull] < len(fullTierJobs) || tiers[tierNightly] < len(nightlyTierJobs) {
		t.Fatalf("%s tags %d jobs pr, %d code, %d full and %d nightly; want at least %d, %d, %d and %d: either "+
			"the parse found too little to check or a pinned job left its tier", ciGateJob, tiers[tierPR],
			tiers[tierCode], tiers[tierFull], tiers[tierNightly], len(prTierJobs), len(codeTierJobs),
			len(fullTierJobs), len(nightlyTierJobs))
	}
	for tier, names := range map[string][]string{tierPR: prTierJobs, tierCode: codeTierJobs, tierFull: fullTierJobs, tierNightly: nightlyTierJobs} {
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
    types: [opened, synchronize, reopened]
  workflow_dispatch:
env:
  CI_FULL_TIER: ${{ ` + fullTierIf + ` }}
jobs:
  build:
    steps:
      - run: make build
  changes:
    steps:
      - run: echo docs_only=false
  test-typescript:
    if: ` + codeTierIf + `
    needs: [changes]
    steps:
      - run: make test-typescript
  integration-x:
    if: ` + fullTierIf + `
    needs: [build]
    steps:
      - run: make integration-test-x
  fips:
    if: ` + fullTierIf + `
    needs: [build]
    steps:
      - run: make test
  soak-x:
    if: ` + nightlyIf + `
    needs: [build]
    steps:
      - run: make soak-x
  ci-gate:
    if: always()
    needs: [build, fips, changes, test-typescript, integration-x, soak-x]
    steps:
      - env:
          RESULT_BUILD: ${{ needs.build.result }}
          RESULT_FIPS: ${{ needs.fips.result }}
          RESULT_CHANGES: ${{ needs.changes.result }}
          RESULT_TEST_TYPESCRIPT: ${{ needs.test-typescript.result }}
          RESULT_INTEGRATION_X: ${{ needs.integration-x.result }}
          RESULT_SOAK_X: ${{ needs.soak-x.result }}
          FULL_TIER: ` + fullTierGateEnv + `
          NIGHTLY: ` + nightlyIf + `
          DOCS_ONLY: ` + codeTierGateEnv + `
        run: |
          check() { :; }
          check build "$RESULT_BUILD" pr
          check changes "$RESULT_CHANGES" pr
          check test-typescript "$RESULT_TEST_TYPESCRIPT" code
          check integration-x "$RESULT_INTEGRATION_X" full
          check fips "$RESULT_FIPS" full
          check soak-x "$RESULT_SOAK_X" nightly
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
		"pull_request with labeled":     replace("synchronize, reopened]", "synchronize, reopened, labeled]"),
		"pull_request without reopened": replace("synchronize, reopened]", "synchronize]"),
		"full job with a different if": replace("    if: "+fullTierIf+"\n    needs: [build]",
			"    if: github.event_name == 'push'\n    needs: [build]"),
		"full job with no if":  replace("    if: "+fullTierIf+"\n    needs: [build]", "    needs: [build]"),
		"pr job with an if":    replace("  build:\n    steps:", "  build:\n    if: github.event_name == 'push'\n    steps:"),
		"full job tagged pr":   replace(`"$RESULT_INTEGRATION_X" full`, `"$RESULT_INTEGRATION_X" pr`),
		"pr job tagged full":   replace(`"$RESULT_BUILD" pr`, `"$RESULT_BUILD" full`),
		"untagged check line":  replace(`"$RESULT_BUILD" pr`, `"$RESULT_BUILD"`),
		"FULL_TIER differs":    replace("FULL_TIER: "+fullTierGateEnv, "FULL_TIER: ${{ github.event_name == 'push' }}"),
		"CI_FULL_TIER differs": replace("CI_FULL_TIER: ${{ "+fullTierIf, "CI_FULL_TIER: ${{ github.event_name == 'push'"),
		"CI_FULL_TIER missing": replace("env:\n  CI_FULL_TIER: ${{ "+fullTierIf+" }}\n", ""),
		"NIGHTLY differs":      replace("NIGHTLY: "+nightlyIf, "NIGHTLY: ${{ github.event_name == 'workflow_dispatch' }}"),
		"NIGHTLY missing":      replace("          NIGHTLY: "+nightlyIf+"\n", ""),
		"nightly job with a different if": replace("    if: "+nightlyIf+"\n    needs: [build]",
			"    if: ${{ github.event_name == 'push' }}\n    needs: [build]"),
		"nightly job with no if":         replace("    if: "+nightlyIf+"\n    needs: [build]", "    needs: [build]"),
		"nightly job tagged full":        replace(`"$RESULT_SOAK_X" nightly`, `"$RESULT_SOAK_X" full`),
		"nightly job tagged pr":          replace(`"$RESULT_SOAK_X" nightly`, `"$RESULT_SOAK_X" pr`),
		"full job tagged nightly":        replace(`"$RESULT_INTEGRATION_X" full`, `"$RESULT_INTEGRATION_X" nightly`),
		"pr job tagged nightly":          replace(`"$RESULT_BUILD" pr`, `"$RESULT_BUILD" nightly`),
		"pr job needs a nightly job":     replace("  build:\n    steps:", "  build:\n    needs: [soak-x]\n    steps:"),
		"nightly job missing from needs": replace("test-typescript, integration-x, soak-x]", "test-typescript, integration-x]"),
		"job missing from needs":         replace("test-typescript, integration-x, soak-x]", "test-typescript, soak-x]"),
		"job missing a check line":       replace("          check integration-x \"$RESULT_INTEGRATION_X\" full\n", ""),
		"check reads the wrong job":      replace("RESULT_INTEGRATION_X: ${{ needs.integration-x.result }}", "RESULT_INTEGRATION_X: ${{ needs.build.result }}"),
		"pr job needs a full job":        replace("  build:\n    steps:", "  build:\n    needs: [integration-x]\n    steps:"),
		"check names no job":             replace("          check build", "          check ghost \"$RESULT_BUILD\" pr\n          check build"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if p := ciTierProblems(parseWorkflow(t, body)); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}

	// The code tier (DS-3160): each case names the problem its own guard
	// reports, so a case another guard happens to catch does not stand in for
	// a guard that has gone missing.
	codeJob := "  test-typescript:\n    if: " + codeTierIf + "\n    needs: [changes]\n"
	codeCases := map[string]struct{ body, want string }{
		"code job with a different if": {replace(codeJob,
			"  test-typescript:\n    if: github.event_name == 'push'\n    needs: [changes]\n"),
			"is tagged code in " + ciGateJob + " but its `if:`"},
		"code job with no if": {replace(codeJob, "  test-typescript:\n    needs: [changes]\n"),
			"is tagged code in " + ciGateJob + " but its `if:` is \"\""},
		"code job without changes in needs": {replace(codeJob,
			"  test-typescript:\n    if: "+codeTierIf+"\n    needs: [build]\n"),
			"code-tier job test-typescript does not need changes"},
		"code job with no needs": {replace(codeJob, "  test-typescript:\n    if: "+codeTierIf+"\n"),
			"code-tier job test-typescript does not need changes"},
		"DOCS_ONLY missing": {replace("          DOCS_ONLY: "+codeTierGateEnv+"\n", ""),
			"'s DOCS_ONLY is \"\""},
		"DOCS_ONLY differs": {replace("DOCS_ONLY: "+codeTierGateEnv, "DOCS_ONLY: ${{ github.event_name == 'pull_request' }}"),
			"'s DOCS_ONLY is "},
		"pr job needs a code job": {replace("  build:\n    steps:", "  build:\n    needs: [test-typescript]\n    steps:"),
			"pr-tier job build needs code-tier job test-typescript"},
		"code job needs a full job": {replace("    needs: [changes]\n", "    needs: [changes, integration-x]\n"),
			"code-tier job test-typescript needs full-tier job integration-x"},
		"code job needs a nightly job": {replace("    needs: [changes]\n", "    needs: [changes, soak-x]\n"),
			"code-tier job test-typescript needs nightly-tier job soak-x"},
		"code job tagged pr": {replace(`"$RESULT_TEST_TYPESCRIPT" code`, `"$RESULT_TEST_TYPESCRIPT" pr`),
			"is a code-tier job tagged pr"},
		"pr job tagged code": {replace(`"$RESULT_CHANGES" pr`, `"$RESULT_CHANGES" code`),
			"job changes is a PR-tier or full-tier job tagged code"},
		"full job tagged code": {replace(`"$RESULT_INTEGRATION_X" full`, `"$RESULT_INTEGRATION_X" code`),
			"job integration-x is a PR-tier or full-tier job tagged code"},
		"code job missing from needs": {replace("changes, test-typescript, integration-x", "changes, integration-x"),
			ciGateJob + " does not list test-typescript in needs"},
	}
	for name, tc := range codeCases {
		t.Run(name, func(t *testing.T) {
			p := ciTierProblems(parseWorkflow(t, tc.body))
			if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, tc.want) }) {
				t.Fatalf("want a problem containing %q, got %q", tc.want, p)
			}
		})
	}

	// fips (#20) on the PR tier: no `if:` and tagged pr is self-consistent to
	// every other guard, so only fullTierJob's name rule can catch it.
	fipsOnPR := strings.Replace(replace("  fips:\n    if: "+fullTierIf+"\n", "  fips:\n"),
		`"$RESULT_FIPS" full`, `"$RESULT_FIPS" pr`, 1)
	want := "job " + fipsJob + " is tagged pr but belongs to the full tier"
	if p := ciTierProblems(parseWorkflow(t, fipsOnPR)); !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, want) }) {
		t.Fatalf("fips job on the PR tier: want a problem containing %q, got %q", want, p)
	}
}

// runGate executes ci-gate's script under bash the way a runner does, with
// every RESULT_ variable set from results (default success), FULL_TIER,
// NIGHTLY and DOCS_ONLY.
func runGate(t *testing.T, run string, env map[string]string, results map[string]string, fullTier, nightly, docsOnly string) int {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required to execute %s's script: %v", ciGateJob, err)
	}
	vars := []string{"PATH=" + os.Getenv("PATH"), "FULL_TIER=" + fullTier, "NIGHTLY=" + nightly, "DOCS_ONLY=" + docsOnly}
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
// only for a full-tier job outside a full-tier run or a nightly job outside a
// nightly run; everything else that is not success fails, cancelled included.
func TestCIGateScriptHonoursTheTiers(t *testing.T) {
	wf := readCIWorkflow(t)
	run, env, ok := gateScript(wf)
	if !ok {
		t.Fatalf("ci.yml has no %s step defining check()", ciGateJob)
	}
	checks := gateChecks(run)
	var full, pr, code, nightly []string
	for name, c := range checks {
		switch c.tier {
		case tierFull:
			full = append(full, name)
		case tierPR:
			pr = append(pr, name)
		case tierCode:
			code = append(code, name)
		case tierNightly:
			nightly = append(nightly, name)
		}
	}
	if len(full) == 0 || len(pr) == 0 || len(code) == 0 || len(nightly) == 0 {
		t.Fatalf("%s has %d full, %d pr, %d code and %d nightly check lines; this test needs all four",
			ciGateJob, len(full), len(pr), len(code), len(nightly))
	}
	sort.Strings(full)
	sort.Strings(pr)
	sort.Strings(code)
	sort.Strings(nightly)
	// The per-feature matrix reaches the gate as one result: the runner
	// aggregates its legs into needs.<job>.result, which is failure when any
	// leg fails and cancelled when a leg is cancelled. The cases below that
	// name featureJob are one red leg as the gate sees it.
	if c, found := checks[featureJob]; !found || c.tier != tierFull {
		t.Fatalf("%s has no full-tier check line for %s; the per-feature legs would not gate", ciGateJob, featureJob)
	}
	// fips (#20) is a required full-tier gate: its cases below name it rather
	// than full[0], which is whichever full job sorts first.
	if c, found := checks[fipsJob]; !found || c.tier != tierFull {
		t.Fatalf("%s has no full-tier check line for %s; a FIPS 140-3 failure would not gate", ciGateJob, fipsJob)
	}
	all := func(jobs []string, result string) map[string]string {
		m := map[string]string{}
		for _, j := range jobs {
			m[j] = result
		}
		return m
	}
	// A plain PR selects neither tier, a push to main the full tier alone,
	// and a workflow_dispatch both; schedule never fires here (no cron).
	allFull := func(result string) map[string]string { return all(full, result) }
	allNightly := func(result string) map[string]string { return all(nightly, result) }
	offTier := func(result string) map[string]string {
		m := allFull(result)
		for j, r := range allNightly(result) {
			m[j] = r
		}
		return m
	}

	cases := []struct {
		name     string
		results  map[string]string
		fullTier string
		nightly  string
		wantExit bool
	}{
		{"everything succeeded, dispatch", nil, "true", "true", false},
		{"everything succeeded, full tier", nil, "true", "false", false},
		{"everything succeeded, PR tier", nil, "false", "false", false},
		{"full and nightly skipped on a PR-tier run", offTier("skipped"), "false", "false", false},
		{"full tier skipped on a full-tier run", allFull("skipped"), "true", "false", true},
		{"one full job skipped on a full-tier run", map[string]string{full[0]: "skipped"}, "true", "false", true},
		{"full job failed on a PR-tier run", map[string]string{full[0]: "failure"}, "false", "false", true},
		{"full job cancelled on a PR-tier run", map[string]string{full[0]: "cancelled"}, "false", "false", true},
		{"full job cancelled on a full-tier run", map[string]string{full[0]: "cancelled"}, "true", "false", true},
		{"one feature leg failed on a full-tier run", map[string]string{featureJob: "failure"}, "true", "false", true},
		{"one feature leg cancelled", map[string]string{featureJob: "cancelled"}, "true", "false", true},
		{"feature legs skipped on a full-tier run", map[string]string{featureJob: "skipped"}, "true", "false", true},
		{"feature legs skipped on a PR-tier run", map[string]string{featureJob: "skipped"}, "false", "false", false},
		{"fips failed on a full-tier run", map[string]string{fipsJob: "failure"}, "true", "false", true},
		{"fips cancelled on a full-tier run", map[string]string{fipsJob: "cancelled"}, "true", "false", true},
		{"fips skipped on a full-tier run", map[string]string{fipsJob: "skipped"}, "true", "false", true},
		{"fips skipped on a PR-tier run", map[string]string{fipsJob: "skipped"}, "false", "false", false},
		{"nightly skipped on a main push", allNightly("skipped"), "true", "false", false},
		{"nightly skipped on a dispatch", allNightly("skipped"), "true", "true", true},
		{"nightly failed on a dispatch", map[string]string{nightly[0]: "failure"}, "true", "true", true},
		{"nightly cancelled on a dispatch", map[string]string{nightly[0]: "cancelled"}, "true", "true", true},
		{"nightly failed off the nightly tier", map[string]string{nightly[0]: "failure"}, "true", "false", true},
		{"nightly skip is not a full-tier skip", allNightly("skipped"), "false", "true", true},
		{"full skip is not a nightly skip", allFull("skipped"), "true", "true", true},
		{"PR job skipped on a PR-tier run", map[string]string{pr[0]: "skipped"}, "false", "false", true},
		{"PR job cancelled", map[string]string{pr[0]: "cancelled"}, "false", "false", true},
		{"PR job failed", map[string]string{pr[0]: "failure"}, "true", "false", true},
		{"PR job result unreadable", map[string]string{pr[0]: ""}, "false", "false", true},
		{"FULL_TIER unreadable", nil, "", "false", true},
		{"FULL_TIER not a boolean", allFull("skipped"), "maybe", "false", true},
		{"NIGHTLY unreadable", nil, "false", "", true},
		{"NIGHTLY not a boolean", allNightly("skipped"), "false", "maybe", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exit := runGate(t, run, env, tc.results, tc.fullTier, tc.nightly, "false")
			if tc.wantExit && exit == 0 {
				t.Fatalf("%s passed; want it to fail", ciGateJob)
			}
			if !tc.wantExit && exit != 0 {
				t.Fatalf("%s failed with exit %d; want it to pass", ciGateJob, exit)
			}
		})
	}

	// The code tier (DS-3160): a docs-only pull request selects neither the
	// full nor the nightly tier and skips every code job. Only DOCS_ONLY=true
	// excuses a code skip, and it excuses nothing else.
	allCode := func(result string) map[string]string { return all(code, result) }
	docsOnlyRun := func() map[string]string {
		m := offTier("skipped")
		for j, r := range allCode("skipped") {
			m[j] = r
		}
		return m
	}
	codeCases := []struct {
		name     string
		results  map[string]string
		fullTier string
		docsOnly string
		wantExit bool
	}{
		{"code jobs skipped on a docs-only PR", allCode("skipped"), "false", "true", false},
		{"a docs-only PR as the runner sees it", docsOnlyRun(), "false", "true", false},
		{"everything succeeded on a docs-only PR", nil, "false", "true", false},
		{"code jobs skipped on a code PR", allCode("skipped"), "false", "false", true},
		{"one code job skipped on a code PR", map[string]string{code[0]: "skipped"}, "false", "false", true},
		{"code jobs skipped on a full-tier run", allCode("skipped"), "true", "false", true},
		{"code job failed on a docs-only PR", map[string]string{code[0]: "failure"}, "false", "true", true},
		{"code job cancelled on a docs-only PR", map[string]string{code[0]: "cancelled"}, "false", "true", true},
		{"PR job skipped on a docs-only PR", map[string]string{pr[0]: "skipped"}, "false", "true", true},
		{"docs-only does not excuse a full-tier skip", allFull("skipped"), "true", "true", true},
		{"code jobs skipped, DOCS_ONLY unreadable", allCode("skipped"), "false", "", true},
		{"DOCS_ONLY unreadable", nil, "false", "", true},
		{"DOCS_ONLY not a boolean", allCode("skipped"), "false", "maybe", true},
	}
	for _, tc := range codeCases {
		t.Run(tc.name, func(t *testing.T) {
			exit := runGate(t, run, env, tc.results, tc.fullTier, "false", tc.docsOnly)
			if tc.wantExit && exit == 0 {
				t.Fatalf("%s passed; want it to fail", ciGateJob)
			}
			if !tc.wantExit && exit != 0 {
				t.Fatalf("%s failed with exit %d; want it to pass", ciGateJob, exit)
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
	// ci.yml takes ci-template v1.25's pair (Company Agent m701, m857); the other
	// pull_request workflows keep v1.22.1's: a PR ref's newer run cancels
	// the older, and a run on main is never cancelled.
	wantGroup, wantCancel := refKeyedGroup, cancelOffMain
	if name == ciWorkflowName {
		wantGroup, wantCancel = mainKeyedGroup, cancelLiteral
	}
	if group != wantGroup {
		problems = append(problems, name+" runs on pull_request but its concurrency group is "+
			strconv.Quote(group)+", not "+wantGroup+" (devops.md §9)")
	}
	if cancel != wantCancel {
		problems = append(problems, name+" runs on pull_request but cancel-in-progress is "+
			strconv.Quote(cancel)+", not "+wantCancel+" (devops.md §9)")
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
	const good = "on:\n  pull_request:\n  workflow_dispatch:\nconcurrency:\n  group: " + refKeyedGroup + "\n  cancel-in-progress: " + cancelOffMain + "\n"
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
	const ciGood = "on:\n  pull_request:\n  workflow_dispatch:\nconcurrency:\n  group: " + mainKeyedGroup + "\n  cancel-in-progress: " + cancelLiteral + "\n"
	if p := workflowTriggerProblems(ciWorkflowName, parse(ciGood)); len(p) != 0 {
		t.Fatalf("the valid %s fixture must have no problems, got %v", ciWorkflowName, p)
	}
	type fixture struct{ file, body string }
	cases := map[string]fixture{
		"schedule":                {"x.yml", "on:\n  schedule:\n    - cron: '0 3 * * *'\n  workflow_dispatch:\n"},
		"no concurrency":          {"x.yml", "on:\n  pull_request:\n"},
		"group not ref-keyed":     {"x.yml", strings.Replace(good, refKeyedGroup, "${{ github.workflow }}", 1)},
		"cancel-in-progress off":  {"x.yml", strings.Replace(good, cancelOffMain, "false", 1)},
		"cancels on main too":     {"x.yml", strings.Replace(good, cancelOffMain, "true", 1)},
		"cancel-in-progress gone": {"x.yml", strings.Replace(good, "  cancel-in-progress: "+cancelOffMain+"\n", "", 1)},
		"other workflow on v1.23": {"x.yml", ciGood},
		"ci.yml cancel expression": {ciWorkflowName,
			strings.Replace(ciGood, "cancel-in-progress: "+cancelLiteral, "cancel-in-progress: "+cancelOffMain, 1)},
		"ci.yml main group per commit": {ciWorkflowName,
			strings.Replace(ciGood, "format('main-{0}', github.event_name)", "format('main-{0}-{1}', github.event_name, github.sha)", 1)},
		"ci.yml v1.22.1 group":  {ciWorkflowName, strings.Replace(ciGood, mainKeyedGroup, refKeyedGroup, 1)},
		"ci.yml v1.22.1 block":  {ciWorkflowName, good},
		"ci.yml no concurrency": {ciWorkflowName, "on:\n  pull_request:\n"},
		"ci.yml cancel false":   {ciWorkflowName, strings.Replace(ciGood, "cancel-in-progress: "+cancelLiteral, "cancel-in-progress: false", 1)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.file == ciWorkflowName && c.body == ciGood {
				t.Fatal("the mutation left the valid fixture unchanged")
			}
			if p := workflowTriggerProblems(c.file, parse(c.body)); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
