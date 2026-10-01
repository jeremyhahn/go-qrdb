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

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfile writes a profile of n packages, each 10 statements with the
// given number covered, and returns its path.
func writeProfile(t *testing.T, n, covered int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("mode: atomic\n")
	for i := range n {
		pkg := "example.com/m/p" + itoa(i)
		// one covered block and one uncovered block per package
		b.WriteString(pkg + "/f.go:1.1,2.2 " + itoa(covered) + " 1\n")
		if covered < 10 {
			b.WriteString(pkg + "/f.go:3.1,4.2 " + itoa(10-covered) + " 0\n")
		}
	}
	p := filepath.Join(t.TempDir(), "total.out")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRun_WriteThenCheckPasses is the day-one path end to end: generate a
// baseline from a profile, then check that same profile against it. It must
// pass with nothing flagged, or the gate is red on the day it is armed.
func TestRun_WriteThenCheckPasses(t *testing.T) {
	prof := writeProfile(t, 4, 9) // 90%: under the floor, so only the ratchet can pass it
	base := filepath.Join(t.TempDir(), "baseline.txt")

	if err := run([]string{"-tags", "t", "-profile", prof, "-baseline", base, "-write", "-discovery-floor", "4"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out strings.Builder
	err := run([]string{"-tags", "t", "-profile", prof, "-baseline", base, "-min", "85", "-discovery-floor", "4"}, &out)
	if err != nil {
		t.Fatalf("check against its own baseline: %v", err)
	}
	if strings.Contains(out.String(), "headroom") {
		t.Errorf("a fresh baseline reported headroom:\n%s", out.String())
	}
}

// TestRun_CheckCatchesARegressionAgainstAWrittenBaseline: the same round trip,
// then coverage falls. This is the negative control for the test above -- a
// "pass" that could not fail would prove nothing.
func TestRun_CheckCatchesARegressionAgainstAWrittenBaseline(t *testing.T) {
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := run([]string{"-tags", "t", "-profile", writeProfile(t, 4, 10), "-baseline", base, "-write", "-discovery-floor", "4"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := run([]string{"-tags", "t", "-profile", writeProfile(t, 4, 9), "-baseline", base, "-min", "85", "-discovery-floor", "4"}, io.Discard)
	var ge *GateError
	if !errors.As(err, &ge) || len(ge.Regressed) != 4 {
		t.Fatalf("err = %v, want all four packages regressed 100%% -> 90%%", err)
	}
}

func TestRun_RejectsBadFlags(t *testing.T) {
	prof := writeProfile(t, 1, 10)
	for name, args := range map[string][]string{
		"no profile":         {"-baseline", "b", "-min", "91"},
		"no baseline":        {"-profile", prof, "-min", "91"},
		"no min":             {"-profile", prof, "-baseline", "b"},
		"min above 100":      {"-profile", prof, "-baseline", "b", "-min", "101"},
		"negative tolerance": {"-profile", prof, "-baseline", "b", "-min", "91", "-tolerance", "-1"},
		"zero floor":         {"-profile", prof, "-baseline", "b", "-min", "91", "-discovery-floor", "0"},
		"unknown flag":       {"-profile", prof, "-baseline", "b", "-min", "91", "-nope"},
	} {
		t.Run(name, func(t *testing.T) {
			var ce *ConfigError
			if err := run(args, io.Discard); !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *ConfigError", err)
			}
		})
	}
}

// TestRun_MissingInputsAreErrors: a gate pointed at a file that does not exist
// must fail. Treating a missing profile or baseline as empty would pass.
func TestRun_MissingInputsAreErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	var pe *ProfileError
	if err := run([]string{"-tags", "t", "-profile", missing, "-baseline", "b", "-min", "91"}, io.Discard); !errors.As(err, &pe) {
		t.Errorf("missing profile: err = %v, want *ProfileError", err)
	}
	var be *BaselineError
	if err := run([]string{"-tags", "t", "-profile", writeProfile(t, 1, 10), "-baseline", missing, "-min", "91", "-discovery-floor", "1"}, io.Discard); !errors.As(err, &be) {
		t.Errorf("missing baseline: err = %v, want *BaselineError", err)
	}
}

// TestRun_WriteRefusesATruncatedProfile: regenerating the baseline from a
// profile that measured almost nothing would write a baseline that holds
// almost nothing -- and every package missing from it would then be judged
// only as "new". The discovery floor guards -write as well as the check.
func TestRun_WriteRefusesATruncatedProfile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "baseline.txt")
	err := run([]string{"-tags", "t", "-profile", writeProfile(t, 2, 10), "-baseline", base, "-write", "-discovery-floor", "50"}, io.Discard)
	var fe *DiscoveryFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *DiscoveryFloorError", err)
	}
	if _, statErr := os.Stat(base); statErr == nil {
		t.Error("a refused -write still created the baseline file")
	}
}

