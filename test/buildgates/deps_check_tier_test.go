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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lintJob is the job that runs `make lint` on every run, a docs-only pull
// request included (#1046 splits it out of the template's image job).
const lintJob = "lint"

// scansWorkflowName is the workflow that runs the advisory checks on main.
const scansWorkflowName = "scans.yml"

// advisoryRuns are the commands whose answer changes with no change to the
// tree: they read the module proxy or an advisory database. ci-template
// v1.42 (owner ruling 742 (1)) runs them in scans.yml only.
var advisoryRuns = []string{"make deps-check", "make security", "make trivy-image"}

// gosecRun is the tree-only static analysis that stays in the CI Gate.
const gosecRun = "make gosec"

// hermeticGateProblems checks ci-template v1.42's hermetic gate (ruling 742
// (1)): no ci.yml job runs an advisory check, so the CI Gate's outcome
// depends only on the tree under test; the lint job runs `make gosec` on
// every tier and does not skip a docs-only pull request; and scans.yml runs
// every advisory check, on no pull_request or push trigger.
func hermeticGateProblems(ci, scans ciWorkflow) []string {
	var problems []string
	for name, job := range ci.Jobs {
		for _, s := range job.Steps {
			for _, cmd := range advisoryRuns {
				if strings.Contains(s.Run, cmd) {
					problems = append(problems, fmt.Sprintf("ci.yml job %s runs %s; ci-template v1.42 runs it in %s only", name, cmd, scansWorkflowName))
				}
			}
		}
	}
	lint, ok := ci.Jobs[lintJob]
	if !ok {
		problems = append(problems, "ci.yml has no "+lintJob+" job")
	} else {
		if strings.Contains(lint.If, "docs_only") {
			problems = append(problems, fmt.Sprintf("job %s's `if:` %q skips a docs-only pull request", lintJob, lint.If))
		}
		gosecAt := -1
		for i, s := range lint.Steps {
			if strings.Contains(s.Run, gosecRun) {
				gosecAt = i
				break
			}
		}
		switch {
		case gosecAt < 0:
			problems = append(problems, fmt.Sprintf("job %s has no %s step", lintJob, gosecRun))
		case strings.Contains(lint.Steps[gosecAt].If, "CI_FULL_TIER"):
			problems = append(problems, fmt.Sprintf("job %s's %s step depends on the tier (`if:` %q); v1.42 runs it on every event", lintJob, gosecRun, lint.Steps[gosecAt].If))
		}
	}
	for _, trigger := range onTriggers(scans.On) {
		if trigger == "pull_request" || trigger == "pull_request_target" || trigger == "push" {
			problems = append(problems, fmt.Sprintf("%s runs on %s; its findings must never gate a change", scansWorkflowName, trigger))
		}
	}
	for _, cmd := range advisoryRuns {
		found := false
		for _, job := range scans.Jobs {
			for _, s := range job.Steps {
				found = found || strings.Contains(s.Run, cmd)
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s does not run %s; nothing would run it", scansWorkflowName, cmd))
		}
	}
	return problems
}

func readScansWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", scansWorkflowName))
	if err != nil {
		t.Fatalf("read %s: %v", scansWorkflowName, err)
	}
	return parseWorkflow(t, string(b))
}

func TestCIGateIsHermetic(t *testing.T) {
	if p := hermeticGateProblems(readCIWorkflow(t), readScansWorkflow(t)); len(p) != 0 {
		t.Fatalf("ci.yml and scans.yml depart from ci-template v1.42's hermetic gate:\n  %s", strings.Join(p, "\n  "))
	}
}

const hermeticCIFixture = `
jobs:
  lint:
    if: ${{ !github.event.pull_request.draft }}
    steps:
      - name: make lint
        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" "make lint GO_BUILD_TAGS='$GO_BUILD_TAGS'"
      - name: make gosec
        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' && needs.preflight.outputs.docs_only != 'true' }}
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" "make gosec GO_BUILD_TAGS='$GO_BUILD_TAGS'"
  image:
    if: ${{ (needs.preflight.outputs.docs_only != 'true') && !github.event.pull_request.draft }}
    steps:
      - name: make build-server build-cli-headless
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" "make build-server"
`

