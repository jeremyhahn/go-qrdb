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
	"fmt"
	"strconv"
	"strings"
)

// ConfigError is returned when the command line is unusable.
type ConfigError struct {
	// Reason describes what is wrong with the flags.
	Reason string
}

// Error implements the error interface.
func (e *ConfigError) Error() string { return "coverratchet: " + e.Reason }

// ProfileError is returned when a coverage profile cannot be read or parsed.
//
// A profile this tool cannot read is a FAILURE, never an empty pass: a gate
// that treats an unparseable input as "nothing to check" reports success for
// the one case where it knows least.
type ProfileError struct {
	// Path is the profile file.
	Path string
	// Line is the 1-based line that failed to parse, or 0 when the failure is
	// not tied to one line.
	Line int
	// Reason describes the failure.
	Reason string
	// Err is the underlying error, if any.
	Err error
}

// Error implements the error interface.
func (e *ProfileError) Error() string {
	loc := e.Path
	if e.Line > 0 {
		loc += ":" + strconv.Itoa(e.Line)
	}
	msg := "coverratchet: profile " + loc + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *ProfileError) Unwrap() error { return e.Err }

// BaselineError is returned when the baseline file cannot be read or parsed.
type BaselineError struct {
	// Path is the baseline file.
	Path string
	// Line is the 1-based line that failed to parse, or 0.
	Line int
	// Reason describes the failure.
	Reason string
	// Err is the underlying error, if any.
	Err error
}

// Error implements the error interface.
func (e *BaselineError) Error() string {
	loc := e.Path
	if e.Line > 0 {
		loc += ":" + strconv.Itoa(e.Line)
	}
	msg := "coverratchet: baseline " + loc + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *BaselineError) Unwrap() error { return e.Err }

// DiscoveryFloorError is returned when the profile names fewer packages than
// the floor.
//
// Every other check in this tool iterates over the packages it found. With
// none found, every loop is empty and every check passes. The floor is what
// makes "no packages" a failure rather than a clean bill of health.
type DiscoveryFloorError struct {
	// Found is how many measured packages the profile contained.
	Found int
	// Floor is the minimum this tool accepts.
	Floor int
}

// Error implements the error interface.
func (e *DiscoveryFloorError) Error() string {
	return fmt.Sprintf("coverratchet: the profile measured %d packages, below the discovery floor of %d: "+
		"an empty or truncated profile would otherwise pass every check vacuously", e.Found, e.Floor)
}

// Regression is one package whose coverage fell below what it is held to.
type Regression struct {
	// Package is the import path.
	Package string
	// Got is the measured percentage.
	Got float64
	// Want is the percentage it is held to.
	Want float64
}

// GateError reports every way the profile failed the gate, in one pass.
//
// It is deliberately an aggregate. A gate that stopped at the first failure
// would make a developer fix one package, push, and learn about the next --
// one CI round trip per finding. Every field is populated independently.
type GateError struct {
	// RepoPct is the measured repo-wide percentage, helpers excluded.
	RepoPct float64
	// RepoMin is the repo-wide floor. RepoPct < RepoMin is a failure.
	RepoMin float64
	// RepoBelow reports whether the repo-wide floor failed.
	RepoBelow bool
	// Regressed are baselined packages that dropped below baseline minus the
	// tolerance.
	Regressed []Regression
	// NewBelowFloor are packages absent from the baseline that do not meet the
	// repo-wide floor. New code meets the written standard; only existing
	// code is grandfathered.
	NewBelowFloor []Regression
	// Missing are baselined packages the profile did not measure at all.
	Missing []string
}

// Failed reports whether any check failed.
func (e *GateError) Failed() bool {
	return e.RepoBelow || len(e.Regressed) > 0 || len(e.NewBelowFloor) > 0 || len(e.Missing) > 0
}

// Error implements the error interface.
func (e *GateError) Error() string {
	var b strings.Builder
	b.WriteString("coverratchet: coverage gate failed")
	if e.RepoBelow {
		fmt.Fprintf(&b, "\n  repo-wide %.2f%% is below the floor of %.2f%%", e.RepoPct, e.RepoMin)
	}
	for _, r := range e.Regressed {
		fmt.Fprintf(&b, "\n  REGRESSED  %s: %.2f%%, baseline %.2f%%", r.Package, r.Got, r.Want)
	}
	for _, r := range e.NewBelowFloor {
		fmt.Fprintf(&b, "\n  NEW        %s: %.2f%%, below the %.2f%% floor that new packages must meet", r.Package, r.Got, r.Want)
	}
	for _, p := range e.Missing {
		fmt.Fprintf(&b, "\n  MISSING    %s: in the baseline but not measured -- if it was deleted, regenerate the baseline in the same change", p)
	}
	return b.String()
}
