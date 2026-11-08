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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkCoverageProbe is a make target that runs the Makefile's check-coverage
// macro, unchanged, over the profile named by PROBE_PROFILE.
const checkCoverageProbe = "check-coverage-probe:\n\t$(call check-coverage,$(PROBE_PROFILE),probe)\n"

// checkCoverageSource is the file the synthetic profiles measure: two
// functions of one statement each, so a profile can cover 0%, 50% or 100%.
const checkCoverageSource = "package probe\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n"

// noProfile tells runCheckCoverage to name a profile it never writes.
const noProfile = "\x00absent"

// runCheckCoverage writes profile, with %s standing for the source file's
// absolute path, and runs the macro over it through make. It returns make's
// combined output and its error. Everything it writes is under t.TempDir.
func runCheckCoverage(t *testing.T, profile string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(src, []byte(checkCoverageSource), 0o600); err != nil {
		t.Fatal(err)
	}
	prof := filepath.Join(dir, "cover.out")
	if profile != noProfile {
		if err := os.WriteFile(prof, []byte(strings.ReplaceAll(profile, "%s", src)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probe := filepath.Join(dir, "probe.mk")
	if err := os.WriteFile(probe, []byte(checkCoverageProbe), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "-s", "-f", "Makefile", "-f", probe, "PROBE_PROFILE="+prof, "check-coverage-probe") // #nosec G204 -- probe and profile are this test's own temp files
	cmd.Dir = repoRoot(t)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "COVER_MIN=") { // the floor under test is the Makefile's, not an inherited one
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// TestCheckCoverageComparesTheFigure proves the per-package coverage check
// compares the figure with COVER_MIN, and that a profile with no figure fails.
//
// The check once compared with bc, which the CI image does not have: the
// comparison produced nothing, `[` failed, and every coverage-<pkg> target
// passed at any figure, 0% included (found by lane sdkfix2).
func TestCheckCoverageComparesTheFigure(t *testing.T) {
	for _, tc := range []struct {
		name, profile, wantOut string
		wantPass               bool
	}{
		{"all covered", "mode: set\n%s:3.14,5.2 1 1\n%s:7.14,9.2 1 1\n", "probe coverage: 100.0%", true},
		{"half covered", "mode: set\n%s:3.14,5.2 1 1\n%s:7.14,9.2 1 0\n", "is below minimum", false},
		{"nothing covered", "mode: set\n%s:3.14,5.2 1 0\n%s:7.14,9.2 1 0\n", "is below minimum", false},
		{"empty profile", "", "is below minimum", false}, // go tool cover totals an empty profile at 0.0%
		{"missing profile", noProfile, "has no total to compare", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCheckCoverage(t, tc.profile)
			if tc.wantPass && err != nil {
				t.Fatalf("check-coverage failed a profile above the floor: %v\n%s", err, out)
			}
			if !tc.wantPass {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("check-coverage passed, or make did not run (%v), for a profile it must fail:\n%s", err, out)
				}
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Fatalf("output %q does not contain %q", out, tc.wantOut)
			}
		})
	}
}

// TestCheckCoverageFailsAModeLineOnly covers a profile from a package with no
// statements: go tool cover reports no usable total, and that must not pass.
func TestCheckCoverageFailsAModeLineOnly(t *testing.T) {
	out, err := runCheckCoverage(t, "mode: set\n")
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("check-coverage passed, or make did not run (%v), for a profile with no blocks:\n%s", err, out)
	}
}
