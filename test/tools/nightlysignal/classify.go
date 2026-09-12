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
	"regexp"
	"strconv"
	"strings"
)

// Verdict is what one job's result means to this tool.
type Verdict int

const (
	// VerdictInconclusive is a job that neither passed nor failed — cancelled,
	// skipped, or never scheduled. It is the DEFAULT so that a zero value can
	// never be mistaken for a pass: an inconclusive run must not close a
	// tracking issue, because it proved nothing about the property.
	VerdictInconclusive Verdict = iota
	// VerdictRed is a failed job.
	VerdictRed
	// VerdictGreen is a passing job.
	VerdictGreen
)

// String names the verdict for the run summary.
func (v Verdict) String() string {
	switch v {
	case VerdictRed:
		return "red"
	case VerdictGreen:
		return "green"
	default:
		return "inconclusive"
	}
}

// The four results Actions can report for a `needs` job. Anything else is
// refused by UnknownResultError rather than folded into one of these.
const (
	resultSuccess   = "success"
	resultFailure   = "failure"
	resultCancelled = "cancelled"
	resultSkipped   = "skipped"
)

// VerdictOf maps an Actions job result onto a Verdict.
func VerdictOf(job, result string) (Verdict, error) {
	switch result {
	case resultSuccess:
		return VerdictGreen, nil
	case resultFailure:
		return VerdictRed, nil
	case resultCancelled, resultSkipped:
		return VerdictInconclusive, nil
	default:
		return VerdictInconclusive, &UnknownResultError{Job: job, Result: result}
	}
}

// Provenance is how a red job got to be red, and it is the ONE THING that makes
// this mechanism different from a notifier.
//
// A notifier that fires identically on the first failure and the two-hundredth
// trains its reader to ignore it, and then the mechanism is intact while the
// property is gone. So the shape of the signal is chosen from here: an issue is
// opened once, on the transition, and what it SAYS depends on which of these
// four it was.
type Provenance int

const (
	// ProvenanceUnknown is a red job with no retained history on this ref to
	// compare against — the first run on a new branch, or a window that has
	// aged out. Reported as unknown rather than guessed at: "no green run was
	// found" and "no run was found" are different facts.
	ProvenanceUnknown Provenance = iota
	// ProvenanceRegression is a red job whose immediately preceding run on this
	// ref was green. Something broke, and the run that broke it is nameable.
	ProvenanceRegression
	// ProvenanceResumed is a red job that was already red on the preceding run
	// but has no open tracking issue — the first run after this mechanism was
	// installed, or the first after someone closed the issue by hand while the
	// job was still failing.
	ProvenanceResumed
	// ProvenanceNeverGreen is a red job with no green run anywhere in the
	// retained history. The LOUDEST case, and the one this repository was
	// actually in: nightly.yml had 22 retained runs on 2026-09-12 and every one
	// of them was red, so the absence of a green run was itself the finding and
	// nobody had been in a position to notice it.
	ProvenanceNeverGreen
)

// provenanceNames is the wire spelling of each provenance, used both in the
// run summary and in the issue marker.
//
// Map-based rather than a switch, per the repository convention, and ONE map
// rather than two so the spelling written into a marker and the spelling read
// back out cannot drift.
var provenanceNames = map[Provenance]string{
	ProvenanceUnknown:    "unknown-history",
	ProvenanceRegression: "regression",
	ProvenanceResumed:    "resumed",
	ProvenanceNeverGreen: "never-green",
}

// String names the provenance for the run summary and the marker.
func (p Provenance) String() string {
	if name, ok := provenanceNames[p]; ok {
		return name
	}
	return provenanceNames[ProvenanceUnknown]
}

// ProvenanceNamed is the inverse of String, reporting whether the name was one
// this version writes.
//
// An unrecognised name is NOT mapped to unknown-history: it means the marker was
// written by a version that classified differently, and silently flattening it
// would replace a statement someone can act on with one they cannot.
func ProvenanceNamed(name string) (Provenance, bool) {
	for p, n := range provenanceNames {
		if n == name {
			return p, true
		}
	}
	return ProvenanceUnknown, false
}

// Action is what this tool does about one job.
type Action int

