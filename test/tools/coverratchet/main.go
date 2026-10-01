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

// Command coverratchet is go-qrdb's coverage gate (#500): a repo-wide floor
// plus a per-package ratchet.
//
// AGENTS.md requires every package to reach the coverage floor, and until this
// tool nothing enforced it. Enforcing it outright would have failed 14
// packages on the day it arrived, so the owner ruled (2026-09-22) for the
// standard way to arm a gate on a codebase that does not meet it yet:
//
//   - the repo-wide total must meet the floor (it does);
//   - no package may fall below the coverage it has TODAY, recorded in a
//     checked-in baseline, so regression is blocked from the first push;
//   - a package NOT in the baseline -- new code -- must meet the floor on its
//     own, so only existing code is grandfathered;
//   - a baselined package that is not measured at all fails, so a package
//     whose tests stop running cannot leave the gate silently.
//
// The unit is each Go package on its own (owner ruling, same date), not the
// Makefile's coverage-* targets, some of which combine a package with its
// subpackages and so let a well-tested child carry a weak parent.
//
// Usage:
//
//	coverratchet -profile coverage/total.out -baseline test/coverage-baseline.txt -tags "codec_json raft ..." -min 91
//	coverratchet -profile coverage/total.out -baseline test/coverage-baseline.txt -tags "codec_json raft ..." -write
//
// -tags IS THE CALLER'S ASSERTION, NOT A MEASUREMENT. A Go coverage profile
// does not record the build tags it was produced under, so this tool cannot
// derive them: it trusts the caller to pass the set its `go test -tags` used.
// `make coverage` and `make coverage-baseline` pass $(BUILD_TAGS), the same
// variable their `go test` invocation expands, which is what makes the
// assertion true there. A caller that passes one set and measured under
// another defeats the check, and nothing here can detect that.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// defaultDiscoveryFloor is the fewest packages a real profile of this
// repository can plausibly measure. The census of 2026-09-22 measured 120; the
// floor sits far enough below that to survive packages being merged or
// removed, and far enough above zero that a truncated or wrongly-scoped
// profile fails instead of passing every check vacuously.
const defaultDiscoveryFloor = 50

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// config is the parsed command line.
type config struct {
	profile        string
	baseline       string
	min            float64
	tolerance      float64
	exclude        map[string]bool
	discoveryFloor int
	write          bool
	// tags is the normalised build-tag set the profile was measured under.
	tags string
}

func parseFlags(args []string, w io.Writer) (*config, error) {
	fs := flag.NewFlagSet("coverratchet", flag.ContinueOnError)
	fs.SetOutput(w)
	cfg := &config{exclude: map[string]bool{}}
	var exclude string
	fs.StringVar(&cfg.profile, "profile", "", "coverage profile from `go test -coverprofile`")
	fs.StringVar(&cfg.baseline, "baseline", "", "per-package baseline file")
	fs.Float64Var(&cfg.min, "min", 0, "repo-wide floor, and the floor for packages not in the baseline")
	fs.Float64Var(&cfg.tolerance, "tolerance", 0, "percentage points a baselined package may fall before it counts as a regression")
	fs.StringVar(&exclude, "exclude", "", "comma-separated import paths to leave out of every check (test helpers)")
	fs.IntVar(&cfg.discoveryFloor, "discovery-floor", defaultDiscoveryFloor, "fewest packages the profile must measure")
	fs.BoolVar(&cfg.write, "write", false, "regenerate the baseline from the profile instead of checking it")
	fs.StringVar(&cfg.tags, "tags", "", "space-separated build tags the caller ASSERTS the profile was measured under (a profile does not record them)")
	if err := fs.Parse(args); err != nil {
		return nil, &ConfigError{Reason: err.Error()}
	}

	if cfg.profile == "" {
		return nil, &ConfigError{Reason: "-profile is required"}
	}
	if cfg.tags = NormalizeTags(cfg.tags); cfg.tags == "" {
		return nil, &ConfigError{Reason: "-tags is required: a baseline is only comparable with a profile measured under the same build tags"}
	}
	if cfg.baseline == "" {
		return nil, &ConfigError{Reason: "-baseline is required"}
	}
	if !cfg.write && (cfg.min <= 0 || cfg.min > 100) {
		return nil, &ConfigError{Reason: fmt.Sprintf("-min must be in (0, 100], got %v", cfg.min)}
	}
	if cfg.tolerance < 0 || cfg.tolerance >= 100 {
		return nil, &ConfigError{Reason: fmt.Sprintf("-tolerance must be in [0, 100), got %v", cfg.tolerance)}
	}
	if cfg.discoveryFloor < 1 {
		return nil, &ConfigError{Reason: "-discovery-floor must be at least 1: a floor of zero lets an empty profile pass"}
	}
	for _, p := range strings.Split(exclude, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.exclude[p] = true
		}
	}
	return cfg, nil
}

