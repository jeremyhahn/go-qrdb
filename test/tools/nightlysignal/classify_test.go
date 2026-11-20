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

func TestVerdictOfMapsEveryActionsResult(t *testing.T) {
	cases := []struct {
		result string
		want   Verdict
	}{
		{resultSuccess, VerdictGreen},
		{resultFailure, VerdictRed},
		{resultCancelled, VerdictInconclusive},
		{resultSkipped, VerdictInconclusive},
	}
	for _, tc := range cases {
		got, err := VerdictOf("fips", tc.result)
		if err != nil {
			t.Fatalf("VerdictOf(%q): %v", tc.result, err)
		}
		if got != tc.want {
			t.Errorf("VerdictOf(%q) = %v, want %v", tc.result, got, tc.want)
		}
	}
}

// An unrecognised result must be refused, not folded into "not red".
//
// The empty string is the case that matters: on Gitea a `needs` context that
// cannot be read yields "" rather than an error (conventions/devops.md §1), and
// treating "" as green would CLOSE a tracking issue on a run that proved
// nothing.
func TestVerdictOfRefusesAnythingElse(t *testing.T) {
	for _, result := range []string{"", "neutral", "SUCCESS", "failed"} {
		_, err := VerdictOf("fips", result)
		var unknown *UnknownResultError
		if !errors.As(err, &unknown) {
			t.Fatalf("VerdictOf(%q) error = %v, want *UnknownResultError", result, err)
		}
		if unknown.Job != "fips" || unknown.Result != result {
			t.Errorf("UnknownResultError = %+v, want job fips result %q", unknown, result)
		}
		if !strings.Contains(unknown.Error(), "success, failure, cancelled, skipped") {
			t.Errorf("error message does not name the accepted values: %s", unknown.Error())
		}
	}
}

// The five-way state table. This IS the mechanism: newly red opens, still red
// updates, green closes, and nothing else writes anything.
func TestDecideDistinguishesNewlyRedFromStillRed(t *testing.T) {
	cases := []struct {
		name      string
		verdict   Verdict
		hasIssue  bool
		want      Action
		rationale string
	}{
		{"newly red", VerdictRed, false, ActionOpen, "the transition is the only notification"},
		{"still red", VerdictRed, true, ActionUpdate, "a second failure must not duplicate the first"},
		{"newly green", VerdictGreen, true, ActionClose, "the break has an endpoint"},
		{"still green", VerdictGreen, false, ActionNone, "silence is correct here"},
		{"inconclusive, nothing open", VerdictInconclusive, false, ActionNone, "proves nothing"},
		{"inconclusive, issue open", VerdictInconclusive, true, ActionNone, "must NOT close on a cancelled run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.verdict, tc.hasIssue); got != tc.want {
				t.Errorf("Decide(%v, %v) = %v, want %v (%s)",
					tc.verdict, tc.hasIssue, got, tc.want, tc.rationale)
			}
		})
	}
}

func TestProvenanceOfClassifiesHistory(t *testing.T) {
	cases := []struct {
		name string
		hist History
		want Provenance
	}{
		{"nothing retained", History{Examined: 0, Window: 0}, ProvenanceUnknown},
		{"nothing retained but runs listed", History{Examined: 0, Window: 4}, ProvenanceUnknown},
		{"never green", History{Examined: 22, Streak: 22}, ProvenanceNeverGreen},
		{"regression", History{Examined: 1, Streak: 0, LastGreenRun: 17271}, ProvenanceRegression},
		{"already red, no issue", History{Examined: 5, Streak: 4, LastGreenRun: 900}, ProvenanceResumed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProvenanceOf(tc.hist); got != tc.want {
				t.Errorf("ProvenanceOf(%+v) = %v, want %v", tc.hist, got, tc.want)
			}
		})
	}
}