const (
	// ActionNone is the quiet case: green with nothing open, or inconclusive.
	ActionNone Action = iota
	// ActionOpen is the transition to red — the only case that notifies.
	ActionOpen
	// ActionUpdate is still red: the open issue is rewritten in place.
	ActionUpdate
	// ActionClose is the transition to green.
	ActionClose
)

// String names the action for the run summary.
func (a Action) String() string {
	switch a {
	case ActionOpen:
		return "open"
	case ActionUpdate:
		return "update"
	case ActionClose:
		return "close"
	default:
		return "none"
	}
}

// Decide maps a verdict and the presence of an open tracking issue onto an
// action.
//
// The tracking issue IS the memory. Newly-red and still-red are told apart by
// whether one is open, not by re-deriving history — which means the mechanism
// keeps working when the run-history window ages out, and it means closing the
// issue by hand while the job is still red deliberately re-arms the signal.
func Decide(v Verdict, hasIssue bool) Action {
	switch {
	case v == VerdictRed && !hasIssue:
		return ActionOpen
	case v == VerdictRed:
		return ActionUpdate
	case v == VerdictGreen && hasIssue:
		return ActionClose
	default:
		return ActionNone
	}
}

// History is what the retained run window says about one job on one ref.
type History struct {
	// Examined is the number of prior completed runs in which this job
	// appeared. Zero means the window held nothing to compare against, which is
	// NOT the same as never green.
	Examined int
	// Streak is how many of the most recent prior runs failed, consecutively.
	Streak int
	// LastGreenRun is the id of the most recent run in which the job passed,
	// or 0 when there is none in the window.
	LastGreenRun int64
	// LastGreenAt is when that run started, empty when there is none.
	LastGreenAt string
	// Window is how many runs of the workflow were listed, so a reader can tell
	// "never green" from "never green as far back as anyone can see".
	Window int
}

// ProvenanceOf classifies a red job from its history.
func ProvenanceOf(h History) Provenance {
	switch {
	case h.Examined == 0:
		return ProvenanceUnknown
	case h.LastGreenRun == 0:
		return ProvenanceNeverGreen
	case h.Streak == 0:
		return ProvenanceRegression
	default:
		return ProvenanceResumed
	}
}

// JobSpec is one job this tool watches, as named by a -job flag.
type JobSpec struct {
	// Key is the YAML key under `jobs:` in the workflow file.
	Key string
	// Name is the job's `name:`, which is what the runs API reports.
	Name string
	// Result is the `needs.<key>.result` the workflow passed in.
	Result string
}

// RunRef identifies the run being reported on.
type RunRef struct {
	ID        int64
	URL       string
	StartedAt string
	SHA       string
	Event     string
}

// Marker is the machine-readable key this tool writes into every issue it
// opens, and reads back to find that issue again.
//
// A MARKER RATHER THAN A TITLE MATCH. A title is prose a human may reasonably
// edit; a marker in an HTML comment is invisible in the rendered issue and
// survives retitling, so renaming the issue cannot make the next nightly open a
// second one.
type Marker struct {
	// Job is the workflow job key.
	Job string
	// Ref is the git ref the run was on. Part of the key because a nightly
	// dispatched on a branch is a statement about THAT branch: without the ref
	// a lane's dispatch would update — or close — main's tracking issue.
	Ref string
	// FirstRun is the run on which the issue was opened. Carried in the marker
	// so a rewrite does not lose it; there is nowhere else it survives.
	FirstRun int64
	// FirstAt is when that run started.
	FirstAt string
	// Prov is how the job BECAME red, in provenanceNames spelling, fixed at the
	// moment the issue was opened.
	//
	// IT IS CARRIED HERE BECAUSE IT MUST NOT BE RECOMPUTED. Provenance answers
	// "how did this break", and once an issue is open every later run answers
	// that question differently — the run before is now itself a failure, so a
	// regression re-derives as `resumed` on the very next night. That was not a
	// theoretical concern: it happened on the live demonstration of this
	// mechanism (run 17318), where the body came back saying the job "was
	// already red before this signal existed" under a title that said it had
	// just gone red. The title was right and the body was wrong, and they
	// disagreed because only one of them was being preserved.
	//
	// Empty is legal and means a marker written before this field existed; the
	// caller falls back to the freshly computed classification.
	Prov string
}

