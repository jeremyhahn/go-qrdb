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

// CI runs the self-spawned integration suites (test/integration) as one
// job per feature: ci.yml's integration-features
// job is a matrix whose legs each run `make integration-test IT_FEATURE=<feature>`
// -- the selection `make integration-test-<feature>` runs on a host, in the
// integration image -- under their own limit, in parallel (owner ruling 10,
// 2026-10-14). The legs are
// every feature test/integration/features.mk maps at least one file to;
// `make -s print-integration-features` prints them.
//
// Gitea cannot expand a matrix computed at run time (handbook devops.md §1),
// so the list is literal in ci.yml, and this gate is what keeps it equal to
// the Makefile's. Each property below is one edit from going false without
// anything else noticing:
//
//   - a feature mapped in features.mk but absent from the matrix: its tests
//     never run in CI, and feature_integration_coverage_test.go still passes,
//     because it checks the mapping, not the jobs;
//   - a leg whose features.mk entry is empty: it runs zero tests and passes;
//   - a limit above 45 minutes, or none at all (GitHub's default is 360);
//   - fail-fast left at its default, true, so one red leg cancels the others
//     and hides whether they would have passed;
//   - a leg that runs something other than the per-feature target, or a job
//     that leaves the full tier or stops running on push;
//   - a leg with no always() teardown, so a cancelled leg leaves its compose
//     project and image on the runner;
//   - a leg whose project or image name another leg or run shares, or that
//     the reap step's prefix does not cover.
//
// That the gate goes red when one leg fails is ci_tiers_test.go's
// TestCIGateScriptHonoursTheTiers: a matrix is one needs entry, and its
// result is failure when any leg fails.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	// featureJob is ci.yml's per-feature integration job.
	featureJob = "integration-features"

	// featureJobLimit is each leg's timeout-minutes ceiling (ruling 10: the
	// 45-minute limit is not raised).
	featureJobLimit = 45

	// featureMatrixKey is the job's one matrix axis.
	featureMatrixKey = "feature"

	// featureReapPrefix is the name prefix every leg's stack and image carry,
	// and the one featureReap removes stale leftovers under.
	featureReapPrefix = "qrdb-ci-"

	// featureReap is each leg's first step: it removes stacks a killed run
	// left on the runner's daemon, so they do not collide with this one.
	featureReap = "scripts/reap-stale-ci-stacks.sh " + featureReapPrefix

	// featureStep is the one test command each leg runs: the feature's
	// features.mk selection, in the integration image, under the leg's names.
	featureStep = `make integration-test IT_FEATURE=${{ matrix.feature }} IT_PROJECT="$IT_PROJECT" IT_IMAGE="$IT_IMAGE"`

	// featureTeardown is the leg's last step, run under always(): it removes
	// the compose project and image featureStep ran under, so a cancelled leg
	// leaves nothing on the runner.
	featureTeardown = `make docker-down-it IT_PROJECT="$IT_PROJECT" IT_IMAGE="$IT_IMAGE"`

	// featureLegFloor is a discovery floor: 40 features map files today, and
	// a parse that finds fewer than this has found too little to check.
	featureLegFloor = 30
)

