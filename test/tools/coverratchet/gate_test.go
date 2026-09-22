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
	"slices"
	"testing"
)

// pkgs builds a measured set of n packages at the given percent (out of 100
// statements each), named p0..p{n-1}, so a test can clear the discovery floor
// without spelling out every package.
func pkgs(n, pct int) map[string]Stats {
	out := make(map[string]Stats, n)
	for i := range n {
		out["p"+itoa(i)] = Stats{Covered: pct, Total: 100}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// baselineOf holds every measured package at its current value: the state
// immediately after `make coverage-baseline`.
func baselineOf(m map[string]Stats) map[string]float64 {
	b := make(map[string]float64, len(m))
	for p, s := range m {
		b[p] = s.Pct()
	}
	return b
}

func gate() Gate { return Gate{RepoMin: 91, Tolerance: 0.5, DiscoveryFloor: 3} }

func mustGateError(t *testing.T, err error) *GateError {
	t.Helper()
	var ge *GateError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want *GateError", err)
	}
	return ge
}

func TestGate_PassesAnUnchangedTree(t *testing.T) {
	m := pkgs(5, 95)
	res, err := gate().Evaluate(m, baselineOf(m))
	if err != nil {
		t.Fatalf("an unchanged tree failed its own baseline: %v", err)
	}
	if res.Measured != 5 || res.RepoPct != 95 {
		t.Errorf("res = %+v", res)
	}
	if len(res.Improved) != 0 {
		t.Errorf("an unchanged tree reported headroom: %v", res.Improved)
	}
}

func TestGate_RepoWideFloor(t *testing.T) {
	m := pkgs(5, 80)
	_, err := gate().Evaluate(m, baselineOf(m))
	ge := mustGateError(t, err)
	if !ge.RepoBelow || ge.RepoPct != 80 {
		t.Errorf("RepoBelow=%v RepoPct=%v, want true, 80", ge.RepoBelow, ge.RepoPct)
	}
}

// TestGate_RatchetBlocksARegression is the property the whole tool exists for.
// A package that was at 88% and drops to 85% fails, even though 88% was
// already under the floor: grandfathered code may not get WORSE.
func TestGate_RatchetBlocksARegression(t *testing.T) {
	m := pkgs(5, 95)
	m["weak"] = Stats{Covered: 85, Total: 100}
	base := baselineOf(pkgs(5, 95))
	base["weak"] = 88

	_, err := gate().Evaluate(m, base)
	ge := mustGateError(t, err)
	if len(ge.Regressed) != 1 || ge.Regressed[0].Package != "weak" {
		t.Fatalf("Regressed = %+v, want [weak]", ge.Regressed)
	}
	if ge.Regressed[0].Got != 85 || ge.Regressed[0].Want != 88 {
		t.Errorf("Regressed[0] = %+v", ge.Regressed[0])
	}
}

// TestGate_GrandfathersExistingCodeBelowTheFloor: a package already under the
// floor passes as long as it does not fall. This is what lets the gate arm on
// day one instead of failing 14 packages.
func TestGate_GrandfathersExistingCodeBelowTheFloor(t *testing.T) {
	m := pkgs(5, 95)
	m["weak"] = Stats{Covered: 80, Total: 100}
	if _, err := gate().Evaluate(m, baselineOf(m)); err != nil {
		t.Fatalf("a package held at 80%% and measuring 80%% failed: %v", err)
	}
}

// TestGate_ToleranceAbsorbsJitter: a fall no larger than the tolerance is not a
// regression. Without it, nondeterministic coverage -- timing paths, retry
// loops -- would fail pushes at random.
func TestGate_ToleranceAbsorbsJitter(t *testing.T) {
	m := pkgs(5, 95)
	base := baselineOf(m)
	base["p0"] = 95.4 // measured 95.0: a 0.4pp fall, inside the 0.5pp tolerance
	if _, err := gate().Evaluate(m, base); err != nil {
		t.Fatalf("a fall inside the tolerance failed: %v", err)
	}
	base["p0"] = 95.6 // a 0.6pp fall, outside it
	_, err := gate().Evaluate(m, base)
	if ge := mustGateError(t, err); len(ge.Regressed) != 1 {
		t.Errorf("Regressed = %+v, want p0", ge.Regressed)
	}
}

// TestGate_NewCodeMustMeetTheFloor: only EXISTING code is grandfathered. A
// package absent from the baseline is held to the full floor, so the gate
// cannot be satisfied by adding weakly tested packages.
func TestGate_NewCodeMustMeetTheFloor(t *testing.T) {
	base := baselineOf(pkgs(5, 95))

	m := pkgs(5, 95)
	m["fresh"] = Stats{Covered: 70, Total: 100}
	_, err := gate().Evaluate(m, base)
	ge := mustGateError(t, err)
	if len(ge.NewBelowFloor) != 1 || ge.NewBelowFloor[0].Package != "fresh" || ge.NewBelowFloor[0].Want != 91 {
		t.Fatalf("NewBelowFloor = %+v, want fresh held to 91", ge.NewBelowFloor)
	}

	m["fresh"] = Stats{Covered: 92, Total: 100}
	if _, err := gate().Evaluate(m, base); err != nil {
		t.Fatalf("a new package at 92%% failed: %v", err)
	}
}

// TestGate_ABaselinedPackageThatStopsBeingMeasuredFails: otherwise a package
// whose tests stop compiling, or are dropped from the package list, would
// leave the gate silently -- its regression can never be seen if it is never
// measured.
func TestGate_ABaselinedPackageThatStopsBeingMeasuredFails(t *testing.T) {
	m := pkgs(5, 95)
	base := baselineOf(m)
	base["vanished"] = 93
	_, err := gate().Evaluate(m, base)
	ge := mustGateError(t, err)
	if !slices.Equal(ge.Missing, []string{"vanished"}) {
		t.Errorf("Missing = %v, want [vanished]", ge.Missing)
	}
}

// TestGate_ExcludedPackagesAreIgnoredEverywhere: an excluded package counts
// toward nothing -- not the total, not the ratchet, not the new-code floor, not
// the missing check -- and is REPORTED, so the exclusion is visible.
func TestGate_ExcludedPackagesAreIgnoredEverywhere(t *testing.T) {
	g := gate()
	g.Exclude = map[string]bool{"helpertest": true, "gone": true}

	m := pkgs(5, 95)
	m["helpertest"] = Stats{Covered: 0, Total: 500} // would sink the total and fail the new-code floor
	base := baselineOf(pkgs(5, 95))
	base["gone"] = 50 // excluded AND baselined AND absent: must not be "missing"

	res, err := g.Evaluate(m, base)
	if err != nil {
		t.Fatalf("an excluded package affected the verdict: %v", err)
	}
	if res.RepoPct != 95 {
		t.Errorf("RepoPct = %v, want 95: the excluded package leaked into the total", res.RepoPct)
	}
	if !slices.Equal(res.Excluded, []string{"helpertest"}) {
		t.Errorf("Excluded = %v, want [helpertest]", res.Excluded)
	}
	if !slices.Equal(res.StaleExclusions, []string{"gone"}) {
		t.Errorf("StaleExclusions = %v, want [gone]", res.StaleExclusions)
	}
}

// TestGate_DiscoveryFloorRefusesAnEmptyMeasurement is the check that stops
// every other check passing vacuously. An empty profile has no packages, so no
// package can regress, be new, or be missing-from-measured -- and the repo-wide
// total of nothing is 100%. Without the floor that is a clean pass.
func TestGate_DiscoveryFloorRefusesAnEmptyMeasurement(t *testing.T) {
	res, err := gate().Evaluate(map[string]Stats{}, map[string]float64{})
	var fe *DiscoveryFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *DiscoveryFloorError", err)
	}
	if fe.Found != 0 || fe.Floor != 3 {
		t.Errorf("fe = %+v", fe)
	}
	if res == nil {
		t.Error("the Result must be returned even on refusal, so the caller can report")
	}
}