const hermeticScansFixture = `
on:
  workflow_dispatch:
jobs:
  scans:
    steps:
      - name: Scans
        run: |
          scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make deps-check
          scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" "make security GO_BUILD_TAGS='$GO_BUILD_TAGS'"
          scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make trivy-image TRIVY_IMAGE_INPUT=it-image.tar
`

func TestHermeticGateProblems_CanFail(t *testing.T) {
	if p := hermeticGateProblems(parseWorkflow(t, hermeticCIFixture), parseWorkflow(t, hermeticScansFixture)); len(p) != 0 {
		t.Fatalf("the valid fixtures must have no problems, got %v", p)
	}
	edit := func(fixture, old, new string) string {
		if !strings.Contains(fixture, old) {
			t.Fatalf("fixture has no %q; the case would test nothing", old)
		}
		return strings.Replace(fixture, old, new, 1)
	}
	gosecIf := "        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' && needs.preflight.outputs.docs_only != 'true' }}\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" \"make gosec"
	depsStep := "      - name: make deps-check\n        env:\n          IN_IMAGE_NETWORK: bridge\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make deps-check\n"
	securityStep := "      - name: make security\n        if: ${{ env.CI_FULL_TIER == 'true' }}\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make security\n"
	cases := map[string]struct{ ci, scans, want string }{
		// v1.28.0's placement, which failed pull requests on a new advisory.
		"deps-check in lint": {edit(hermeticCIFixture, "  image:\n", depsStep+"  image:\n"), hermeticScansFixture,
			"ci.yml job lint runs make deps-check"},
		"security in image, full tier": {hermeticCIFixture + securityStep, hermeticScansFixture,
			"ci.yml job image runs make security"},
		"trivy-image in image": {hermeticCIFixture + "      - name: trivy image\n        run: make trivy-image TRIVY_IMAGE_INPUT=it-image.tar\n", hermeticScansFixture,
			"ci.yml job image runs make trivy-image"},
		"no lint job": {edit(hermeticCIFixture, "  lint:\n", "  lint-x:\n"), hermeticScansFixture, "has no lint job"},
		"lint skips docs-only": {edit(hermeticCIFixture, "    if: ${{ !github.event.pull_request.draft }}", "    if: ${{ needs.preflight.outputs.docs_only != 'true' }}"),
			hermeticScansFixture, "skips a docs-only pull request"},
		"no gosec step": {edit(hermeticCIFixture, "make gosec GO_BUILD_TAGS", "make vet GO_BUILD_TAGS"), hermeticScansFixture, "has no make gosec step"},
		"gosec on the PR tier only": {edit(hermeticCIFixture, gosecIf, strings.Replace(gosecIf, "&& needs.", "&& env.CI_FULL_TIER != 'true' && needs.", 1)),
			hermeticScansFixture, "depends on the tier"},
		"scans on pull_request": {hermeticCIFixture, edit(hermeticScansFixture, "  workflow_dispatch:\n", "  workflow_dispatch:\n  pull_request:\n"),
			"scans.yml runs on pull_request"},
		"scans on push": {hermeticCIFixture, edit(hermeticScansFixture, "  workflow_dispatch:\n", "  workflow_dispatch:\n  push:\n"),
			"scans.yml runs on push"},
		"scans without deps-check": {hermeticCIFixture, edit(hermeticScansFixture, "make deps-check\n", "make build\n"),
			"scans.yml does not run make deps-check"},
		"scans without trivy-image": {hermeticCIFixture, edit(hermeticScansFixture, "make trivy-image", "make build"),
			"scans.yml does not run make trivy-image"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := hermeticGateProblems(parseWorkflow(t, tc.ci), parseWorkflow(t, tc.scans))
			if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, tc.want) }) {
				t.Fatalf("want a problem containing %q, got %q", tc.want, p)
			}
		})
	}
}
