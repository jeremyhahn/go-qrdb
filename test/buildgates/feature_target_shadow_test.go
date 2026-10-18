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
	"errors"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// explicitIntTarget matches an explicit integration-test-<name> rule in the
// output of `make -p`. The pattern rule itself, integration-test-%, does not
// match, and neither does a .PHONY line, which starts with the dot.
var explicitIntTarget = regexp.MustCompile(`^integration-test-([^%:\s]+):`)

// makeNotATarget is the comment `make -p` prints above a file it knows of
// but has no rule for; the line after it is not a target.
const makeNotATarget = "# Not a target:"

// shadowingTargets returns, sorted, the features that have an explicit
// integration-test-<feature> rule in db, the database `make -p` prints.
// GNU make prefers an explicit rule to a pattern rule, so such a target
// runs whatever its own recipe does instead of the feature's features.mk
// selection: `make integration-test-rag` once started the Docker stack with
// the Ollama sidecar while CI's rag leg was meant to run the non-Docker tests
// mapped to rag.
func shadowingTargets(db string, features []string) []string {
	isFeature := make(map[string]bool, len(features))
	for _, f := range features {
		isFeature[f] = true
	}
	var out []string
	notATarget := false
	for _, line := range strings.Split(db, "\n") {
		if m := explicitIntTarget.FindStringSubmatch(line); m != nil && !notATarget && isFeature[m[1]] {
			out = append(out, m[1])
		}
		notATarget = strings.HasPrefix(line, makeNotATarget)
	}
	sort.Strings(out)
	return out
}

// makeDatabase returns the rule database of the repository's Makefile,
// every included file with it, as `make -p` prints it. -q runs no recipe;
// its exit status 1 only says the goal is out of date.
func makeDatabase(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("make", "-pRrq", "print-integration-features")
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("make -pRrq: %v\n%s", err, errOut.String())
		}
	}
	return out.String()
}

// TestIntegrationFeatureCoverage_NoTargetShadowsAFeature fails when a
// features.mk feature has an explicit integration-test-<feature> rule, which
// would hide the pattern rule that runs that feature's selection, in CI's
// per-feature job and for anyone typing the target. Give such a target a
// name that is not a feature's.
func TestIntegrationFeatureCoverage_NoTargetShadowsAFeature(t *testing.T) {
	root := repoRoot(t)
	_, declared := featureMapping(t, root)
	features := make([]string, 0, len(declared))
	for v := range declared {
		features = append(features, strings.ReplaceAll(strings.TrimPrefix(v, intTestVarPrefix), "_", "-"))
	}
	if len(features) < featureFloor {
		t.Fatalf("features.mk declares %d features, below the floor of %d", len(features), featureFloor)
	}
	db := makeDatabase(t, root)
	// Discovery floor: a database this gate cannot read would find no
	// explicit target at all and pass.
	if !strings.Contains(db, "\nintegration-test-standalone:") {
		t.Fatal("make -p printed no integration-test-standalone rule: the gate is not reading the Makefile's database")
	}
	for _, f := range shadowingTargets(db, features) {
		t.Errorf("Makefile has an explicit integration-test-%s target, which shadows the pattern rule that "+
			"runs feature %s's features.mk selection; rename it to a name that is not a feature's", f, f)
	}
}

// TestIntegrationFeatureCoverage_ShadowCanFail proves shadowingTargets
// reports an explicit rule named for a feature, and nothing else.
func TestIntegrationFeatureCoverage_ShadowCanFail(t *testing.T) {
	db := strings.Join([]string{
		"integration-test-%:",
		"integration-test-rag: ## Ollama",
		"integration-test-auth:",
		".PHONY: integration-test-sql",
		makeNotATarget,
		"integration-test-sql:",
		"integration-test-key-value: integration-image",
	}, "\n")
	got := shadowingTargets(db, []string{"key-value", "rag", "sql", "watch"})
	if want := []string{"key-value", "rag"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("shadowingTargets = %v, want %v", got, want)
	}
	if got := shadowingTargets("integration-test-%:\nintegration-test-auth:\n", []string{"rag"}); len(got) != 0 {
		t.Fatalf("shadowingTargets with no explicit feature rule = %v, want none", got)
	}
}