// TestGate_ExclusionsDoNotCountTowardTheDiscoveryFloor: a profile of nothing
// but excluded helpers measured nothing that is checked.
func TestGate_ExclusionsDoNotCountTowardTheDiscoveryFloor(t *testing.T) {
	g := gate()
	g.Exclude = map[string]bool{"a": true, "b": true, "c": true}
	_, err := g.Evaluate(map[string]Stats{"a": {1, 1}, "b": {1, 1}, "c": {1, 1}}, nil)
	var fe *DiscoveryFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *DiscoveryFloorError: excluded packages were counted as checked", err)
	}
}

// TestGate_ReportsEveryFailureInOnePass: the gate must not stop at the first
// finding, or fixing CI becomes one round trip per package.
func TestGate_ReportsEveryFailureInOnePass(t *testing.T) {
	m := pkgs(5, 60) // repo-wide below the floor
	m["regress"] = Stats{Covered: 50, Total: 100}
	m["fresh"] = Stats{Covered: 10, Total: 100}
	base := baselineOf(pkgs(5, 60))
	base["regress"] = 70
	base["vanished"] = 99

	_, err := gate().Evaluate(m, base)
	ge := mustGateError(t, err)
	if !ge.RepoBelow {
		t.Error("the repo-wide failure was not reported")
	}
	if len(ge.Regressed) != 1 {
		t.Errorf("Regressed = %v", ge.Regressed)
	}
	if len(ge.NewBelowFloor) != 1 {
		t.Errorf("NewBelowFloor = %v", ge.NewBelowFloor)
	}
	if len(ge.Missing) != 1 {
		t.Errorf("Missing = %v", ge.Missing)
	}
}

// TestGate_ImprovedIgnoresBaselineRounding: a baseline floored to two decimals
// sits up to 0.01pp below what was measured. That margin must not read as an
// improvement, or every package reports headroom on every run -- which is what
// the first real run of this tool did, for all 118 packages.
func TestGate_ImprovedIgnoresBaselineRounding(t *testing.T) {
	// RepoMin is set below the fixture on purpose. This test is about the
	// improvement threshold only; with a single package the REPO-WIDE total
	// equals that package's figure, and a 91 floor would fail it -- correctly,
	// as the first version of this test found out, but for a reason this test
	// is not about.
	g := Gate{RepoMin: 50, Tolerance: 0, DiscoveryFloor: 1}
	m := map[string]Stats{"a": {Covered: 2, Total: 3}} // 66.666...%

	res, err := g.Evaluate(m, map[string]float64{"a": 66.66})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Improved) != 0 {
		t.Errorf("rounding reported as headroom: %v", res.Improved)
	}

	// A genuine rise above the precision is reported.
	res, _ = g.Evaluate(m, map[string]float64{"a": 60})
	if len(res.Improved) != 1 {
		t.Errorf("a 6.67pp rise was not reported: %v", res.Improved)
	}
}

// TestGateError_Message names every failure a reader has to act on.
func TestGateError_Message(t *testing.T) {
	msg := (&GateError{
		RepoPct: 80, RepoMin: 91, RepoBelow: true,
		Regressed:     []Regression{{Package: "r", Got: 1, Want: 2}},
		NewBelowFloor: []Regression{{Package: "n", Got: 3, Want: 91}},
		Missing:       []string{"m"},
	}).Error()
	for _, want := range []string{"repo-wide 80.00%", "REGRESSED  r", "NEW        n", "MISSING    m"} {
		if !contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