func run(args []string, w io.Writer) error {
	cfg, err := parseFlags(args, w)
	if err != nil {
		return err
	}

	pf, err := os.Open(cfg.profile)
	if err != nil {
		return &ProfileError{Path: cfg.profile, Reason: "cannot open", Err: err}
	}
	measured, err := ParseProfile(cfg.profile, pf)
	_ = pf.Close()
	if err != nil {
		return err
	}

	if cfg.write {
		return writeBaseline(cfg, measured, w)
	}

	bf, err := os.Open(cfg.baseline)
	if err != nil {
		return &BaselineError{Path: cfg.baseline, Reason: "cannot open -- generate it with `make coverage-baseline`", Err: err}
	}
	baseline, err := ReadBaseline(cfg.baseline, bf)
	_ = bf.Close()
	if err != nil {
		return err
	}

	if baseline.Tags != cfg.tags {
		return &BuildTagsError{Path: cfg.baseline, Baseline: baseline.Tags, Profile: cfg.tags}
	}

	gate := Gate{RepoMin: cfg.min, Tolerance: cfg.tolerance, Exclude: cfg.exclude, DiscoveryFloor: cfg.discoveryFloor}
	res, evalErr := gate.Evaluate(measured, baseline.Held)
	report(w, gate, res)
	return evalErr
}

// writeBaseline regenerates the baseline, leaving excluded packages out.
func writeBaseline(cfg *config, measured map[string]Stats, w io.Writer) error {
	kept := make(map[string]Stats, len(measured))
	for p, s := range measured {
		if !cfg.exclude[p] {
			kept[p] = s
		}
	}
	if len(kept) < cfg.discoveryFloor {
		return &DiscoveryFloorError{Found: len(kept), Floor: cfg.discoveryFloor}
	}
	// #nosec G304,G703 -- the -baseline flag is a developer-supplied path on a
	// build tool (make coverage-baseline), never a request value.
	f, err := os.Create(cfg.baseline)
	if err != nil {
		return &BaselineError{Path: cfg.baseline, Reason: "cannot create", Err: err}
	}
	if err := WriteBaseline(f, kept, cfg.tags); err != nil {
		_ = f.Close()
		return &BaselineError{Path: cfg.baseline, Reason: "write failed", Err: err}
	}
	if err := f.Close(); err != nil {
		return &BaselineError{Path: cfg.baseline, Reason: "close failed", Err: err}
	}
	fmt.Fprintf(w, "coverratchet: wrote %d packages to %s\n", len(kept), cfg.baseline)
	return nil
}

// report prints what was checked. It runs on pass AND fail, so a green log
// still shows which packages were excluded and which have headroom -- a gate
// whose passing output is silent cannot be told apart from one that checked
// nothing.
func report(w io.Writer, g Gate, res *Result) {
	if res == nil {
		return
	}
	fmt.Fprintf(w, "coverratchet: %d packages checked, repo-wide %.2f%% (floor %.2f%%, tolerance %.2fpp)\n",
		res.Measured, res.RepoPct, g.RepoMin, g.Tolerance)
	for _, p := range res.Excluded {
		fmt.Fprintf(w, "  excluded   %s\n", p)
	}
	for _, p := range res.StaleExclusions {
		fmt.Fprintf(w, "  STALE      %s is excluded but was not measured -- remove it from the exclusion list\n", p)
	}
	for _, r := range res.Improved {
		fmt.Fprintf(w, "  headroom   %s: %.2f%%, baseline %.2f%% -- `make coverage-baseline` would raise it\n", r.Package, r.Got, r.Want)
	}
}
