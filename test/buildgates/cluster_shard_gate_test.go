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

// The cluster suite runs in CI as matrix legs of ci.yml's integration-cluster
// job, each `make integration-test-cluster CLUSTER_SHARD=I/N`.
// `make check-cluster-shards` proves that CLUSTER_SHARDS legs partition the
// suite's tests -- but only for the N the Makefile names. This gate holds the
// workflow to that same N: a matrix of [1, 2] under a check of 3 would leave
// every third test unrun, and a step passing a fixed leg, or the full suite
// with no leg at all, would make the matrix run one leg three times.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	clusterJob       = "integration-cluster"
	clusterTarget    = "integration-test-cluster"
	clusterShardsVar = "CLUSTER_SHARDS"
)

// legProject matches an INT_PROJECT assignment whose quoted value names the
// matrix leg.
var legProject = regexp.MustCompile(`INT_PROJECT="[^"\n]*\$\{\{ matrix\.shard \}\}[^"\n]*"`)

// shardWorkflow is the slice of ci.yml this gate reads.
type shardWorkflow struct {
	Jobs map[string]struct {
		If       string    `yaml:"if"`
		Needs    yaml.Node `yaml:"needs"`
		Strategy struct {
			FailFast *bool `yaml:"fail-fast"`
			Matrix   struct {
				Shard []int `yaml:"shard"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Run string            `yaml:"run"`
			Env map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// clusterShards reads CLUSTER_SHARDS from the Makefile.
func clusterShards(t *testing.T, root string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	m := regexp.MustCompile(`(?m)^` + clusterShardsVar + `\s*\??=\s*(\d+)\s*$`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("Makefile declares no %s ?= <legs>; check-cluster-shards has no leg count to prove", clusterShardsVar)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 1 {
		t.Fatalf("%s = %q is not a positive leg count", clusterShardsVar, m[1])
	}
	return n
}

// clusterShardProblems lists every way wf's cluster job fails to run exactly
// legs 1..n of n, each once, under ci-gate.
func clusterShardProblems(wf shardWorkflow, n int) []string {
	var problems []string
	job, ok := wf.Jobs[clusterJob]
	if !ok {
		return []string{"ci.yml has no " + clusterJob + " job: the cluster suite does not gate the merge"}
	}
	// The legs are in the full tier (#674): every push to main, every dispatch
	// and a PR labelled `ci:full`. Any other `if:` -- or none, which puts the
	// suite back on every PR push -- is a tier ci-gate does not know the job
	// is in, and a narrower one gets excused as a full-tier skip.
	if job.If != fullTierIf {
		problems = append(problems, "job "+clusterJob+"'s `if:` is "+strconv.Quote(job.If)+
			", not the full-tier condition "+strconv.Quote(fullTierIf)+
			": it must run on every push to main and on a `ci:full` pull request")
	}

	shards := job.Strategy.Matrix.Shard
	want := make([]string, n)
	for i := range want {
		want[i] = strconv.Itoa(i + 1)
	}
	got := make([]string, len(shards))
	for i, s := range shards {
		got[i] = strconv.Itoa(s)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		problems = append(problems, "job "+clusterJob+"'s strategy.matrix.shard is ["+strings.Join(got, ", ")+
			"], want ["+strings.Join(want, ", ")+"]: check-cluster-shards proves "+strconv.Itoa(n)+
			" legs partition the suite, so any other matrix leaves tests unrun or runs them twice")
	}
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		problems = append(problems, "job "+clusterJob+" does not set strategy.fail-fast: false: one red leg "+
			"would cancel the others and hide their results")
	}

	leg := "CLUSTER_SHARD=${{ matrix.shard }}/" + strconv.Itoa(n)
	runs := 0
	for _, s := range job.Steps {
		if !strings.Contains(s.Run, "make "+clusterTarget) {
			continue
		}
		runs++
		if !strings.Contains(s.Run, leg) {
			problems = append(problems, "job "+clusterJob+"'s `make "+clusterTarget+"` step does not pass "+leg+
				": every leg would run the same tests")
		}
		if !legProject.MatchString(s.Run) {
			problems = append(problems, "job "+clusterJob+"'s INT_PROJECT does not carry the leg: legs on one "+
				"host would share, and tear down, one compose project")
		}
	}
	if runs != 1 {
		problems = append(problems, "job "+clusterJob+" has "+strconv.Itoa(runs)+" steps running `make "+
			clusterTarget+"`, want exactly 1")
	}

	gate, ok := wf.Jobs[ciGateJob]
	if !ok {
		return append(problems, "ci.yml has no "+ciGateJob+" job")
	}
	required := false
	for _, need := range needsList(gate.Needs) {
		if need == clusterJob {
			required = true
		}
	}
	if !required {
		problems = append(problems, ciGateJob+" does not list "+clusterJob+" in needs")
	}
	checked := false
	for _, s := range gate.Steps {
		if !strings.Contains(s.Run, "check "+clusterJob+" ") {
			continue
		}
		for _, v := range s.Env {
			if strings.Contains(v, "needs."+clusterJob+".result") {
				checked = true
			}
		}
	}
	if !checked {
		problems = append(problems, ciGateJob+" never checks "+clusterJob+"'s result")
	}
	return problems
}

func TestClusterShardMatrixMatchesMakefile(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	var wf shardWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for _, problem := range clusterShardProblems(wf, clusterShards(t, root)) {
		t.Error(problem)
	}
}

// shardWorkflowFixture is a minimal workflow clusterShardProblems accepts
// for n = 3.
const shardWorkflowFixture = `jobs:
  integration-cluster:
    if: ` + fullTierIf + `
    strategy:
      fail-fast: false
      matrix:
        shard: [1, 2, 3]
    steps:
      - run: |
          export INT_PROJECT="qrdb-int-cluster-${{ matrix.shard }}-1"
          make integration-test-cluster INT_PROJECT="$INT_PROJECT" CLUSTER_SHARD=${{ matrix.shard }}/3
  ci-gate:
    needs: [integration-cluster]
    steps:
      - env:
          RESULT_INTEGRATION_CLUSTER: ${{ needs.integration-cluster.result }}
        run: |
          check integration-cluster "$RESULT_INTEGRATION_CLUSTER"
`

func parseShardWorkflow(t *testing.T, body string) shardWorkflow {
	t.Helper()
	var wf shardWorkflow
	if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatalf("parse workflow fixture: %v", err)
	}
	return wf
}

