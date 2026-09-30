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
	"bufio"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// baselineHeader is written at the top of every generated baseline. It says
// what the file is and how it may change, because a reviewer seeing this file
// in a diff needs to know that a LOWERED number is the one thing to question.
const baselineHeader = `# Per-package coverage baseline for the coverage ratchet (go-qrdb, #500).
#
# Each line is "<import path> <percent>": the coverage that package is held to.
# CI fails when a listed package drops below its number (minus the tolerance),
# when a package NOT listed here is below the repo-wide floor, or when a listed
# package is not measured at all.
#
# The numbers hold only under the build-tag set recorded below: coverage
# depends on which files the tags compile. ` + "`make coverage`" + ` measures under the
# Makefile's BUILD_TAGS (the default feature set), and the gate refuses a
# profile measured under any other set. A per-package figure taken by hand is
# comparable only under the same tags -- ` + "`make print-build-tags`" + ` prints them.
#
# Regenerate with ` + "`make coverage-baseline`" + `, never by hand. A regenerated
# baseline may RAISE numbers freely. A LOWERED number in review is the one
# thing to question: it is a package whose coverage fell, being written down as
# the new normal.
`

// buildTagsDirective opens the one comment line a baseline gives meaning to:
// the build-tag set its numbers were measured under. A package's coverage
// depends on which of its files those tags compile, so the set is part of what
// the numbers mean. It is a comment so the line reads as prose in a diff.
const buildTagsDirective = "# build-tags:"

// Baseline is a parsed baseline file.
type Baseline struct {
	// Tags is the normalised build-tag set the numbers were measured under,
	// or "" when the file does not record one.
	Tags string
	// Held maps an import path to the coverage that package is held to.
	Held map[string]float64
}

// NormalizeTags returns a build-tag list as a sorted, de-duplicated,
// space-joined set, so order, repetition and spacing do not read as a
// different measurement.
func NormalizeTags(tags string) string {
	fields := strings.Fields(tags)
	slices.Sort(fields)
	return strings.Join(slices.Compact(fields), " ")
}

// ReadBaseline parses a baseline file. Blank lines and lines starting with '#'
// are ignored, except the single build-tags directive.
func ReadBaseline(name string, r io.Reader) (*Baseline, error) {
	out := &Baseline{Held: make(map[string]float64)}
	sawTags := false
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, buildTagsDirective); ok {
			if sawTags {
				return nil, &BaselineError{Path: name, Line: lineNo, Reason: "a second " + strconv.Quote(buildTagsDirective) + " line"}
			}
			sawTags = true
			out.Tags = NormalizeTags(rest)
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, &BaselineError{Path: name, Line: lineNo, Reason: "want \"<import path> <percent>\", got " + strconv.Quote(line)}
		}
		pct, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || pct < 0 || pct > 100 {
			return nil, &BaselineError{Path: name, Line: lineNo, Reason: "bad percentage " + strconv.Quote(fields[1]), Err: err}
		}
		if _, dup := out.Held[fields[0]]; dup {
			return nil, &BaselineError{Path: name, Line: lineNo, Reason: "package " + strconv.Quote(fields[0]) + " is listed twice"}
		}
		out.Held[fields[0]] = pct
	}
	if err := sc.Err(); err != nil {
		return nil, &BaselineError{Path: name, Reason: "read failed", Err: err}
	}
	return out, nil
}

// WriteBaseline writes the measured packages as a baseline, sorted by import
// path so a regeneration diffs line-for-line against the previous one, under
// a directive recording the build-tag set they were measured with.
//
// Percentages are floored to two decimals rather than rounded. Rounding up
// would hold a package to a number fractionally ABOVE what it measured, and
// the very next identical run would fail by that fraction.
func WriteBaseline(w io.Writer, measured map[string]Stats, tags string) error {
	if _, err := io.WriteString(w, baselineHeader); err != nil {
		return err
	}
	if _, err := io.WriteString(w, buildTagsDirective+" "+NormalizeTags(tags)+"\n"); err != nil {
		return err
	}
	pkgs := make([]string, 0, len(measured))
	for p := range measured {
		pkgs = append(pkgs, p)
	}
	slices.Sort(pkgs)
	for _, p := range pkgs {
		held := float64(int(measured[p].Pct()*100)) / 100
		if _, err := fmt.Fprintf(w, "%s %.2f\n", p, held); err != nil {
			return err
		}
	}
	return nil
}