// featureWorkflow is the slice of ci.yml this gate reads.
type featureWorkflow struct {
	On   yaml.Node `yaml:"on"`
	Jobs map[string]struct {
		If             string            `yaml:"if"`
		TimeoutMinutes *int              `yaml:"timeout-minutes"`
		Env            map[string]string `yaml:"env"`
		Strategy       struct {
			FailFast *bool     `yaml:"fail-fast"`
			Matrix   yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Name string `yaml:"name"`
			If   string `yaml:"if"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// featureMatrixLine is the `feature:` line ci.yml must carry for want.
func featureMatrixLine(want []string) string {
	return featureMatrixKey + ": [" + strings.Join(want, ", ") + "]"
}

// featureJobProblems checks ci.yml's integration-features job against want,
// the features that must each be one leg.
func featureJobProblems(body []byte, want []string) []string {
	var wf featureWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml is not a workflow this gate can read: " + err.Error()}
	}
	var problems []string
	triggers := onTriggers(wf.On)
	for _, ev := range []string{"push", "pull_request"} {
		if !slices.Contains(triggers, ev) {
			problems = append(problems, "ci.yml does not run on "+ev+": "+featureJob+" would not gate it")
		}
	}
	job, ok := wf.Jobs[featureJob]
	if !ok {
		return append(problems, "ci.yml has no "+featureJob+" job: no feature's integration tests run in CI")
	}
	if strings.TrimSpace(job.If) != fullTierIf {
		problems = append(problems, featureJob+"'s if: is "+strconv.Quote(job.If)+"; want the full-tier condition")
	}
	switch tm := job.TimeoutMinutes; {
	case tm == nil:
		problems = append(problems, featureJob+" has no timeout-minutes: a leg would get the runner's default, not its own limit")
	case *tm <= 0 || *tm > featureJobLimit:
		problems = append(problems, featureJob+"'s timeout-minutes is "+strconv.Itoa(*tm)+
			"; want 1.."+strconv.Itoa(featureJobLimit)+": the 45-minute limit is not raised")
	}
	if ff := job.Strategy.FailFast; ff == nil || *ff {
		problems = append(problems, featureJob+" does not set fail-fast: false: one red leg would cancel the rest")
	}
	problems = append(problems, featureMatrixProblems(job.Strategy.Matrix, want)...)

	var runs, conds []string
	for _, s := range job.Steps {
		// The credential mask (ci-template v1.21.1) leads the leg; its shape is
		// ci_logs_test.go's to check. Only a first run step that runs no make
		// target is taken for it, so the name cannot carry a narrowed run past
		// this gate.
		if len(runs) == 0 && s.Name == maskStepName && !strings.Contains(s.Run, "make ") {
			continue
		}
		if s.Run != "" {
			runs = append(runs, strings.TrimSpace(s.Run))
			conds = append(conds, strings.TrimSpace(s.If))
		}
	}
	if !slices.Equal(runs, []string{featureReap, featureStep, featureTeardown}) {
		problems = append(problems, featureJob+" runs "+strconv.Quote(strings.Join(runs, "; "))+
			"; want exactly `"+featureReap+"`, `"+featureStep+"`, then `"+featureTeardown+"`")
	} else if conds[0] != "" || conds[1] != "" || conds[2] != "always()" {
		problems = append(problems, featureJob+"'s steps run under if: "+strconv.Quote(strings.Join(conds, ", "))+
			"; want the reap and the tests unconditional and the teardown under always(), "+
			"so a failed or cancelled leg still removes its stack")
	}
	// The names the steps pass must be this leg's alone: legs of one run, and
	// runs sharing a daemon, would otherwise share a stack or an image tag.
	for _, name := range []string{"IT_PROJECT", "IT_IMAGE"} {
		v := job.Env[name]
		if !strings.HasPrefix(v, featureReapPrefix) {
			problems = append(problems, featureJob+"'s "+name+" is "+strconv.Quote(v)+
				"; want it under "+strconv.Quote(featureReapPrefix)+", the prefix the reap step removes")
		}
		for _, part := range []string{"${{ github.run_id }}", "${{ github.run_attempt }}", "${{ matrix.feature }}"} {
			if !strings.Contains(v, part) {
				problems = append(problems, featureJob+"'s "+name+" is "+strconv.Quote(v)+
					"; want it to carry "+part+", so no other leg or run shares it")
			}
		}
	}
	return problems
}

// featureMatrixProblems checks that the matrix has the one feature axis and
// that it lists exactly want, in order.
func featureMatrixProblems(m yaml.Node, want []string) []string {
	if m.Kind != yaml.MappingNode {
		return []string{featureJob + " has no matrix mapping; want " + featureMatrixLine(want)}
	}
	var problems []string
	var legs []string
	found := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i].Value, m.Content[i+1]
		if key != featureMatrixKey {
			problems = append(problems, featureJob+"'s matrix has key "+strconv.Quote(key)+
				"; want only "+featureMatrixKey+": any other key multiplies or edits the legs")
			continue
		}
		found = true
		if val.Kind != yaml.SequenceNode {
			problems = append(problems, featureJob+"'s matrix "+featureMatrixKey+" is not a literal list")
			continue
		}
		for _, c := range val.Content {
			legs = append(legs, c.Value)
		}
	}
	if !found {
		return append(problems, featureJob+"'s matrix has no "+featureMatrixKey+" axis; want "+featureMatrixLine(want))
	}
	if !slices.Equal(legs, want) {
		problems = append(problems, featureJob+"'s matrix is out of step with `make -s print-integration-features`; want\n"+
			featureMatrixLine(want)+"\ngot\n"+featureMatrixLine(legs))
	}
	return problems
}

// makefileFeatures returns `make -s print-integration-features`, the one
// source of the leg list.
func makefileFeatures(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("make", "-s", "print-integration-features")
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make -s print-integration-features: %v\n%s", err, errOut.String())
	}
	return strings.Fields(out.String())
}

func TestIntegrationFeatureCoverage_OneJobPerFeature(t *testing.T) {
	root := repoRoot(t)
	want := makefileFeatures(t, root)

	// The Makefile's list must be the features.mk entries that map a file,
	// read independently, so a broken INT_FEATURES cannot agree with itself.
	files, _ := featureMapping(t, root)
	mapped := make([]string, 0, len(files))
	for v := range files {
		mapped = append(mapped, strings.ReplaceAll(strings.TrimPrefix(v, intTestVarPrefix), "_", "-"))
	}
	sort.Strings(mapped)
	if !slices.Equal(want, mapped) {
		t.Fatalf("make -s print-integration-features printed %v; features.mk maps files for %v", want, mapped)
	}
	if len(want) < featureLegFloor {
		t.Fatalf("found %d features with integration tests, below the floor of %d: a gate that finds "+
			"nothing to check reports the same green as one that checked everything", len(want), featureLegFloor)
	}

	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	for _, p := range featureJobProblems(body, want) {
		t.Error(p)
	}
}

const featureJobFixture = `on:
  push:
    branches: [main]
  pull_request:
    types: [opened, synchronize, reopened]
jobs:
  integration-features:
    name: Integration (${{ matrix.feature }})
    if: github.event_name == 'push' || github.event_name == 'workflow_dispatch' || contains(github.event.pull_request.labels.*.name, 'ci:full')
    needs: [build]
    timeout-minutes: 45
    env:
      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.feature }}
      IT_IMAGE: qrdb-ci-it:${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.feature }}
    strategy:
      fail-fast: false
      matrix:
        feature: [audit, key-value, sql]
    steps:
      - uses: actions/checkout@0000000000000000000000000000000000000000 # v6
      - name: Mask the forge credentials
        run: echo "::add-mask::$GIT_CONFIG_VALUE_0"
      - name: Reap stale CI stacks
        run: scripts/reap-stale-ci-stacks.sh qrdb-ci-
      - name: Integration tests (${{ matrix.feature }})
        run: make integration-test IT_FEATURE=${{ matrix.feature }} IT_PROJECT="$IT_PROJECT" IT_IMAGE="$IT_IMAGE"
      - name: Tear down (${{ matrix.feature }})
        if: always()
        run: make docker-down-it IT_PROJECT="$IT_PROJECT" IT_IMAGE="$IT_IMAGE"
