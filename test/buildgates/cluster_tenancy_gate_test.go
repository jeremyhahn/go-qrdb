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

// The multi-node tenant suites gate every push (owner ruling 2026-10-02,
// strict isolation plan phase 0). Every later strict-isolation phase proves
// itself on real 3-node clusters, so a suite that runs only nightly would let
// a phase merge with its proof unexercised for up to a day.
//
// This gate keeps that true. It reads the selection from the Makefile, not a
// copy of it, and it asserts three things because each is one edit from
// silently going false:
//
//   - the selection still names tests: a regex that matches nothing makes the
//     job green while running nothing, so a discovery floor counts real test
//     functions in ./test/integration that it selects;
//   - every multi-node tenant suite is in it: the isolation_raft_* files are
//     the ones a strict phase extends, and dropping one from the regex would
//     not fail anything else;
//   - CI runs the target on push AND the result gate requires the job: a job
//     that can pass and is not required is a fail-open gate.
//
// Since the tier split (#674) the job is in ci.yml's full tier: every push to
// main, every workflow_dispatch, and a pull request labelled `ci:full`. A plain
// PR push no longer runs it, so a PR that touches tenant isolation carries
// `ci:full`; the push to main that lands it runs the suites regardless.

import (
	"bufio"
	"bytes"
	"go/build/constraint"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	tenancyTarget    = "integration-test-cluster-tenancy"
	tenancyJob       = "integration-cluster-tenancy"
	tenancyRunVar    = "CLUSTER_TENANCY_RUN"
	ciGateJob        = "ci-gate"
	buildJob         = "build"
	tenancyTestFloor = 20
)

// tenancyRunRegex returns the -run expression the Makefile declares.
func tenancyRunRegex(t *testing.T, root string) *regexp.Regexp {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	re := regexp.MustCompile(`(?m)^` + tenancyRunVar + `\s*:?=\s*(\S+)\s*$`)
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatalf("Makefile declares no %s := <regex>; the per-push tenancy job has nothing to select", tenancyRunVar)
	}
	sel, err := regexp.Compile(string(m[1]))
	if err != nil {
		t.Fatalf("%s is not a valid Go regexp: %v", tenancyRunVar, err)
	}
	if !strings.Contains(string(b), "\n"+tenancyTarget+":") {
		t.Fatalf("Makefile has no %s target", tenancyTarget)
	}
	return sel
}

// satisfiesIntegrationBuild reports whether src's build constraint is
// satisfied when the ONLY tag set is "integration", alongside the default
// GOOS/GOARCH (go-qrdb strict-isolation phase 0 review finding N3).
//
// A file with no `//go:build` line carries no constraint and so
// participates in every build, including +integration: it is satisfied
// vacuously. A scan rather than a full parse of the file: a build
// constraint comment must precede the package clause (the Go spec), so the
// first non-blank line that is not a "//" comment ends the search.
func satisfiesIntegrationBuild(src []byte) (bool, error) {
	ok := func(tag string) bool {
		return tag == "integration" || tag == runtime.GOOS || tag == runtime.GOARCH
	}
	scanner := bufio.NewScanner(bytes.NewReader(src))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			break
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, err
		}
		return expr.Eval(ok), nil
	}
	return true, nil
}

// clusterTests maps each top-level Test function in ./test/integration to the
// file that declares it, for files whose build constraint puts them in the
// +integration build -- the one `make integration-test-cluster-tenancy`
// actually runs. A file tagged `//go:build !integration` (or any other
// constraint the integration tag does not satisfy) never compiles into that
// binary and cannot select, so counting its Test functions toward the
// discovery floor or the per-suite check above would be counting tests that
// cannot run.
func clusterTests(t *testing.T, root string) map[string]string {
	t.Helper()
	dir := filepath.Join(root, "test", "integration")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fn := regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\)`)
	out := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		satisfied, err := satisfiesIntegrationBuild(b)
		if err != nil {
			t.Fatalf("parse build constraint in %s: %v", e.Name(), err)
		}
		if !satisfied {
			continue
		}
		for _, m := range fn.FindAllSubmatch(b, -1) {
			out[string(m[1])] = e.Name()
		}
	}
	return out
}

// TestClusterTests_RespectsBuildConstraints is the regression for go-qrdb
// strict-isolation phase 0 review finding N3: clusterTests counted every
// `func Test...` in ./test/integration by regex, with no regard for whether
// the file's build constraint actually puts it in the +integration build
// that `make integration-test-cluster-tenancy` runs. A file tagged
// `//go:build !integration` never compiles into that binary and cannot
// select, so counting it toward the discovery floor or the per-suite check
// above is counting a test that cannot run.
func TestClusterTests_RespectsBuildConstraints(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "test", "integration")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("included_test.go", "//go:build integration\n\npackage integration\n\nfunc TestIsolationRaftIncluded(t *testing.T) {}\n")
	write("excluded_test.go", "//go:build !integration\n\npackage integration\n\nfunc TestIsolationRaftExcluded(t *testing.T) {}\n")
	write("unconstrained_test.go", "package integration\n\nfunc TestClusterUnconstrained(t *testing.T) {}\n")

	tests := clusterTests(t, root)
	if _, ok := tests["TestIsolationRaftIncluded"]; !ok {
		t.Error("TestIsolationRaftIncluded: a file tagged integration must be counted")
	}
	if _, ok := tests["TestIsolationRaftExcluded"]; ok {
		t.Error("TestIsolationRaftExcluded: a file tagged !integration never compiles into the " +
			"+integration build and cannot run in make integration-test-cluster-tenancy; it must not be counted")
	}
	if _, ok := tests["TestClusterUnconstrained"]; !ok {
		t.Error("TestClusterUnconstrained: an unconstrained file participates in every build and must be counted")
	}
}

