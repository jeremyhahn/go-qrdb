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

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	tenancyTarget    = "integration-test-cluster-tenancy"
	tenancyJob       = "integration-cluster-tenancy"
	tenancyRunVar    = "CLUSTER_TENANCY_RUN"
	ciGateJob        = "ci-gate"
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

// clusterTests maps each top-level Test function in ./test/integration to the
// file that declares it.
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
		for _, m := range fn.FindAllSubmatch(b, -1) {
			out[string(m[1])] = e.Name()
		}
	}
	return out
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
		Needs yaml.Node `yaml:"needs"`
		Steps []struct {
			Run string            `yaml:"run"`
			Env map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
	On yaml.Node `yaml:"on"`
}

func needsList(t *testing.T, n yaml.Node) []string {
	t.Helper()
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

	job, ok := wf.Jobs[tenancyJob]
	if !ok {
		t.Fatalf("ci.yml has no %q job: the multi-node tenant suites run only nightly", tenancyJob)
	}
	runsTarget := false
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "make "+tenancyTarget) {
			runsTarget = true
		}
	}
	if !runsTarget {
		t.Errorf("job %q has no step running `make %s`", tenancyJob, tenancyTarget)
	}

	gate, ok := wf.Jobs[ciGateJob]
	if !ok {
		t.Fatalf("ci.yml has no %q job", ciGateJob)
	}
	required := false
	for _, n := range needsList(t, gate.Needs) {
		if n == tenancyJob {
			required = true
		}
	}
	if !required {
		t.Errorf("%q does not list %q in needs: a job that can pass and is not required is a fail-open gate", ciGateJob, tenancyJob)
	}
	checked := false
	for _, s := range gate.Steps {
		if strings.Contains(s.Run, "check "+tenancyJob+" ") {
			for _, v := range s.Env {
				if strings.Contains(v, "needs."+tenancyJob+".result") {
					checked = true
				}
			}
		}
	}
	if !checked {
		t.Errorf("%q needs %q but never checks its result: a skipped or failed run would not fail the gate", ciGateJob, tenancyJob)
	}
}