`

var featureFixtureWant = []string{"audit", "key-value", "sql"}

func TestIntegrationFeatureCoverage_JobProblemsValidFixture(t *testing.T) {
	if p := featureJobProblems([]byte(featureJobFixture), featureFixtureWant); len(p) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", p)
	}
	if got := featureMatrixLine(featureFixtureWant); got != "feature: [audit, key-value, sql]" {
		t.Fatalf("featureMatrixLine = %q; want the line ci.yml carries", got)
	}
}

func TestIntegrationFeatureCoverage_JobProblemsCanFail(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []string
	}{
		{name: "job missing", old: "  integration-features:\n", new: "  integration-other:\n"},
		{name: "a mapped feature has no leg", old: "[audit, key-value, sql]", new: "[audit, sql]"},
		{name: "a leg with no mapped feature", old: "[audit, key-value, sql]", new: "[audit, key-value, rate-limit, sql]"},
		{name: "legs out of order", old: "[audit, key-value, sql]", new: "[key-value, audit, sql]"},
		{name: "a duplicated leg", old: "[audit, key-value, sql]", new: "[audit, key-value, sql, sql]"},
		{name: "matrix list is an expression", old: "feature: [audit, key-value, sql]", new: "feature: ${{ fromJSON(needs.list.outputs.features) }}"},
		{name: "a second matrix axis", old: "        feature: [audit, key-value, sql]\n", new: "        feature: [audit, key-value, sql]\n        os: [a, b]\n"},
		{name: "an include", old: "        feature: [audit, key-value, sql]\n", new: "        feature: [audit, key-value, sql]\n        include:\n          - feature: extra\n"},
		{name: "no matrix", old: "      matrix:\n        feature: [audit, key-value, sql]\n", new: ""},
		{name: "limit raised", old: "timeout-minutes: 45", new: "timeout-minutes: 46"},
		{name: "limit zero", old: "timeout-minutes: 45", new: "timeout-minutes: 0"},
		{name: "no limit", old: "    timeout-minutes: 45\n", new: ""},
		{name: "fail-fast default", old: "      fail-fast: false\n", new: ""},
		{name: "fail-fast true", old: "fail-fast: false", new: "fail-fast: true"},
		{name: "left the full tier", old: "    if: github.event_name == 'push' || ", new: "    if: "},
		{name: "a fixed target", old: "IT_FEATURE=${{ matrix.feature }}", new: "IT_FEATURE=cluster"},
		{name: "the whole suite", old: " IT_FEATURE=${{ matrix.feature }}", new: ""},
		{name: "on the host", old: "run: make integration-test IT_FEATURE=${{ matrix.feature }} IT_PROJECT=\"$IT_PROJECT\" IT_IMAGE=\"$IT_IMAGE\"", new: "run: make integration-test-${{ matrix.feature }}"},
		{name: "a second run step", old: "        run: make integration-test IT_FEATURE=${{ matrix.feature }} IT_PROJECT=\"$IT_PROJECT\" IT_IMAGE=\"$IT_IMAGE\"\n", new: "        run: make integration-test IT_FEATURE=${{ matrix.feature }} IT_PROJECT=\"$IT_PROJECT\" IT_IMAGE=\"$IT_IMAGE\"\n      - run: make integration-test-auth\n"},
		{name: "a make run named as the mask", old: "run: echo \"::add-mask::$GIT_CONFIG_VALUE_0\"", new: "run: make integration-test-auth"},
		{name: "no reap", old: "      - name: Reap stale CI stacks\n        run: scripts/reap-stale-ci-stacks.sh qrdb-ci-\n", new: ""},
		{name: "reap of another prefix", old: "reap-stale-ci-stacks.sh qrdb-ci-", new: "reap-stale-ci-stacks.sh qrdb-"},
		{name: "no teardown", old: "      - name: Tear down (${{ matrix.feature }})\n        if: always()\n        run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\" IT_IMAGE=\"$IT_IMAGE\"\n", new: ""},
		{name: "teardown not always", old: "        if: always()\n", new: ""},
		{name: "teardown on success only", old: "if: always()", new: "if: success()"},
		{name: "teardown of the default names", old: "run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\" IT_IMAGE=\"$IT_IMAGE\"", new: "run: make docker-down-it"},
		{name: "tests made conditional", old: "      - name: Integration tests (${{ matrix.feature }})\n", new: "      - name: Integration tests (${{ matrix.feature }})\n        if: always()\n"},
		{name: "project shared by every leg", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.feature }}\n", new: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}\n"},
		{name: "image shared by every attempt", old: "qrdb-ci-it:${{ github.run_id }}-${{ github.run_attempt }}-", new: "qrdb-ci-it:${{ github.run_id }}-"},
		{name: "project outside the reaped prefix", old: "      IT_PROJECT: qrdb-ci-", new: "      IT_PROJECT: qrdb-it-"},
		{name: "no names", old: "    env:\n      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.feature }}\n      IT_IMAGE: qrdb-ci-it:${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.feature }}\n", new: ""},
		{name: "no push trigger", old: "  push:\n    branches: [main]\n", new: ""},
		{name: "no pull_request trigger", old: "  pull_request:\n    types: [opened, synchronize, reopened]\n", new: ""},
		{name: "want grew", want: []string{"audit", "key-value", "sql", "watch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, want := featureJobFixture, featureFixtureWant
			if tc.old != "" {
				body = strings.Replace(featureJobFixture, tc.old, tc.new, 1)
				if body == featureJobFixture {
					t.Fatal("the mutation did not apply: the case tests the valid fixture")
				}
			}
			if tc.want != nil {
				want = tc.want
			}
			if p := featureJobProblems([]byte(body), want); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