func TestClusterTenancySelection(t *testing.T) {
	root := repoRoot(t)
	sel := tenancyRunRegex(t, root)
	tests := clusterTests(t, root)

	selected := 0
	for name, file := range tests {
		match := sel.MatchString(name)
		if match {
			selected++
		}
		if strings.HasPrefix(file, "isolation_raft_") && !match {
			t.Errorf("%s (%s) is a multi-node tenant suite but %s does not select it", name, file, tenancyRunVar)
		}
	}
	if selected < tenancyTestFloor {
		t.Fatalf("%s selects %d test functions in ./test/integration, below the floor of %d: "+
			"a selection that matches nothing makes the per-push job green while running nothing",
			tenancyRunVar, selected, tenancyTestFloor)
	}
}

type ciWorkflow struct {
	Jobs map[string]struct {
		If    string    `yaml:"if"`
		Needs yaml.Node `yaml:"needs"`
		Steps []struct {
			Run string            `yaml:"run"`
			Env map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
	On yaml.Node `yaml:"on"`
}

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

// onTriggers returns the event names ci.yml's top-level `on:` declares,
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

// tenancyJobProblems validates wf against every property the per-push tenancy
// gate depends on and returns one message per violation found, so a caller
// can either report them all (the real gate) or just count them (the
// self-test that proves the gate can fail).
func tenancyJobProblems(wf ciWorkflow) []string {
	var problems []string

	// The workflow's own `on:` must actually name both events the per-push
	// gate exists for. wf.On used to be parsed and never asserted on: a
	// workflow retargeted to, say, workflow_dispatch alone would still have
	// parsed cleanly and this gate would have said nothing.
	triggers := onTriggers(wf.On)
	for _, want := range []string{"push", "pull_request"} {
		found := false
		for _, got := range triggers {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, "ci.yml's top-level `on:` does not include "+want+" (has "+
				strings.Join(triggers, ",")+"): the per-push tenancy gate would not run on "+want)
		}
	}

	job, ok := wf.Jobs[tenancyJob]
	if !ok {
		return append(problems, "ci.yml has no "+tenancyJob+" job: the multi-node tenant suites run only nightly")
	}
	// The job is in the full tier (#674): it runs on every push to main, on
	// every dispatch and on a PR labelled `ci:full`, and on nothing less. Any
	// other `if:` -- or none, which would put it back on every PR push -- is
	// a tier ci-gate does not know it is in: a narrower one gets excused as a
	// full-tier skip on exactly the pushes to main this gate exists to cover.
	if job.If != fullTierIf {
		problems = append(problems, "job "+tenancyJob+"'s `if:` is "+strconv.Quote(job.If)+
			", not the full-tier condition "+strconv.Quote(fullTierIf)+
			": it must run on every push to main and on a `ci:full` pull request")
	}
	runsTarget := false
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "make "+tenancyTarget) {
			runsTarget = true
		}
	}
	if !runsTarget {
		problems = append(problems, "job "+tenancyJob+" has no step running `make "+tenancyTarget+"`")
	}

	gate, ok := wf.Jobs[ciGateJob]
	if !ok {
		return append(problems, "ci.yml has no "+ciGateJob+" job")
	}
	gateNeeds := needsList(gate.Needs)
	required := false
	buildRequired := false
	for _, n := range gateNeeds {
		if n == tenancyJob {
			required = true
		}
		if n == buildJob {
			buildRequired = true
		}
	}
	if !required {
		problems = append(problems, ciGateJob+" does not list "+tenancyJob+
			" in needs: a job that can pass and is not required is a fail-open gate")
	}
	// The tenancy job itself needs build (checked above as part of the
	// workflow's own job graph), so ci-gate reaching it through a required
	// tenancy job alone would still gate it -- but only AS LONG AS that
	// chain holds. ci-gate names build directly, so a future edit that
	// narrows the tenancy job's own needs cannot quietly drop build from
	// what the result gate actually waits on.
	if !buildRequired {
		problems = append(problems, ciGateJob+" does not list "+buildJob+
			" in needs: the tenancy job's own build dependency is not, by itself, "+
			"something the result gate is guaranteed to keep waiting on")
	}
	checked, tier := false, ""
	for _, s := range gate.Steps {
		c, found := gateChecks(s.Run)[tenancyJob]
		if !found {
			continue
		}
		if m := needsResult.FindStringSubmatch(s.Env[c.envVar]); m != nil && m[1] == tenancyJob {
			checked, tier = true, c.tier
		}
	}
	if !checked {
		problems = append(problems, ciGateJob+" needs "+tenancyJob+
			" but never checks its result: a skipped or failed run would not fail the gate")
	} else if tier != tierFull {
		problems = append(problems, ciGateJob+" tags "+tenancyJob+" "+strconv.Quote(tier)+
			", not full: the gate would fail every plain pull request on its tier skip, "+
			"or excuse no skip it should")
	}
	return problems
}