func TestClusterShardProblems_CanFail(t *testing.T) {
	if problems := clusterShardProblems(parseShardWorkflow(t, shardWorkflowFixture), 3); len(problems) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v; a broken fixture would invalidate "+
			"every other case below", problems)
	}

	cases := []struct {
		name string
		body string
		n    int
	}{
		{name: "matrix narrower than the check", body: strings.Replace(shardWorkflowFixture, "[1, 2, 3]", "[1, 2]", 1), n: 3},
		{name: "check wider than the matrix", body: shardWorkflowFixture, n: 4},
		{name: "duplicate leg", body: strings.Replace(shardWorkflowFixture, "[1, 2, 3]", "[1, 1, 3]", 1), n: 3},
		{name: "no matrix", body: strings.Replace(shardWorkflowFixture, "        shard: [1, 2, 3]\n", "", 1), n: 3},
		{name: "fail-fast on", body: strings.Replace(shardWorkflowFixture, "fail-fast: false", "fail-fast: true", 1), n: 3},
		{name: "fail-fast unset", body: strings.Replace(shardWorkflowFixture, "      fail-fast: false\n", "", 1), n: 3},
		{name: "fixed leg", body: strings.Replace(shardWorkflowFixture, "CLUSTER_SHARD=${{ matrix.shard }}/3", "CLUSTER_SHARD=1/3", 1), n: 3},
		{name: "no leg", body: strings.Replace(shardWorkflowFixture, " CLUSTER_SHARD=${{ matrix.shard }}/3", "", 1), n: 3},
		{name: "shared compose project", body: strings.Replace(shardWorkflowFixture, "qrdb-int-cluster-${{ matrix.shard }}-1", "qrdb-int-cluster-1", 1), n: 3},
		{name: "narrowed to push only", body: strings.Replace(shardWorkflowFixture, "    if: "+fullTierIf+"\n", "    if: ${{ github.event_name == 'push' }}\n", 1), n: 3},
		{name: "no full-tier if", body: strings.Replace(shardWorkflowFixture, "    if: "+fullTierIf+"\n", "", 1), n: 3},
		{name: "not required by ci-gate", body: strings.Replace(shardWorkflowFixture, "needs: [integration-cluster]", "needs: []", 1), n: 3},
		{name: "result not checked", body: strings.Replace(shardWorkflowFixture, "needs.integration-cluster.result", "needs.build.result", 1), n: 3},
		{name: "no cluster job", body: strings.Replace(shardWorkflowFixture, "  integration-cluster:\n", "  integration-other:\n", 1), n: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problems := clusterShardProblems(parseShardWorkflow(t, tc.body), tc.n); len(problems) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
