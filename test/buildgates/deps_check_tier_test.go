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
	"slices"
	"strings"
	"testing"
)

// lintJob is the job that runs `make lint` on every run, a docs-only pull
// request included (#1046 splits it out of the template's image job).
const lintJob = "lint"

// depsCheckRun is the command ci-template v1.28.0's deps-check step runs.
const depsCheckRun = "make deps-check"

// depsCheckProblems checks ci-template v1.28.0 (ruling 219(3)): `make
// deps-check` is a lint-class gate that runs wherever `make lint` runs. Here
// that is exactly one step of the lint job, after `make lint`, with no
// full-tier condition and the bridge network for the module proxy; no other
// job runs it, so the code-tier image job cannot hide it from a docs-only
// pull request.
func depsCheckProblems(w ciWorkflow) []string {
	var problems []string
	for name, job := range w.Jobs {
		if name == lintJob {
			continue
		}
		for _, s := range job.Steps {
			if strings.Contains(s.Run, depsCheckRun) {
				problems = append(problems, fmt.Sprintf("job %s runs %s; it belongs to the %s job, which a docs-only pull request runs", name, depsCheckRun, lintJob))
			}
		}
	}
	lint, ok := w.Jobs[lintJob]
	if !ok {
		return append(problems, "ci.yml has no "+lintJob+" job")
	}
	if strings.Contains(lint.If, "docs_only") {
		problems = append(problems, fmt.Sprintf("job %s's `if:` %q skips a docs-only pull request", lintJob, lint.If))
	}
	lintAt, depsAt := -1, -1
	for i, s := range lint.Steps {
		switch {
		case strings.Contains(s.Run, depsCheckRun):
			if depsAt >= 0 {
				problems = append(problems, fmt.Sprintf("job %s runs %s twice", lintJob, depsCheckRun))
			}
			depsAt = i
			if strings.Contains(s.If, "CI_FULL_TIER") {
				problems = append(problems, fmt.Sprintf("job %s's %s step is full tier only (`if:` %q); v1.28.0 runs it on every event", lintJob, depsCheckRun, s.If))
			}
			if s.Env["IN_IMAGE_NETWORK"] != "bridge" {
				problems = append(problems, fmt.Sprintf("job %s's %s step has IN_IMAGE_NETWORK %q, want bridge for the module proxy", lintJob, depsCheckRun, s.Env["IN_IMAGE_NETWORK"]))
			}
		case strings.Contains(s.Run, "make lint ") || strings.Contains(s.Run, "make lint\""):
			lintAt = i
		}
	}
	switch {
	case depsAt < 0:
		problems = append(problems, fmt.Sprintf("job %s has no %s step", lintJob, depsCheckRun))
	case lintAt < 0:
		problems = append(problems, fmt.Sprintf("job %s has no make lint step", lintJob))
	case depsAt < lintAt:
		problems = append(problems, fmt.Sprintf("job %s runs %s before make lint", lintJob, depsCheckRun))
	}
	return problems
}

func TestDepsCheckRunsWhereLintRuns(t *testing.T) {
	if p := depsCheckProblems(readCIWorkflow(t)); len(p) != 0 {
		t.Fatalf("ci.yml's deps-check step departs from ci-template v1.28.0:\n  %s", strings.Join(p, "\n  "))
	}
}

const depsCheckFixture = `
jobs:
  lint:
    if: ${{ !github.event.pull_request.draft }}
    steps:
      - name: make lint
        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" "make lint GO_BUILD_TAGS='$GO_BUILD_TAGS'"
      - name: make deps-check
        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}
        env:
          IN_IMAGE_NETWORK: bridge
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make deps-check
  image:
    if: ${{ (needs.changes.outputs.docs_only != 'true') && !github.event.pull_request.draft }}
    steps:
      - name: make security
        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' && env.CI_FULL_TIER == 'true' }}
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make security
`

func TestDepsCheckProblems_CanFail(t *testing.T) {
	if p := depsCheckProblems(parseWorkflow(t, depsCheckFixture)); len(p) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", p)
	}
	replace := func(old, new string) string {
		if !strings.Contains(depsCheckFixture, old) {
			t.Fatalf("fixture has no %q; the case would test nothing", old)
		}
		return strings.Replace(depsCheckFixture, old, new, 1)
	}
	lintStep := "      - name: make lint\n        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" \"make lint GO_BUILD_TAGS='$GO_BUILD_TAGS'\"\n"
	depsStep := "      - name: make deps-check\n        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}\n        env:\n          IN_IMAGE_NETWORK: bridge\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make deps-check\n"
	imageSecurity := "        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make security\n"
	cases := map[string]struct{ body, want string }{
		// v1.27.0's placement: full tier only, in the code-tier image job.
		"v1.27.0 image step": {strings.Replace(replace(depsStep, ""), imageSecurity,
			imageSecurity+strings.Replace(depsStep, "env.TOOLCHAIN_IMAGE != '' }}", "env.TOOLCHAIN_IMAGE != '' && env.CI_FULL_TIER == 'true' }}", 1), 1),
			"job image runs make deps-check"},
		"no deps-check step": {replace(depsStep, ""), "job lint has no make deps-check step"},
		"full tier only": {replace("        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' }}\n        env:",
			"        if: ${{ !cancelled() && env.TOOLCHAIN_IMAGE != '' && env.CI_FULL_TIER == 'true' }}\n        env:"),
			"is full tier only"},
		"no network":  {replace("        env:\n          IN_IMAGE_NETWORK: bridge\n", ""), "want bridge"},
		"before lint": {replace(lintStep+depsStep, depsStep+lintStep), "before make lint"},
		"twice":       {replace(depsStep, depsStep+depsStep), "twice"},
		"lint skips docs-only": {replace("    if: ${{ !github.event.pull_request.draft }}",
			"    if: ${{ needs.changes.outputs.docs_only != 'true' }}"), "skips a docs-only pull request"},
		"no lint job":       {replace("  lint:\n", "  lint-x:\n"), "has no lint job"},
		"no make lint step": {replace(lintStep, ""), "has no make lint step"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := depsCheckProblems(parseWorkflow(t, tc.body))
			if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, tc.want) }) {
				t.Fatalf("want a problem containing %q, got %q", tc.want, p)
			}
		})
	}
}
