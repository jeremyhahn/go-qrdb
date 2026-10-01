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
	"strings"
	"testing"
)

func parse(t *testing.T, profile string) map[string]Stats {
	t.Helper()
	got, err := ParseProfile("test.out", strings.NewReader(profile))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	return got
}

func TestParseProfile_AggregatesPerPackage(t *testing.T) {
	got := parse(t, `mode: atomic
example.com/m/a/x.go:1.1,2.2 3 1
example.com/m/a/x.go:3.1,4.2 2 0
example.com/m/a/y.go:1.1,2.2 5 7
example.com/m/b/z.go:1.1,2.2 4 0
`)
	if a := got["example.com/m/a"]; a.Covered != 8 || a.Total != 10 {
		t.Errorf("a = %+v, want 8/10", a)
	}
	if b := got["example.com/m/b"]; b.Covered != 0 || b.Total != 4 {
		t.Errorf("b = %+v, want 0/4", b)
	}
	if len(got) != 2 {
		t.Errorf("packages = %d, want 2", len(got))
	}
}

// TestParseProfile_DeduplicatesBlocksByPosition: a block named twice must be
// counted once, covered if ANY occurrence ran it. Without the dedupe a profile
// assembled from several runs would double its denominators.
func TestParseProfile_DeduplicatesBlocksByPosition(t *testing.T) {
	got := parse(t, `mode: set
example.com/m/a/x.go:1.1,2.2 3 0
example.com/m/a/x.go:1.1,2.2 3 1
example.com/m/a/x.go:1.1,2.2 3 0
`)
	if a := got["example.com/m/a"]; a.Covered != 3 || a.Total != 3 {
		t.Errorf("a = %+v, want 3/3: counted once, covered because one occurrence ran", a)
	}
}

// TestParseProfile_RefusesConflictingDuplicates: two occurrences of one
// position with different statement counts describe different code, so the
// profile is from mismatched builds. Picking either would produce a number
// that means nothing.
func TestParseProfile_RefusesConflictingDuplicates(t *testing.T) {
	_, err := ParseProfile("test.out", strings.NewReader(`mode: set
example.com/m/a/x.go:1.1,2.2 3 1
example.com/m/a/x.go:1.1,2.2 4 1
`))
	var pe *ProfileError
	if !errors.As(err, &pe) || !strings.Contains(pe.Reason, "mixes builds") {
		t.Fatalf("err = %v, want a *ProfileError about mixed builds", err)
	}
	if pe.Line != 3 {
		t.Errorf("Line = %d, want 3", pe.Line)
	}
}

// TestParseProfile_RefusesWhatIsNotAProfile covers every way an input can
// fail to be a profile. Each must be an ERROR: a gate that read an unreadable
// profile as "no packages" would pass with nothing checked.
func TestParseProfile_RefusesWhatIsNotAProfile(t *testing.T) {
	for name, in := range map[string]string{
		"empty file":         "",
		"only blank lines":   "\n\n",
		"no mode header":     "example.com/m/a/x.go:1.1,2.2 3 1\n",
		"too few fields":     "mode: set\nexample.com/m/a/x.go:1.1,2.2 3\n",
		"too many fields":    "mode: set\nexample.com/m/a/x.go:1.1,2.2 3 1 9\n",
		"bad stmt count":     "mode: set\nexample.com/m/a/x.go:1.1,2.2 three 1\n",
		"negative stmts":     "mode: set\nexample.com/m/a/x.go:1.1,2.2 -3 1\n",
		"bad exec count":     "mode: set\nexample.com/m/a/x.go:1.1,2.2 3 once\n",
		"no range separator": "mode: set\nexample.com/m/a/x.go 3 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProfile("test.out", strings.NewReader(in))
			var pe *ProfileError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want *ProfileError", err)
			}
		})
	}
}

// TestParseProfile_HeaderOnlyIsAnEmptyMeasurement: a mode line and nothing
// else is a well-formed profile of zero packages. It parses; the DISCOVERY
// FLOOR is what refuses it later, and TestGate covers that.
func TestParseProfile_HeaderOnlyIsAnEmptyMeasurement(t *testing.T) {
	if got := parse(t, "mode: set\n"); len(got) != 0 {
		t.Errorf("packages = %d, want 0", len(got))
	}
}

func TestStats_Pct(t *testing.T) {
	for _, tc := range []struct {
		s    Stats
		want float64
	}{
		{Stats{Covered: 1, Total: 4}, 25},
		{Stats{Covered: 4, Total: 4}, 100},
		{Stats{Covered: 0, Total: 4}, 0},
		// No statements is fully covered: nothing in it went unexecuted, and
		// treating it as 0% would fail a package for having no code.
		{Stats{Covered: 0, Total: 0}, 100},
	} {
		if got := tc.s.Pct(); got != tc.want {
			t.Errorf("%+v.Pct() = %v, want %v", tc.s, got, tc.want)
		}
	}
}