func TestVerdictProvenanceAndActionAllStringify(t *testing.T) {
	verdicts := map[Verdict]string{VerdictRed: "red", VerdictGreen: "green", VerdictInconclusive: "inconclusive"}
	for v, want := range verdicts {
		if v.String() != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", v, v.String(), want)
		}
	}
	provenances := map[Provenance]string{
		ProvenanceUnknown:    "unknown-history",
		ProvenanceRegression: "regression",
		ProvenanceResumed:    "resumed",
		ProvenanceNeverGreen: "never-green",
	}
	for p, want := range provenances {
		if p.String() != want {
			t.Errorf("Provenance(%d).String() = %q, want %q", p, p.String(), want)
		}
	}
	actions := map[Action]string{
		ActionNone: "none", ActionOpen: "open", ActionUpdate: "update", ActionClose: "close",
	}
	for a, want := range actions {
		if a.String() != want {
			t.Errorf("Action(%d).String() = %q, want %q", a, a.String(), want)
		}
	}
}

func TestMarkerRoundTrips(t *testing.T) {
	// A ref with a slash, because every lane branch has one and a marker that
	// could not carry it would key every branch's tracker to the same issue.
	m := Marker{Job: "trivy", Ref: "issue/478-nightly-signal", FirstRun: 17271, FirstAt: "2026-09-12T02:33:04Z"}
	body := "some prose\n" + m.String() + "\nmore prose"

	got, ok := ParseMarker(body)
	if !ok {
		t.Fatalf("ParseMarker did not find the marker in %q", body)
	}
	if got != m {
		t.Errorf("ParseMarker = %+v, want %+v", got, m)
	}
	if !strings.Contains(body, MarkerPrefix(m.Job, m.Ref)) {
		t.Error("the rendered marker does not carry the prefix findTracker searches for")
	}
}

// A body with no marker, or a mangled one, reports absent rather than failing:
// a human who edited the comment should lose the first-seen record, not the
// mechanism.
func TestParseMarkerReportsAbsentRatherThanFailing(t *testing.T) {
	for _, body := range []string{
		"",
		"no marker here",
		"<!-- nightly-signal v0 job=trivy ref=main first-run=1 first-at=x -->",
		"<!-- nightly-signal v1 job=trivy ref=main first-run=notanumber first-at=x -->",
		"<!-- nightly-signal v1 job=trivy ref=main -->",
	} {
		if _, ok := ParseMarker(body); ok {
			t.Errorf("ParseMarker(%q) = ok, want absent", body)
		}
	}
}

// Two jobs on the same ref, and one job on two refs, must never collide.
func TestMarkerPrefixSeparatesJobsAndRefs(t *testing.T) {
	seen := map[string]bool{}
	for _, job := range []string{"fips", "trivy"} {
		for _, ref := range []string{"main", "issue/478-nightly-signal"} {
			p := MarkerPrefix(job, ref)
			if seen[p] {
				t.Fatalf("marker prefix collision at job=%s ref=%s", job, ref)
			}
			seen[p] = true
		}
	}
	// And a prefix must not be a prefix of another key's prefix, or a substring
	// match would find the wrong tracker.
	if strings.Contains(MarkerPrefix("fips", "main"), MarkerPrefix("fips", "main-2")) {
		t.Error("one ref's prefix contains another's")
	}
}

func testSignal(p Provenance, h History) Signal {
	return Signal{
		Job:           JobSpec{Key: "trivy", Name: "Trivy", Result: resultFailure},
		Ref:           "main",
		DefaultBranch: "main",
		Workflow:      "nightly.yml",
		Verdict:       VerdictRed,
		Provenance:    p,
		History:       h,
		Run: RunRef{
			ID: 17300, URL: "https://forge.example/r/actions/runs/17300",
			StartedAt: "2026-09-13T03:00:00Z", SHA: "133fa5699947792772bac23fbfb6fb051d600775",
			Event: "schedule",
		},
		Marker: Marker{Job: "trivy", Ref: "main", FirstRun: 17271, FirstAt: "2026-09-12T02:33:04Z"},
	}
}

