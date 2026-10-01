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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The trivy severity bands, as trivy spells them.
//
// EVERY ONE OF THEM has to be named by some leg of the pipeline. That is the
// whole of #522: the repository's only severity list was `CRITICAL,HIGH`, so
// four of the five bands were reported by nothing, and a green Security signal
// could not distinguish "there are no MEDIUMs" from "nothing looked". Two real
// advisories — an authentication bypass in go-pkcs12's Decode functions and an
// ECharts XSS, both fixed upstream, both present since the beginning — sat
// under that floor and surfaced only because someone widened the list by hand
// for an unrelated demonstration.
//
// This gate is a property of the Makefile rather than of the program, which is
// why it lives here. Nothing about deleting `--severity MEDIUM,LOW` from a
// recipe fails to compile, fails a scan, or looks wrong in a diff: it just
// quietly restores the hole.
var trivySeverities = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

// severityFlag finds every `--severity <list>` in the text and returns the
// lists, in order of appearance.
var severityFlag = regexp.MustCompile(`--severity[ =]([A-Z,]+)`)

// makefile reads the repository Makefile.
func makefile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(b)
}

// makeRule is one target of the parsed Makefile: its prerequisites, in order
// of declaration across every rule line that names it, and its recipe with
// comments removed.
type makeRule struct {
	deps   []string
	recipe []string
}

// makefileRules is the parsed Makefile, every rule by target name.
//
// ONE parser, shared by every assertion in this file, so that a recipe the
// band tests read and a rule the reachability test walks are the same object.
// The first version of this gate read recipes by prefix-matching lines and
// nothing else, and the review that blocked it reproduced three mutations
// that parser could not see: a scan command turned into a comment still
// counted as a scan, a prerequisite edge two hops away was not an edge, and a
// nightly step that was deleted had never been asserted at all. Each of those
// is a way for the gate to stay green after the thing it guards is removed,
// which is the failure mode #489 names and the one this package exists to
// rule out.
type makefileRules map[string]*makeRule

// makeDirective matches the lines that carry a colon but are not rules.
var makeDirective = regexp.MustCompile(`^(?:ifeq|ifneq|ifdef|ifndef|else|endif|define|endef|-?include|sinclude|export|unexport|override|vpath|undefine|private)\b`)

// shellComment is where the shell stops reading a recipe line: a `#` that
// begins a word. A `#` inside a quoted string is truncated too, and that is
// the safe direction — a parser that drops real text fails the assertions
// below loudly; one that keeps commented-out text passes them silently.
var shellComment = regexp.MustCompile(`\s#`)

// makeInvocation finds the arguments of a recursive make call in a recipe
// line: `$(MAKE) x`, `${MAKE} x`, or a bare `make x` as a shell word. A target
// reached that way is reached, whatever the prerequisite list says — `ci`
// runs `_ci` inside a container and declares no prerequisite at all.
var makeInvocation = regexp.MustCompile(`(?:\$\(MAKE\)|\$\{MAKE\}|(?:^|[\s;&|(])make)\s+([^;&|)]*)`)

// stripShellComment returns the part of a recipe line the shell executes,
// with make's own `@`, `-` and `+` prefixes and surrounding blanks removed.
// A line that is entirely a comment comes back empty.
//
// GNU make passes a recipe line beginning with `#` to the shell unchanged
// (manual §5.1); the shell then ignores it. So `\t# trivy fs ...` is a line
// that runs nothing, and the first version of this parser counted its
// `--severity` list all the same.
func stripShellComment(cmd string) string {
	cmd = strings.TrimLeft(cmd, "@-+ \t")
	if strings.HasPrefix(cmd, "#") {
		return ""
	}
	if m := shellComment.FindStringIndex(cmd); m != nil {
		cmd = cmd[:m[0]]
	}
	return strings.TrimSpace(cmd)
}