// TestRun_WriteLeavesExcludedPackagesOut: a test helper must not be written
// into the baseline, or excluding it later would leave a stale entry.
func TestRun_WriteLeavesExcludedPackagesOut(t *testing.T) {
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := run([]string{"-tags", "t", "-profile", writeProfile(t, 3, 10), "-baseline", base, "-write",
		"-exclude", "example.com/m/p0", "-discovery-floor", "2"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example.com/m/p0 ") {
		t.Errorf("an excluded package was written to the baseline:\n%s", data)
	}
	if !strings.Contains(string(data), "example.com/m/p1 ") {
		t.Errorf("a kept package is missing from the baseline:\n%s", data)
	}
}

// TestRun_ReportShowsHeadroomAndStaleExclusions: the passing output is part of
// the contract. A gate whose green log is silent cannot be told apart from one
// that checked nothing, so what it excluded and what has headroom are printed.
func TestRun_ReportShowsHeadroomAndStaleExclusions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := run([]string{"-tags", "t", "-profile", writeProfile(t, 3, 9), "-baseline", base, "-write", "-discovery-floor", "3"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out strings.Builder
	err := run([]string{"-tags", "t", "-profile", writeProfile(t, 3, 10), "-baseline", base, "-min", "85",
		"-exclude", "example.com/m/never-measured", "-discovery-floor", "3"}, &out)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, want := range []string{
		"3 packages checked",
		"STALE      example.com/m/never-measured",
		"headroom   example.com/m/p0: 100.00%, baseline 90.00%",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRun_WriteReportsAnUncreatableBaseline: -write into a directory that does
// not exist must fail loudly, not report success over a file that was never
// written.
func TestRun_WriteReportsAnUncreatableBaseline(t *testing.T) {
	base := filepath.Join(t.TempDir(), "no-such-dir", "baseline.txt")
	err := run([]string{"-tags", "t", "-profile", writeProfile(t, 2, 10), "-baseline", base, "-write", "-discovery-floor", "2"}, io.Discard)
	var be *BaselineError
	if !errors.As(err, &be) || be.Reason != "cannot create" {
		t.Fatalf("err = %v, want *BaselineError(cannot create)", err)
	}
}

// failingWriter fails every write, standing in for a full disk.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestWriteBaseline_PropagatesWriteFailures: a baseline that could not be
// written must not look written. Both the header and a package line can fail.
func TestWriteBaseline_PropagatesWriteFailures(t *testing.T) {
	if err := WriteBaseline(failingWriter{}, map[string]Stats{"a": {1, 1}}, "t"); err == nil {
		t.Error("a failed header write was swallowed")
	}
	// Let the build-tags directive through, then fail on the first package line.
	if err := WriteBaseline(&countingWriter{failAfter: 1}, map[string]Stats{"a": {1, 1}}, "t"); err == nil {
		t.Error("a failed build-tags write was swallowed")
	}
	// Let the header and the directive through, then fail on the first package line.
	w := &countingWriter{failAfter: 2}
	if err := WriteBaseline(w, map[string]Stats{"a": {1, 1}}, "t"); err == nil {
		t.Error("a failed package-line write was swallowed")
	}
}

// countingWriter accepts failAfter writes and fails the rest.
type countingWriter struct{ n, failAfter int }

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.n >= c.failAfter {
		return 0, errors.New("disk full")
	}
	c.n++
	return len(p), nil
}

// TestErrorMessages asserts the WHOLE message of every error type. These are
// the gate's output in a failing CI log -- the only thing a developer whose
// push was refused actually reads -- so a dropped field is a real regression.
func TestErrorMessages(t *testing.T) {
	cause := errors.New("permission denied")
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ConfigError{Reason: "-min is required"}, "coverratchet: -min is required"},
		{&ProfileError{Path: "p.out", Line: 7, Reason: "bad"}, "coverratchet: profile p.out:7: bad"},
		{&ProfileError{Path: "p.out", Reason: "cannot open", Err: cause}, "coverratchet: profile p.out: cannot open: permission denied"},
		{&BaselineError{Path: "b.txt", Line: 3, Reason: "bad"}, "coverratchet: baseline b.txt:3: bad"},
		{&BaselineError{Path: "b.txt", Reason: "cannot create", Err: cause}, "coverratchet: baseline b.txt: cannot create: permission denied"},
		{&DiscoveryFloorError{Found: 2, Floor: 50}, "coverratchet: the profile measured 2 packages, below the discovery floor of 50: " +
			"an empty or truncated profile would otherwise pass every check vacuously"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() =\n  %q\nwant\n  %q", got, tc.want)
		}
	}

	// Unwrap reaches the cause, so callers can branch without string matching.
	if !errors.Is(&ProfileError{Err: cause}, cause) {
		t.Error("ProfileError does not unwrap to its cause")
	}
	if !errors.Is(&BaselineError{Err: cause}, cause) {
		t.Error("BaselineError does not unwrap to its cause")
	}
}