const markerVersion = "v1"

// MarkerPrefix is what identifies an issue as this job's tracker, independent
// of when it was opened.
func MarkerPrefix(job, ref string) string {
	return "<!-- nightly-signal " + markerVersion + " job=" + job + " ref=" + ref + " "
}

// String renders the marker.
func (m Marker) String() string {
	marker := MarkerPrefix(m.Job, m.Ref) +
		"first-run=" + strconv.FormatInt(m.FirstRun, 10) +
		" first-at=" + m.FirstAt
	if m.Prov != "" {
		marker += " prov=" + m.Prov
	}
	return marker + " -->"
}

// markerPattern reads the marker back.
//
// `prov=` is OPTIONAL rather than required, and the version stays v1. A marker
// this version cannot parse is a marker whose issue looks absent, which opens a
// duplicate — so a field is added in a way that keeps every already-written
// marker readable, and the reader falls back when it is missing.
var markerPattern = regexp.MustCompile(
	`<!-- nightly-signal v1 job=(\S+) ref=(\S+) first-run=(\d+) first-at=(\S*)(?: prov=(\S+))? -->`)

// ParseMarker recovers a Marker from an issue body.
//
// A body this tool wrote always carries one. A body that does not parse is
// reported as absent, and the caller treats that as "no marker to preserve"
// rather than failing — a human who mangled the comment should not be able to
// stop the mechanism, only to lose the first-seen record.
func ParseMarker(body string) (Marker, bool) {
	m := markerPattern.FindStringSubmatch(body)
	if m == nil {
		return Marker{}, false
	}
	first, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return Marker{}, false
	}
	return Marker{Job: m[1], Ref: m[2], FirstRun: first, FirstAt: m[4], Prov: m[5]}, true
}

// Signal is everything needed to render one job's issue.
type Signal struct {
	Job           JobSpec
	Ref           string
	DefaultBranch string
	Workflow      string
	Verdict       Verdict
	Provenance    Provenance
	History       History
	Run           RunRef
	Excerpt       Excerpt
	Marker        Marker
}

// Title is the issue title for a newly opened tracker.
//
// The provenance is in the TITLE, not only the body, because an issue list is
// what a reader actually scans. It is fixed at open and never rewritten: the
// classification describes how the job BECAME red, which does not change while
// it stays red.
func (s Signal) Title() string {
	var state string
	switch s.Provenance {
	case ProvenanceNeverGreen:
		state = "is red, and has never been green"
	case ProvenanceRegression:
		state = "went red"
	case ProvenanceResumed:
		state = "is red"
	default:
		state = "is red"
	}
	title := "Nightly " + s.Job.Name + " " + state
	if s.Ref != s.DefaultBranch {
		title += " on " + s.Ref
	}
	return title
}

// Labels are applied to a newly opened tracker.
//
// task:ready because this repository's open issues are its inbound work queue
// and an unlabelled issue is not in it. The priority is the loudness dial the
// provenance earns: a job that has NEVER been green means the check itself has
// been inert for its whole retained history, which is a worse condition than
// one thing breaking last night.
func (s Signal) Labels() []string {
	priority := "priority:high"
	if s.Provenance == ProvenanceNeverGreen {
		priority = "priority:critical"
	}
	return []string{"task:ready", priority}
}

// Body renders the issue body, marker first.
func (s Signal) Body() string {
	var b strings.Builder
	b.WriteString(s.Marker.String())
	b.WriteString("\n\n")

	b.WriteString("The **")
	b.WriteString(s.Job.Name)
	b.WriteString("** job of `")
	b.WriteString(s.Workflow)
	b.WriteString("` is failing on `")
	b.WriteString(s.Ref)
	b.WriteString("`.\n\n")

	b.WriteString(howThisIssueBehaves)
	b.WriteString("\n")

	b.WriteString("| | |\n|---|---|\n")
	row := func(k, v string) {
		b.WriteString("| ")
		b.WriteString(k)
		b.WriteString(" | ")
		b.WriteString(v)
		b.WriteString(" |\n")
	}
	row("first signalled", "run "+strconv.FormatInt(s.Marker.FirstRun, 10)+" · "+orDash(s.Marker.FirstAt))
	row("latest failing run", runLink(s.Run)+" · "+orDash(s.Run.StartedAt)+" · "+orDash(s.Run.Event))
	row("consecutive failures", strconv.Itoa(s.History.Streak+1)+" (including this run)")
	row("last green", s.lastGreenCell())
	row("commit", "`"+shortSHA(s.Run.SHA)+"`")
	b.WriteString("\n")

	b.WriteString(s.provenanceNote())
	b.WriteString("\n")

	b.WriteString("### What the run said\n\n")
	b.WriteString(s.Excerpt.Render())
	b.WriteString("\n")

	return b.String()
}

