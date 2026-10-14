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

//go:build clusterproof

package soak

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"
)

// Env is what the entry points read and write: the process's environment and
// streams, or a test's.
type Env struct {
	Getenv  func(string) string
	Stdout  io.Writer
	Stderr  io.Writer
	Harness Harness
	Now     func() time.Time
	// Configure adjusts the options LoadOptions built before the run. It is
	// the seam for what the environment cannot name: a linearizability
	// checker, an E3 source, or an in-process cluster. Nil changes nothing.
	Configure func(*Options) error
}

// Main runs a soak configured from the environment and returns its exit
// code: ExitProven, ExitViolated, or ExitInvalid when the configuration, the
// run or the bundle is invalid.
func Main(ctx context.Context, env Env) int {
	o, err := LoadOptions(env.Getenv, env.now())
	if err == nil && env.Configure != nil {
		err = env.Configure(o)
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "clusterproof: invalid configuration:", err)
		return ExitInvalid
	}
	o.Harness = env.Harness
	if o.Progress == nil {
		o.Progress = env.Stderr
	}
	fmt.Fprintf(env.Stdout, "clusterproof: %d nodes, %s, faults %v, seed %d, %d windows, bundle %s\n",
		len(o.Nodes), o.Duration, faultNames(o), o.Schedule.Seed, len(o.Schedule.Windows), o.Bundle)
	res, err := Run(ctx, *o)
	if err != nil {
		fmt.Fprintln(env.Stderr, "clusterproof: run failed:", err)
		return ExitInvalid
	}
	report(env.Stdout, res.Verdict, res.Bundle, res.ManifestSHA256)
	return res.Verdict.Exit
}

// CheckMain verifies the bundle PROOF_BUNDLE names, or the newest one under
// DefaultBundleRoot, and judges it again with no cluster. A bundle that fails
// verification is ExitInvalid; otherwise the exit is the verdict's.
func CheckMain(_ context.Context, env Env) int {
	dir := env.Getenv(EnvBundle)
	if dir == "" {
		var err error
		if dir, err = latestBundle(DefaultBundleRoot); err != nil {
			fmt.Fprintln(env.Stderr, "clusterproof: invalid configuration:", err)
			return ExitInvalid
		}
	}
	var opts CheckOptions
	if env.Configure != nil {
		o := &Options{}
		if err := env.Configure(o); err != nil {
			fmt.Fprintln(env.Stderr, "clusterproof: invalid configuration:", err)
			return ExitInvalid
		}
		opts.Checkers = o.Checkers
	}
	res, err := Check(dir, opts)
	if err != nil {
		fmt.Fprintln(env.Stderr, "clusterproof: bundle invalid:", err)
		return ExitInvalid
	}
	fmt.Fprintln(env.Stdout, "clusterproof: every file matches its manifest checksum and the verdict follows from the evidence")
	report(env.Stdout, res.Verdict, dir, res.ManifestSHA256)
	return res.Verdict.Exit
}

// report prints a verdict for the operator.
func report(w io.Writer, v Verdict, dir, manifest string) {
	fmt.Fprintf(w, "clusterproof: %s under seal %s (exit %d)\n", v.Outcome, v.Seal, v.Exit)
	for _, r := range v.Reasons {
		fmt.Fprintln(w, "  reason:", r)
	}
	for _, c := range v.Criteria {
		if c.Detail == "" {
			fmt.Fprintf(w, "  %-3s %-11s %s\n", c.ID, c.Status, c.Name)
			continue
		}
		fmt.Fprintf(w, "  %-3s %-11s %s: %s\n", c.ID, c.Status, c.Name, c.Detail)
	}
	fmt.Fprintln(w, "  bundle:", dir)
	fmt.Fprintln(w, "  manifest sha256:", manifest)
}

func (e Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

// faultNames lists the configured faults, or "none".
func faultNames(o *Options) []string {
	if len(o.Nemeses) == 0 {
		return []string{nemesisNone}
	}
	return slices.Sorted(maps.Keys(o.Nemeses))
}