// joinContinuations folds backslash-newline into a single line, which is how
// make reads a rule header and how the shell reads a recipe line.
func joinContinuations(src string) []string {
	var out []string
	var cur strings.Builder
	open := false
	for _, ln := range strings.Split(src, "\n") {
		if open {
			cur.WriteString(" ")
			cur.WriteString(strings.TrimLeft(ln, "\t "))
		} else {
			cur.WriteString(ln)
		}
		if strings.HasSuffix(ln, `\`) {
			s := cur.String()
			cur.Reset()
			cur.WriteString(strings.TrimRight(strings.TrimSuffix(s, `\`), " \t"))
			open = true
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
		open = false
	}
	if open {
		out = append(out, cur.String())
	}
	return out
}

// parseRuleLine reads `targets: prerequisites [; inline recipe]`. It reports
// ok=false for anything that is not a rule: an assignment (`:=`, `::=`, or
// `=` anywhere left of the colon), a directive, or a line with no colon.
func parseRuleLine(ln string) (targets, deps []string, inline string, ok bool) {
	if makeDirective.MatchString(strings.TrimSpace(ln)) {
		return nil, nil, "", false
	}
	i := strings.Index(ln, ":")
	if i < 0 {
		return nil, nil, "", false
	}
	lhs, rhs := ln[:i], ln[i+1:]
	if strings.Contains(lhs, "=") || strings.HasPrefix(rhs, "=") || strings.HasPrefix(rhs, ":=") {
		return nil, nil, "", false
	}
	rhs = strings.TrimPrefix(rhs, ":")
	if j := strings.Index(rhs, "#"); j >= 0 {
		rhs = rhs[:j]
	}
	if j := strings.Index(rhs, ";"); j >= 0 {
		inline = stripShellComment(rhs[j+1:])
		rhs = rhs[:j]
	}
	targets = strings.Fields(lhs)
	for _, d := range strings.Fields(rhs) {
		if d != "|" {
			deps = append(deps, d)
		}
	}
	return targets, deps, inline, len(targets) > 0
}

// parseMakefile builds the rule table.
//
// What it follows from GNU make §5.1 and what it deliberately does not:
//
//   - A recipe is the tab-indented lines after a rule, and it is ENDED by the
//     first line that is neither tab-indented, blank, nor a make comment. A
//     `#` line in column 0 in the middle of a recipe does not end it, so a
//     reader that stops there loses every line after it — the first version
//     of this parser did.
//   - Lines inside `define`/`endef` are a variable's value, not rules, even
//     when they look like either.
//   - Both arms of an `ifeq` are read. That over-approximates the graph,
//     which is the right error for an assertion of the form "X is NOT
//     reachable" and harmless for the floor that says "Y IS".
//   - Multiple rule lines for one target merge their prerequisites, as make
//     merges them.
func parseMakefile(src string) makefileRules {
	rules := makefileRules{}
	var current []string
	inDefine := false
	for _, ln := range joinContinuations(src) {
		trimmed := strings.TrimSpace(ln)
		if inDefine {
			if trimmed == "endef" {
				inDefine = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "define ") {
			inDefine = true
			current = nil
			continue
		}
		if strings.HasPrefix(ln, "\t") {
			if current == nil {
				continue
			}
			cmd := stripShellComment(strings.TrimPrefix(ln, "\t"))
			if cmd == "" {
				continue
			}
			for _, tgt := range current {
				rules[tgt].recipe = append(rules[tgt].recipe, cmd)
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		current = nil
		targets, deps, inline, ok := parseRuleLine(ln)
		if !ok {
			continue
		}
		for _, tgt := range targets {
			r := rules[tgt]
			if r == nil {
				r = &makeRule{}
				rules[tgt] = r
			}
			r.deps = append(r.deps, deps...)
			if inline != "" {
				r.recipe = append(r.recipe, inline)
			}
		}
		current = targets
	}
	return rules
}

// recipe returns a target's executable recipe lines joined by newlines, or
// the empty string for a target that does not exist. Every caller treats the
// empty string as a failure rather than as an empty recipe that vacuously
// satisfies whatever is asked of it — see
// TestTheSeverityGateRefusesAVacuousParse.
func (m makefileRules) recipe(name string) string {
	r := m[name]
	if r == nil {
		return ""
	}
	return strings.Join(r.recipe, "\n")
}

// edges returns every target one hop from name: its prerequisites, then any
// target its recipe invokes through a recursive make.
func (m makefileRules) edges(name string) []string {
	r := m[name]
	if r == nil {
		return nil
	}
	out := append([]string(nil), r.deps...)
	for _, ln := range r.recipe {
		for _, inv := range makeInvocation.FindAllStringSubmatch(ln, -1) {
			for _, arg := range strings.Fields(inv[1]) {
				if strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
					continue
				}
				if _, known := m[arg]; known {
					out = append(out, arg)
				}
			}
		}
	}
	return out
}

// path returns the chain of targets by which `to` is reached from `from`,
// beginning with `from` and ending with `to`, or nil when it is not reached.
// Breadth-first, so the chain reported is a shortest one, which is the one a
// reader wants in an error message.
func (m makefileRules) path(from, to string) []string {
	parent := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range m.edges(cur) {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = cur
			if next == to {
				var chain []string
				for n := to; n != ""; n = parent[n] {
					chain = append([]string{n}, chain...)
				}
				return chain
			}
			queue = append(queue, next)
		}
	}
	return nil
}

// expand substitutes the `TRIVY_*_SEVERITY` variables the recipes reference, so
// this gate reads the values that actually reach trivy rather than the literal
// `$(TRIVY_GATE_SEVERITY)`. A variable that cannot be resolved is left alone
// and therefore fails the band assertions loudly, which is the right direction.
func expand(src, recipe string) string {
	for _, v := range []string{"TRIVY_GATE_SEVERITY", "TRIVY_REPORT_SEVERITY", "TRIVY_SCOPE"} {
		re := regexp.MustCompile(`(?m)^` + v + `\s*:?=\s*(\S+)\s*$`)
		m := re.FindStringSubmatch(src)
		if m == nil {
			continue
		}
		recipe = strings.ReplaceAll(recipe, "$("+v+")", m[1])
	}
	return recipe
}

// bands returns the set of severities named anywhere in a recipe.
func bands(recipe string) map[string]bool {
	got := map[string]bool{}
	for _, m := range severityFlag.FindAllStringSubmatch(recipe, -1) {
		for _, s := range strings.Split(m[1], ",") {
			got[strings.TrimSpace(s)] = true
		}
	}
	return got
}

// TestEverySeverityBandIsNamedBySomeLeg is the assertion #522 turns on. Between
// the blocking gate and the sub-threshold report, all five bands are covered.
func TestEverySeverityBandIsNamedBySomeLeg(t *testing.T) {
	src := makefile(t)
	mk := parseMakefile(src)
	covered := map[string]bool{}
	for _, target := range []string{"trivy", "trivy-report"} {
		r := expand(src, mk.recipe(target))
		if r == "" {
			t.Fatalf("target %q has no recipe in the Makefile; this gate cannot rule on a tree it could not parse", target)
		}
		for band := range bands(r) {
			covered[band] = true
		}
	}
	for _, band := range trivySeverities {
		if !covered[band] {
			t.Errorf("trivy severity %q is named by NO leg of the pipeline, so nothing can ever report one. "+
				"That is the defect #522 records: `make trivy` gated on CRITICAL,HIGH alone and four bands went unreported for the whole of this repository's history. "+
				"Name it in `trivy` (blocking) or in `trivy-report` (advisory), and say in a comment which and why", band)
		}
	}
}

// TestTheBlockingGateStaysAtCriticalAndHigh pins the floor that must NOT move.
//
// devops.md §8 sets the blocking threshold at CRITICAL/HIGH deliberately, and
// #522's own body says so again: a gate that blocks a merge on a MEDIUM is one
// people learn to route around. Widening `trivy` is the tempting one-word fix
// for the hole the test above describes, and it is the wrong fix.
func TestTheBlockingGateStaysAtCriticalAndHigh(t *testing.T) {
	src := makefile(t)
	r := expand(src, parseMakefile(src).recipe("trivy"))
	if r == "" {
		t.Fatal("no `trivy` recipe in the Makefile")
	}
	got := bands(r)
	for _, band := range []string{"CRITICAL", "HIGH"} {
		if !got[band] {
			t.Errorf("the blocking trivy gate does not name %q; it must block on both", band)
		}
	}
	for _, band := range []string{"MEDIUM", "LOW", "UNKNOWN"} {
		if got[band] {
			t.Errorf("the blocking trivy gate names %q. The blocking floor is CRITICAL/HIGH (devops.md §8) and does not move: "+
				"sub-threshold findings belong in `trivy-report`, which fails the nightly step and blocks no merge", band)
		}
	}
}

// TestTheReportLegFailsOnAFindingItCanActOn — an advisory leg that exits 0 is
// the state this repository was already in.
//
// The bands with an available fix (MEDIUM, LOW) exit 1 so the nightly can
// transition to red, which is the only signal shape anyone reads (#478). The
// UNKNOWN band exits 0 because trivy scores nothing there: on this tree it is
// golang.org/x/crypto/openpgp, deprecated upstream with an EMPTY fixed
// version, so a red-on-UNKNOWN leg would be red from its first run with nothing
// anyone could do about it — a guard red on the day it lands is a guard nobody
// arms.
//
// The recipe read here has had its comments removed, so a scan line turned
// into `# trivy fs ...` is a scan line that is not there, and the leg reports
// nothing — which is what this test then says.
func TestTheReportLegFailsOnAFindingItCanActOn(t *testing.T) {
	src := makefile(t)
	r := expand(src, parseMakefile(src).recipe("trivy-report"))
	if r == "" {
		t.Fatal("no `trivy-report` recipe in the Makefile")
	}
	var actionable, advisory string
	for _, ln := range strings.Split(r, "\n") {
		if !strings.Contains(ln, "--severity") {
			continue
		}
		switch {
		case strings.Contains(ln, "UNKNOWN"):
			advisory = ln
		default:
			actionable = ln
		}
	}
	if actionable == "" {
		t.Fatal("`trivy-report` names no severity band below the gate; it reports nothing")
	}
	if !strings.Contains(actionable, "--exit-code 1") {
		t.Errorf("the sub-threshold scan does not use --exit-code 1: %q. "+
			"An exit-0 step in a nightly is indistinguishable from no step at all, which is exactly the state #522 exists to end", actionable)
	}
	if advisory == "" {
		t.Fatal("`trivy-report` does not scan the UNKNOWN band at all, so that band is reported by nothing")
	}
	if !strings.Contains(advisory, "--exit-code 0") {
		t.Errorf("the UNKNOWN scan does not use --exit-code 0: %q. "+
			"UNKNOWN carries findings with no fixed version, so failing on it makes the leg permanently red", advisory)
	}
}

// blockingRoots are the Makefile entry points that block a merge or claim to
// mirror the path that does. `security` is devops.md §8's contract; `ci` runs
// `_ci` in the devcontainer and `_ci` is what it runs.
var blockingRoots = []string{"security", "ci", "_ci"}

// TestTheReportLegIsNotReachableFromTheBlockingContract keeps MEDIUM from
// becoming blocking by the back door — ANY back door.
//
// A MEDIUM-red prerequisite anywhere under `security` or `_ci` makes the
// reporting floor a gating floor without anybody deciding that it should be.
// TRANSITIVELY: the first version of this test read the direct prerequisite
// list and nothing else, and `trivy: trivy-report` — one hop further down —
// passed it while making every MEDIUM block `make security`. The walk here
// follows prerequisites and recursive `make` invocations to any depth and
// reports the chain it found.
//
// The floor is that `trivy` IS reached from every root. A walk that finds
// nothing because the graph parser broke would report `trivy-report`
// unreachable and pass; a walk that cannot find the blocking gate has not
// parsed the tree it claims to rule on.
func TestTheReportLegIsNotReachableFromTheBlockingContract(t *testing.T) {
	mk := parseMakefile(makefile(t))
	for _, root := range blockingRoots {
		if mk[root] == nil {
			t.Fatalf("no `%s` rule in the Makefile; this gate cannot rule on a tree it could not parse", root)
		}
		if mk.path(root, "trivy") == nil {
			t.Fatalf("`%s` does not reach `trivy`, the blocking gate. Either the security suite lost its trivy leg "+
				"(devops.md §8 requires it) or this gate's graph walk is broken, and both would let the assertion below pass over nothing", root)
		}
		if chain := mk.path(root, "trivy-report"); chain != nil {
			t.Errorf("`%s` reaches `trivy-report` via %s. That makes the MEDIUM/LOW reporting floor a BLOCKING floor, "+
				"which is the one thing #522 rules out: the report runs in nightly.yml, where it gates nothing",
				root, strings.Join(chain, " -> "))
		}
	}
}

// workflowStep is the part of a step that decides whether it runs and whether
// its failure counts. Everything else about a step is irrelevant to this gate.
//
// `continue-on-error` is read as a string so that the bare YAML boolean and an
// `${{ }}` expression both arrive; anything but absent or `false` is a step
// whose red does not fail the job.
type workflowStep struct {
	Name            string `yaml:"name"`
	Run             string `yaml:"run"`
	If              string `yaml:"if"`
	ContinueOnError string `yaml:"continue-on-error"`
}

// jobSteps returns the steps of one job of a workflow file, in order.
//
// PARSED, not regexed, for the reasons action_pins_test.go gives on
// collectUses: a line-oriented reader sees one spelling and reports prose
// inside block scalars as steps. The nightly file's Trivy job carries fifty
// lines of commentary that mention `make trivy` and `make trivy-report` by
// name, and that is exactly what a grep would find after the step was gone.
func jobSteps(t *testing.T, path, job string) []workflowStep {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not parseable YAML: %v", path, err)
	}
	j, ok := doc.Jobs[job]
	if !ok {
		t.Fatalf("%s has no `%s` job; this gate cannot rule on a job it could not find", path, job)
	}
	return j.Steps
}

// trivyWiringDefects returns every way a Trivy job's steps fail to run the
// blocking gate and then the report, on every host, with a red that counts.
// An empty result is the wiring #522 asks for.
//
// The match is on the exact `run:` text, deliberately. `make trivy-report ||
// true` runs the target and reports nothing, and a looser match would call
// that wired.
func trivyWiringDefects(steps []workflowStep) []string {
	gate, report := -1, -1
	for i, s := range steps {
		switch strings.TrimSpace(s.Run) {
		case "make trivy":
			if gate < 0 {
				gate = i
			}
		case "make trivy-report":
			if report < 0 {
				report = i
			}
		}
	}
	var out []string
	if gate < 0 {
		out = append(out, "no step runs exactly `make trivy`: the blocking CRITICAL/HIGH gate is not invoked by the nightly Trivy job")
	}
	if report < 0 {
		out = append(out, "no step runs exactly `make trivy-report`: the sub-threshold report is not invoked by the nightly Trivy job, "+
			"so MEDIUM and LOW are once again reported by nothing — the exact state #522 exists to end")
	}
	if gate >= 0 && report >= 0 && report < gate {
		out = append(out, "`make trivy-report` runs BEFORE `make trivy`. The order is the disambiguation: "+
			"gate first, so a red gate skips the report and \"gate green, report red\" is readable from the step list")
	}
	for _, i := range []int{gate, report} {
		if i < 0 {
			continue
		}
		s := steps[i]
		if s.If != "" {
			out = append(out, fmt.Sprintf("step %q carries `if: %s`. The CLI gate and the report run on BOTH hosts; "+
				"a host guard here makes one of them vanish on the plane this repository's CI actually runs on", s.Name, s.If))
		}
		if s.ContinueOnError != "" && s.ContinueOnError != "false" {
			out = append(out, fmt.Sprintf("step %q has `continue-on-error: %s`, so its red does not fail the job. "+
				"An exit that cannot turn the nightly red is indistinguishable from no step at all (#478)", s.Name, s.ContinueOnError))
		}
	}
	return out
}

// TestNightlyRunsTheGateThenTheReport asserts that the Makefile targets the
// tests above rule on are actually INVOKED, in order, unconditionally, with a
// failure that counts.
//
// Without this, every other assertion in this file is about a recipe nobody
// runs: the first version of this gate passed with `run: make trivy-report`
// deleted from nightly.yml, and so did every other test in the tree, because
// the wiring had never been asserted. The property #522 claims is end to end
// — a MEDIUM is reported to somebody — and the step is where the end is.
func TestNightlyRunsTheGateThenTheReport(t *testing.T) {
	steps := jobSteps(t, filepath.Join(repoRoot(t), ".github", "workflows", "nightly.yml"), "trivy")
	if len(steps) == 0 {
		t.Fatal("nightly.yml's trivy job has no steps; this gate cannot rule on a job it could not read")
	}
	for _, d := range trivyWiringDefects(steps) {
		t.Error(d)
	}
}

// TestTheWiringCheckSeesEachWayTheReportCanBeUnplugged runs trivyWiringDefects
// over the mutations the review reproduced, plus the ones a reader reaches for
// next, each written as the YAML a workflow would carry and read back through
// the same parser the real assertion uses.
//
// A check nobody has watched go red is a check nobody has verified; the
// `wired` case at the top is the shape nightly.yml has, and every other case
// is one edit away from it.
func TestTheWiringCheckSeesEachWayTheReportCanBeUnplugged(t *testing.T) {
	const wired = "jobs:\n  trivy:\n    steps:\n" +
		"      - name: Trivy gate\n        run: make trivy\n" +
		"      - name: Trivy report\n        run: make trivy-report\n"
	for _, tc := range []struct {
		name, body, want string
	}{
		{"wired as nightly.yml is", wired, ""},
		{"a block-scalar run is the same step", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - run: |\n          make trivy-report\n", ""},
		{"the report step deleted", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n", "no step runs exactly `make trivy-report`"},
		{"the gate step deleted", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy-report\n", "no step runs exactly `make trivy`"},
		{"the report only in commentary", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      # - run: make trivy-report\n", "no step runs exactly `make trivy-report`"},
		{"the report before the gate", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy-report\n" +
			"      - run: make trivy\n", "runs BEFORE"},
		{"the report told to succeed", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - run: make trivy-report || true\n", "no step runs exactly `make trivy-report`"},
		{"the report guarded to one host", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - name: r\n        if: github.server_url == 'https://github.com'\n        run: make trivy-report\n", "carries `if:"},
		{"the report allowed to fail as a boolean", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - name: r\n        run: make trivy-report\n        continue-on-error: true\n", "continue-on-error: true"},
		{"the report allowed to fail as an expression", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - name: r\n        run: make trivy-report\n        continue-on-error: ${{ true }}\n", "continue-on-error:"},
		{"an explicit false is not a defect", "jobs:\n  trivy:\n    steps:\n" +
			"      - run: make trivy\n" +
			"      - run: make trivy-report\n        continue-on-error: false\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nightly.yml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := trivyWiringDefects(jobSteps(t, path, "trivy"))
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("reported %v over a correctly wired job", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("reported nothing; this mutation unplugs the report and the check did not see it:\n%s", tc.body)
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("reported %v, want a defect mentioning %q", got, tc.want)
			}
		})
	}
}

// TestTheMakefileParserSeesWhatMakeSees is the parser's own conformance
// table, on synthetic input, so that each rule it claims to follow has been
// watched to hold.
//
// The three cases marked with the review are the mutations that passed the
// first version of this gate. The rest are the neighbouring ways the same
// parser would fail open, found by reading GNU make §5.1 against the code.
func TestTheMakefileParserSeesWhatMakeSees(t *testing.T) {
	const src = "" +
		"VAR := a:b\n" +
		"OTHER = x:y\n" +
		"ifeq ($(X),a:b)\n" +
		"endif\n" +
		"define body\n" +
		"phantom: trivy-report\n" +
		"\ttrivy fs --severity LOW .\n" +
		"endef\n" +
		".PHONY: trivy trivy-report\n" +
		"trivy: trivy-report # review: a prerequisite the direct-list check never saw\n" +
		"\t# trivy fs --severity MEDIUM --exit-code 1 .   # review: a commented-out scan\n" +
		"\t@# also a comment\n" +
		"\ttrivy fs --severity CRITICAL,HIGH \\\n" +
		"\t\t--exit-code 1 . # trailing --severity LOW\n" +
		"# a make comment between recipe lines does not end the recipe\n" +
		"\n" +
		"\t@echo \"still $(RECIPE)\"\n" +
		"trivy-report:\n" +
		"\ttrivy fs --severity MEDIUM --exit-code 1 .\n" +
		"security: gosec vuln | trivy\n" +
		"ci:\n" +
		"\tdocker compose run --rm devcontainer make _ci\n" +
		"_ci: lint security\n" +
		"later: ; @echo inline\n" +
		"security: mod-verify\n" +
		"gosec vuln mod-verify lint:\n"

	mk := parseMakefile(src)

	// Assignments and directives carrying a colon are not rules.
	for _, notARule := range []string{"VAR", "OTHER", "ifeq ($(X),a", "phantom"} {
		if mk[notARule] != nil {
			t.Errorf("%q was read as a rule", notARule)
		}
	}
	// Comments are gone from the recipe; the continuation is one line; the
	// make comment did not end the recipe.
	got := mk.recipe("trivy")
	want := "trivy fs --severity CRITICAL,HIGH --exit-code 1 .\necho \"still $(RECIPE)\""
	if got != want {
		t.Errorf("trivy recipe parsed as:\n%s\nwant:\n%s", got, want)
	}
	if b := bands(got); b["MEDIUM"] || b["LOW"] || len(b) != 2 {
		t.Errorf("bands read from the comment-stripped recipe are %v; a commented-out scan must name nothing", b)
	}
	// Prerequisites merge across rule lines; order-only prerequisites count.
	if deps := strings.Join(mk["security"].deps, " "); deps != "gosec vuln trivy mod-verify" {
		t.Errorf("security prerequisites parsed as %q", deps)
	}
	// An inline recipe is a recipe.
	if r := mk.recipe("later"); r != "echo inline" {
		t.Errorf("inline recipe parsed as %q", r)
	}
	// The transitive edge and the recursive-make edge are both walked.
	if chain := mk.path("security", "trivy-report"); strings.Join(chain, " -> ") != "security -> trivy -> trivy-report" {
		t.Errorf("security -> trivy -> trivy-report was not found; got %v", chain)
	}
	if chain := mk.path("ci", "trivy"); strings.Join(chain, " -> ") != "ci -> _ci -> security -> trivy" {
		t.Errorf("the recursive `make _ci` edge was not walked; got %v", chain)
	}
	if chain := mk.path("lint", "trivy"); chain != nil {
		t.Errorf("lint does not reach trivy, yet the walk returned %v", chain)
	}
}

// TestTheSeverityGateRefusesAVacuousParse is the discovery floor.
//
// Every assertion above is over the recipe of a target found by name. If the
// parse finds nothing — the Makefile moved, the target was renamed, `recipe`
// broke — those assertions pass over empty strings and the gate reports a
// property it never checked. This is the test that fails instead.
func TestTheSeverityGateRefusesAVacuousParse(t *testing.T) {
	src := makefile(t)
	if len(src) < 1000 {
		t.Fatalf("the Makefile read back as %d bytes; this gate did not read the file it claims to rule on", len(src))
	}
	mk := parseMakefile(src)
	for _, target := range []string{"trivy", "trivy-report"} {
		r := mk.recipe(target)
		if !strings.Contains(r, "trivy fs") {
			t.Errorf("the recipe parsed for %q contains no `trivy fs` invocation: %q. "+
				"Either the target is gone or this gate's parser is, and both look like a pass to every other assertion in this file", target, r)
		}
	}
	if got := bands("--severity CRITICAL,HIGH"); !got["CRITICAL"] || !got["HIGH"] || len(got) != 2 {
		t.Errorf("the severity parser does not read a known-good list correctly: %v", got)
	}
	if got := bands("no flag here"); len(got) != 0 {
		t.Errorf("the severity parser invented bands from text with no --severity flag: %v", got)
	}
	if r := mk.recipe("no-such-target-anywhere"); r != "" {
		t.Errorf("a target that does not exist parsed to a recipe: %q", r)
	}
}

// TestEveryScanLegIncludesDevDependencies is the assertion #530 turns on.
//
// trivy's npm parser EXCLUDES a lockfile's dev dependencies unless
// `--include-dev-deps` is passed, so a scan line without the flag is green on
// every HIGH in the frontend's build toolchain — the tools that produce the
// shipped bundle. That is a scope gap, not a severity one: the severity gate
// above at least reports the band above its floor, while this reported nothing
// about a whole dependency tree, and three HIGHs sat AT the blocking floor
// unseen. Every `trivy fs` scan on both legs must carry the flag; a leg that
// drops it (or a `$(TRIVY_SCOPE)` that expands to nothing) fails here rather
// than passing as a scan of the whole tree.
func TestEveryScanLegIncludesDevDependencies(t *testing.T) {
	src := makefile(t)
	mk := parseMakefile(src)
	var scans int
	for _, target := range []string{"trivy", "trivy-report"} {
		r := expand(src, mk.recipe(target))
		if r == "" {
			t.Fatalf("target %q has no recipe in the Makefile; this gate cannot rule on a tree it could not parse", target)
		}
		for _, line := range strings.Split(r, "\n") {
			if !strings.Contains(line, "trivy fs") || strings.Contains(line, "--download-db-only") {
				continue
			}
			scans++
			if !strings.Contains(line, "--include-dev-deps") {
				t.Errorf("target %q scan line lacks --include-dev-deps, so the frontend's build toolchain (vite, esbuild, postcss) is outside its scope and a HIGH there is invisible to it (go-qrdb#530):\n\t%s", target, line)
			}
		}
	}
	// A DISCOVERY FLOOR, for the same reason the action-pin gate carries one:
	// the loop above reports success over zero scan lines. Three is what the
	// tree holds today — one on the blocking leg, two on the report leg.
	if scans < 3 {
		t.Fatalf("found %d trivy scan lines across the two legs, expected at least 3; the recipe parse has broken and this gate is verifying nothing", scans)
	}
}