func TestTitleCarriesProvenanceAndRef(t *testing.T) {
	cases := []struct {
		provenance Provenance
		ref        string
		want       string
	}{
		{ProvenanceNeverGreen, "main", "Nightly Trivy is red, and has never been green"},
		{ProvenanceRegression, "main", "Nightly Trivy went red"},
		{ProvenanceResumed, "main", "Nightly Trivy is red"},
		{ProvenanceUnknown, "main", "Nightly Trivy is red"},
		{ProvenanceRegression, "issue/478-nightly-signal", "Nightly Trivy went red on issue/478-nightly-signal"},
	}
	for _, tc := range cases {
		s := testSignal(tc.provenance, History{})
		s.Ref = tc.ref
		if got := s.Title(); got != tc.want {
			t.Errorf("Title() for %v on %s = %q, want %q", tc.provenance, tc.ref, got, tc.want)
		}
	}
}

// Never-green is the LOUDER condition, per the ruling on #478: a job that has
// never passed means nothing was ever in a position to notice.
func TestLabelsAreLouderForNeverGreen(t *testing.T) {
	never := testSignal(ProvenanceNeverGreen, History{}).Labels()
	broke := testSignal(ProvenanceRegression, History{}).Labels()

	if !contains(never, "priority:critical") {
		t.Errorf("never-green labels = %v, want priority:critical", never)
	}
	if !contains(broke, "priority:high") || contains(broke, "priority:critical") {
		t.Errorf("regression labels = %v, want priority:high and not critical", broke)
	}
	for _, labels := range [][]string{never, broke} {
		if !contains(labels, "task:ready") {
			t.Errorf("labels %v do not put the issue in the inbound work queue", labels)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestBodyCarriesMarkerCountsAndBehaviour(t *testing.T) {
	s := testSignal(ProvenanceNeverGreen, History{Examined: 22, Streak: 22, Window: 22})
	s.Excerpt = Excerpt{Lines: []string{"Total: 1 (HIGH: 1, CRITICAL: 0)"}, Matched: 1, Scanned: 400}
	body := s.Body()

	// The marker must be first, so ParseMarker on a body this tool wrote always
	// finds it and findTracker on the next run always matches.
	if !strings.HasPrefix(body, MarkerPrefix("trivy", "main")) {
		t.Fatalf("body does not open with the marker:\n%s", body)
	}
	if _, ok := ParseMarker(body); !ok {
		t.Fatal("a body this tool rendered does not parse back")
	}

	mustContain(t, body, "23 (including this run)") // streak 22 + this one.
	mustContain(t, body, "**never**, in 22 retained run(s)")
	mustContain(t, body, "has never been green")
	mustContain(t, body, "run 17271")  // first signalled.
	mustContain(t, body, "run 17300")  // latest failing.
	mustContain(t, body, "`133fa569`") // short sha.
	mustContain(t, body, "Total: 1 (HIGH: 1, CRITICAL: 0)")
	// The body has to explain its own behaviour, or a reader cannot tell a
	// quiet still-open issue from a forgotten one.
	mustContain(t, body, "rewrites this body")
	mustContain(t, body, "closes it")
}

func TestBodyDistinguishesUnknownHistoryFromNeverGreen(t *testing.T) {
	never := testSignal(ProvenanceNeverGreen, History{Examined: 22, Streak: 22}).Body()
	unknown := testSignal(ProvenanceUnknown, History{}).Body()

	mustContain(t, never, "**never**")
	mustContain(t, unknown, "unknown — no prior completed run")
	if strings.Contains(unknown, "**never**") {
		t.Error("an empty history window must not be reported as never green")
	}
}

func TestBodyNamesTheLastGreenRunWhenThereIsOne(t *testing.T) {
	s := testSignal(ProvenanceRegression, History{Examined: 1, LastGreenRun: 17271, LastGreenAt: "2026-09-12T02:33:04Z"})
	body := s.Body()
	mustContain(t, body, "run 17271 · 2026-09-12T02:33:04Z")
	mustContain(t, body, "This is a regression")
	mustContain(t, body, "1 (including this run)")
}

func TestBodyHandlesMissingTimestampsAndShortSHAs(t *testing.T) {
	s := testSignal(ProvenanceResumed, History{Examined: 3, Streak: 2, LastGreenRun: 900})
	s.Run.StartedAt = ""
	s.Run.Event = ""
	s.Run.SHA = ""
	s.Run.URL = ""
	s.Marker.FirstAt = ""
	body := s.Body()
	mustContain(t, body, "—")
	mustContain(t, body, "`unknown`")
	mustContain(t, body, "run 17300") // no URL: a plain reference, not a broken link.
	mustContain(t, body, "already red before this signal existed")
}

func TestCloseCommentNamesTheGreenRunAndTheBreak(t *testing.T) {
	s := testSignal(ProvenanceNeverGreen, History{})
	s.Verdict = VerdictGreen
	comment := s.CloseComment()

	mustContain(t, comment, "Trivy is green")
	mustContain(t, comment, "[run 17300]")
	mustContain(t, comment, "red since run 17271")
	mustContain(t, comment, "2026-09-12T02:33:04Z")
	// A future break must open a NEW issue, so the history of each break stays
	// separate; the comment says so rather than leaving it to be discovered.
	mustContain(t, comment, "new issue is opened rather than this one reopened")
}

func TestShortSHA(t *testing.T) {
	// The full-length and empty cases are asserted through Body() by
	// TestBodyCarriesMarkerCountsAndBehaviour and
	// TestBodyHandlesMissingTimestampsAndShortSHAs.
	cases := map[string]string{
		"abc": "abc",
	}
	for in, want := range cases {
		if got := shortSHA(in); got != want {
			t.Errorf("shortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("missing %q in:\n%s", needle, haystack)
	}
}

// The marker carries the OPENING classification, because provenance answers
// "how did this break" and that answer must not drift once a break is under
// way.
func TestMarkerCarriesTheProvenanceAndStaysReadableWithout(t *testing.T) {
	m := Marker{Job: "trivy", Ref: "main", FirstRun: 17310, FirstAt: "2026-09-12T03:03:32Z", Prov: "regression"}
	got, ok := ParseMarker(m.String())
	if !ok || got != m {
		t.Fatalf("ParseMarker = %+v, %v, want %+v", got, ok, m)
	}

	// A marker written before the field existed must still parse, or its issue
	// looks absent and a duplicate is opened.
	legacy := "<!-- nightly-signal v1 job=trivy ref=main first-run=17310 first-at=2026-09-12T03:03:32Z -->"
	old, ok := ParseMarker(legacy)
	if !ok {
		t.Fatal("a marker without prov= no longer parses; every already-open tracker would look absent")
	}
	if old.Prov != "" {
		t.Errorf("Prov = %q, want empty so the caller knows to fall back", old.Prov)
	}
	if old.FirstRun != 17310 {
		t.Errorf("FirstRun = %d", old.FirstRun)
	}
}

func TestProvenanceNamedIsTheInverseOfString(t *testing.T) {
	for _, p := range []Provenance{
		ProvenanceUnknown, ProvenanceRegression, ProvenanceResumed, ProvenanceNeverGreen,
	} {
		got, ok := ProvenanceNamed(p.String())
		if !ok || got != p {
			t.Errorf("ProvenanceNamed(%q) = %v, %v, want %v", p.String(), got, ok, p)
		}
	}
	// An unrecognised name is refused rather than flattened to unknown-history:
	// it means a version classified differently, and saying "unknown" would
	// replace a statement someone can act on with one they cannot.
	if _, ok := ProvenanceNamed("something-else"); ok {
		t.Error("an unknown provenance name was accepted")
	}
	// A Provenance outside the declared set still renders something.
	if Provenance(99).String() != "unknown-history" {
		t.Errorf("Provenance(99).String() = %q", Provenance(99).String())
	}
}