const howThisIssueBehaves = "" +
	"*This issue is the nightly's signal. It was opened on the run where the job " +
	"went red; every further nightly that finds it still red **rewrites this body " +
	"in place** — no second issue, no nightly comment — and the first nightly that " +
	"finds the job green closes it. So a NEW issue means something broke, and an " +
	"OLD open one means it is still broken. Closing this by hand while the job is " +
	"still red re-arms the signal: the next failing nightly opens a fresh issue.*\n"

func (s Signal) lastGreenCell() string {
	if s.History.LastGreenRun != 0 {
		return "run " + strconv.FormatInt(s.History.LastGreenRun, 10) + " · " + orDash(s.History.LastGreenAt)
	}
	if s.History.Examined == 0 {
		return "unknown — no prior completed run of this workflow on `" + s.Ref + "` is retained"
	}
	return "**never**, in " + strconv.Itoa(s.History.Examined) +
		" retained run(s) of this workflow on `" + s.Ref + "`"
}

func (s Signal) provenanceNote() string {
	switch s.Provenance {
	case ProvenanceNeverGreen:
		return "> **This job has never been green.** Not \"it broke and nobody fixed it\" — " +
			"there is no passing run of it anywhere in the retained history, so the absence " +
			"of a green run was itself the information, and nothing was in a position to " +
			"report it. Treat the age of this condition as unknown and at least as long as " +
			"the retention window.\n"
	case ProvenanceRegression:
		return "> **This is a regression.** The preceding nightly on this ref passed this " +
			"job. The change that broke it is in the range between that run's commit and " +
			"this one's.\n"
	case ProvenanceResumed:
		return "> **This job was already red before this signal existed.** It failed on " +
			"the preceding nightly too, but no tracking issue was open — either this " +
			"mechanism has just been installed, or the previous issue was closed by hand " +
			"while the job was still failing. The consecutive count above is what the " +
			"retained window can see, and is a lower bound.\n"
	default:
		return "> **No prior run of this workflow on this ref is retained**, so this " +
			"report cannot say whether the job just broke or has been red all along. " +
			"That is a statement about the retention window, not about the job.\n"
	}
}

// CloseComment is the one comment this tool ever posts. Good news with an
// endpoint is worth a notification; a nightly repeat of bad news is not.
func (s Signal) CloseComment() string {
	var b strings.Builder
	b.WriteString("**")
	b.WriteString(s.Job.Name)
	b.WriteString(" is green.** ")
	b.WriteString(runLink(s.Run))
	if s.Run.StartedAt != "" {
		b.WriteString(" · ")
		b.WriteString(s.Run.StartedAt)
	}
	b.WriteString(" · commit `")
	b.WriteString(shortSHA(s.Run.SHA))
	b.WriteString("`\n\n")
	b.WriteString("Closing. It had been red since run ")
	b.WriteString(strconv.FormatInt(s.Marker.FirstRun, 10))
	if s.Marker.FirstAt != "" {
		b.WriteString(" (")
		b.WriteString(s.Marker.FirstAt)
		b.WriteString(")")
	}
	b.WriteString(". If it goes red again a new issue is opened rather than this one reopened, ")
	b.WriteString("so the history of each break stays separate.\n")
	return b.String()
}

func runLink(r RunRef) string {
	id := strconv.FormatInt(r.ID, 10)
	if r.URL == "" {
		return "run " + id
	}
	return "[run " + id + "](" + r.URL + ")"
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