func TestClusterTenancyJobGatesEveryPush(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	var wf ciWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for _, problem := range tenancyJobProblems(wf) {
		t.Error(problem)
	}
}

// tenancyWorkflow is a minimal, otherwise-valid ci.yml fixture: the
// tenancyJob and ciGateJob tenancyJobProblems looks for, wired correctly.
// Each case of TestTenancyJobProblems_CanFail starts from this and breaks
// exactly one property.
const tenancyWorkflow = `
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
jobs:
  ` + tenancyJob + `:
    if: ` + fullTierIf + `
    steps:
      - run: make ` + tenancyTarget + `
  ` + ciGateJob + `:
    needs: [` + buildJob + `, ` + tenancyJob + `]
    steps:
      - run: check ` + tenancyJob + ` "$R" full
        env:
          R: ${{ needs.` + tenancyJob + `.result }}
`

func parseWorkflow(t *testing.T, body string) ciWorkflow {
	t.Helper()
	var wf ciWorkflow
	if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatalf("parse workflow fixture: %v", err)
	}
	return wf
}

// TestTenancyJobProblems_CanFail proves tenancyJobProblems' two new checks
// (go-qrdb strict-isolation phase 0 review finding N4 -- `on:` was parsed and
// never asserted, and no check existed for a job-level `if:`) actually redden
// on the defect each names, rather than passing vacuously on every input the
// way an unanchored check would (go-qrdb#500's lesson: a guard that cannot go
// red is not a gate).
func TestTenancyJobProblems_CanFail(t *testing.T) {
	if problems := tenancyJobProblems(parseWorkflow(t, tenancyWorkflow)); len(problems) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v; a broken fixture would invalidate "+
			"every other case below", problems)
	}

	cases := []struct {
		name string
		body string
	}{
		{
			name: "missing pull_request trigger",
			body: strings.Replace(tenancyWorkflow, "  pull_request:\n    branches: [main]\n", "", 1),
		},
		{
			name: "missing push trigger",
			body: strings.Replace(tenancyWorkflow, "  push:\n    branches: [main]\n", "", 1),
		},
		{
			name: "tenancy job narrowed to push only",
			body: strings.Replace(tenancyWorkflow,
				"    if: "+fullTierIf+"\n",
				"    if: ${{ github.event_name == 'push' }}\n", 1),
		},
		{
			name: "tenancy job without the full-tier if",
			body: strings.Replace(tenancyWorkflow, "    if: "+fullTierIf+"\n", "", 1),
		},
		{
			name: "tenancy check tagged pr",
			body: strings.Replace(tenancyWorkflow, `"$R" full`, `"$R" pr`, 1),
		},
		{
			name: "tenancy check reads another job's result",
			body: strings.Replace(tenancyWorkflow, "needs."+tenancyJob+".result", "needs."+buildJob+".result", 1),
		},
		{
			name: "ci-gate does not need build",
			body: strings.Replace(tenancyWorkflow,
				"needs: ["+buildJob+", "+tenancyJob+"]", "needs: ["+tenancyJob+"]", 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problems := tenancyJobProblems(parseWorkflow(t, tc.body)); len(problems) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
