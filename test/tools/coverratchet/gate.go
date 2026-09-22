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

import "slices"

// baselinePrecision is the resolution baseline numbers are written at: two
// decimals, FLOORED (see WriteBaseline). A package is therefore held to a
// number up to 0.01pp below what it measured, and a rise smaller than that is
// the flooring, not an improvement.
//
// Without this margin every package reported "headroom" against a baseline
// generated from the very same profile -- measured on the first real run,
// where all 118 packages did, 89.68% against a written 89.67% and so on. A
// hint that fires on every package on every run is noise, and noise is the
// fastest way to teach people to stop reading a gate's output.
const baselinePrecision = 0.01

// Gate is the configuration of one ratchet evaluation.
type Gate struct {
	// RepoMin is the repo-wide floor, and the floor a package absent from the
	// baseline must meet on its own.
	RepoMin float64
	// Tolerance is how far a baselined package may fall below its number
	// before it counts as a regression. It exists because some coverage is
	// not deterministic -- timing paths, retry loops, select arms -- and it is
	// set from a MEASURED run-to-run jitter, not chosen.
	Tolerance float64
	// Exclude are import paths left out of every check: test-helper packages,
	// which CLAUDE.md requires excluding from coverage reports. An explicit
	// list rather than a naming rule, because a rule such as "ends in test"
	// would also exempt a real package named `latest` and open a hole in the
	// gate that nobody would see.
	Exclude map[string]bool
	// DiscoveryFloor is the minimum number of measured packages. See
	// DiscoveryFloorError.
	DiscoveryFloor int
}

// Result describes an evaluation, pass or fail.
type Result struct {
	// RepoPct is the repo-wide coverage with excluded packages left out.
	RepoPct float64
	// Measured is how many packages were checked.
	Measured int
	// Excluded are the excluded packages that were present in the profile.
	Excluded []string
	// StaleExclusions are excluded packages the profile did not contain. Not a
	// failure -- an excluded package that no longer exists opens no hole --
	// but reported so the list does not rot.
	StaleExclusions []string
	// Improved are baselined packages now measurably above their number. Not
	// a failure: the hint that `make coverage-baseline` would tighten them.
	Improved []Regression
}

// Evaluate checks measured coverage against the baseline and the floors.
//
// It returns a *DiscoveryFloorError before checking anything else when too few
// packages were measured, since every later check iterates over the measured
// set and would pass vacuously on an empty one. Otherwise it returns a
// *GateError carrying EVERY failure, or nil. The Result is populated in both
// cases so the caller can print what was checked.
func (g Gate) Evaluate(measured map[string]Stats, baseline map[string]float64) (*Result, error) {
	res := &Result{}
	gerr := &GateError{RepoMin: g.RepoMin}

	var repo Stats
	for pkg, s := range measured {
		if g.Exclude[pkg] {
			res.Excluded = append(res.Excluded, pkg)
			continue
		}
		res.Measured++
		repo.Covered += s.Covered
		repo.Total += s.Total

		want, baselined := baseline[pkg]
		got := s.Pct()
		switch {
		case !baselined:
			if got < g.RepoMin {
				gerr.NewBelowFloor = append(gerr.NewBelowFloor, Regression{Package: pkg, Got: got, Want: g.RepoMin})
			}
		case got < want-g.Tolerance:
			gerr.Regressed = append(gerr.Regressed, Regression{Package: pkg, Got: got, Want: want})
		case got >= want+g.Tolerance+baselinePrecision:
			res.Improved = append(res.Improved, Regression{Package: pkg, Got: got, Want: want})
		}
	}

	if res.Measured < g.DiscoveryFloor {
		return res, &DiscoveryFloorError{Found: res.Measured, Floor: g.DiscoveryFloor}
	}

	for pkg := range baseline {
		if g.Exclude[pkg] {
			continue
		}
		if _, ok := measured[pkg]; !ok {
			gerr.Missing = append(gerr.Missing, pkg)
		}
	}
	for pkg := range g.Exclude {
		if _, ok := measured[pkg]; !ok {
			res.StaleExclusions = append(res.StaleExclusions, pkg)
		}
	}

	res.RepoPct = repo.Pct()
	gerr.RepoPct = res.RepoPct
	gerr.RepoBelow = res.RepoPct < g.RepoMin

	// Deterministic order, so two runs over the same inputs print the same
	// report and a CI log diffs cleanly against the last one.
	byPkg := func(a, b Regression) int {
		if a.Package < b.Package {
			return -1
		}
		if a.Package > b.Package {
			return 1
		}
		return 0
	}
	slices.SortFunc(gerr.Regressed, byPkg)
	slices.SortFunc(gerr.NewBelowFloor, byPkg)
	slices.SortFunc(res.Improved, byPkg)
	slices.Sort(gerr.Missing)
	slices.Sort(res.Excluded)
	slices.Sort(res.StaleExclusions)

	if gerr.Failed() {
		return res, gerr
	}
	return res, nil
}
