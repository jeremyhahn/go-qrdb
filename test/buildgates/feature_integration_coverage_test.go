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

// Every docs/<feature>/architecture.md folder must have a corresponding
// `make integration-test-<feature>` selection in test/integration/features.mk,
// and every Test function in the NON-DOCKER integration packages
// (./test/integration, ./test/integration/auth -- test/integration/api is
// the compose-based suite and out of scope here) must be named by some
// feature's file list. Either direction drifting silently is exactly the
// shape #500 and #367 both named: a property of the whole tree that no
// single diff shows a reviewer, so it is a scanner rather than a review
// habit.
//
// A feature mapped to an EMPTY file list is not a failure -- it is the
// mapping's honest way of saying "no non-Docker integration test exists for
// this feature yet", and test/integration/COVERAGE.md is where that gap is
// read. The only failures this gate reports are a MISSING entry (the feature
// folder exists and features.mk says nothing about it at all) and an
// unmapped Test function (it runs under `make test` / `make integration-test`
// but no `make integration-test-<feature>` ever selects it).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	featuresMapFile  = "test/integration/features.mk"
	intTestVarPrefix = "INTTEST_"
	featureFloor     = 40 // docs/*/architecture.md folders, measured at 45 when this gate was written
	testFuncFloor    = 80 // Test functions in the non-Docker packages, measured at ~190
)

// featureFolders returns the feature names docs/<feature>/architecture.md
// declares -- the canonical feature set every other table in this gate is
// checked against.
func featureFolders(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("read docs: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "docs", e.Name(), "architecture.md")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// featureVar is the Makefile variable name test/integration/features.mk
// declares for feature, and the one the Makefile's integration-test-%
// pattern rule computes with $(subst -,_,$*) -- the two must derive the
// same string from the same feature name, or the mapping and the target
// silently disagree about which feature a line names.
func featureVar(feature string) string {
	return intTestVarPrefix + strings.ReplaceAll(feature, "-", "_")
}

// featureMapping parses test/integration/features.mk's `INTTEST_<feature> :=`
// lines into feature-var -> declared (possibly empty) file list, and
// separately which variable names were declared at all. A variable is
// "declared" the moment its key line appears, regardless of whether its
// value is empty: an empty value is the legitimate "not yet covered" state,
// and only a MISSING key means the feature has no integration target at all.
func featureMapping(t *testing.T, root string) (files map[string][]string, declared map[string]bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, featuresMapFile))
	if err != nil {
		t.Fatalf("read %s: %v", featuresMapFile, err)
	}
	files = make(map[string][]string)
	declared = make(map[string]bool)
	re := regexp.MustCompile(`^(INTTEST_[A-Za-z0-9_]+)\s*:=\s*(.*)$`)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		declared[m[1]] = true
		if v := strings.TrimSpace(m[2]); v != "" {
			files[m[1]] = strings.Fields(v)
		}
	}
	return files, declared
}

// nonDockerTestFunctions maps every top-level Test function declared in the
// non-Docker integration packages to the file that declares it, named the
// way features.mk names it: relative to test/integration/, so the auth
// subpackage's files read "auth/<name>".
func nonDockerTestFunctions(t *testing.T, root string) map[string]string {
	t.Helper()
	fn := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	out := make(map[string]string)
	scan := func(dir, prefix string) {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
			}
			rel := prefix + e.Name()
			for _, m := range fn.FindAllSubmatch(b, -1) {
				out[string(m[1])] = rel
			}
		}
	}
	scan("test/integration", "")
	scan("test/integration/auth", "auth/")
	return out
}

// missingFeatureTargets returns the features in the canonical set that have
// no declared variable in features.mk at all.
func missingFeatureTargets(features []string, declared map[string]bool) []string {
	var missing []string
	for _, f := range features {
		if !declared[featureVar(f)] {
			missing = append(missing, f)
		}
	}
	return missing
}

// unmappedTests returns the subset of tests (name -> file) whose file is not
// claimed by any feature's file list.
func unmappedTests(tests map[string]string, mappedFiles map[string]string) map[string]string {
	out := make(map[string]string)
	for name, file := range tests {
		if _, ok := mappedFiles[file]; !ok {
			out[name] = file
		}
	}
	return out
}

func TestIntegrationFeatureCoverage_EveryFeatureHasATarget(t *testing.T) {
	root := repoRoot(t)
	features := featureFolders(t, root)
	if len(features) < featureFloor {
		t.Fatalf("found only %d docs/*/architecture.md feature folders, below the floor of %d: "+
			"the feature-folder discovery looks broken", len(features), featureFloor)
	}
	_, declared := featureMapping(t, root)
	for _, f := range missingFeatureTargets(features, declared) {
		t.Errorf("docs/%s/ has no %s entry in %s: `make integration-test-%s` has no declared "+
			"selection and no record of why", f, featureVar(f), featuresMapFile, f)
	}
}

func TestIntegrationFeatureCoverage_EveryTestIsMapped(t *testing.T) {
	root := repoRoot(t)
	files, _ := featureMapping(t, root)
	mappedFiles := make(map[string]string) // file -> feature var that claims it
	for v, fs := range files {
		for _, f := range fs {
			mappedFiles[f] = v
		}
	}
	tests := nonDockerTestFunctions(t, root)
	if len(tests) < testFuncFloor {
		t.Fatalf("found only %d Test functions under the non-Docker integration packages, below the "+
			"floor of %d: the test-function discovery looks broken", len(tests), testFuncFloor)
	}
	for name, file := range unmappedTests(tests, mappedFiles) {
		t.Errorf("%s in test/integration/%s is not selected by any feature in %s: it runs under "+
			"`make test` / `make integration-test` but no `make integration-test-<feature>` "+
			"ever selects it", name, file, featuresMapFile)
	}
}

// TestIntegrationFeatureCoverage_FilesExist catches a stale entry: a file
// features.mk names that has since been renamed or deleted still parses and
// still selects zero tests, so without this check it would fail silently as
// "nothing to run" rather than loudly as "this file is gone".
func TestIntegrationFeatureCoverage_FilesExist(t *testing.T) {
	root := repoRoot(t)
	files, _ := featureMapping(t, root)
	for v, fs := range files {
		for _, f := range fs {
			p := filepath.Join(root, "test", "integration", f)
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s names %q, which does not exist at %s", v, f, p)
			}
		}
	}
}

// TestIntegrationFeatureCoverage_CanFail proves the two comparison functions
// above are not vacuous (go-qrdb#500's lesson: a gate that cannot go red is
// not a gate) against plain in-memory fixtures -- no synthetic filesystem
// tree is needed, since missingFeatureTargets and unmappedTests take their
// input as data rather than reading it themselves.
func TestIntegrationFeatureCoverage_CanFail(t *testing.T) {
	features := []string{"alpha", "beta"}
	onlyAlpha := map[string]bool{featureVar("alpha"): true}
	missing := missingFeatureTargets(features, onlyAlpha)
	if len(missing) != 1 || missing[0] != "beta" {
		t.Fatalf("missingFeatureTargets(%v, %v) = %v, want [beta]", features, onlyAlpha, missing)
	}
	both := map[string]bool{featureVar("alpha"): true, featureVar("beta"): true}
	if got := missingFeatureTargets(features, both); len(got) != 0 {
		t.Fatalf("missingFeatureTargets with every feature declared = %v, want none", got)
	}

	tests := map[string]string{"TestFoo": "foo_test.go", "TestBar": "bar_test.go"}
	onlyFoo := map[string]string{"foo_test.go": "INTTEST_a"}
	un := unmappedTests(tests, onlyFoo)
	if len(un) != 1 || un["TestBar"] != "bar_test.go" {
		t.Fatalf("unmappedTests(%v, %v) = %v, want {TestBar: bar_test.go}", tests, onlyFoo, un)
	}
	fooAndBar := map[string]string{"foo_test.go": "INTTEST_a", "bar_test.go": "INTTEST_b"}
	if got := unmappedTests(tests, fooAndBar); len(got) != 0 {
		t.Fatalf("unmappedTests with every file mapped = %v, want none", got)
	}
}
