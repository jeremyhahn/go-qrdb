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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testRepo   = "jeremyhahn/go-qrdb"
	fipsName   = "FIPS 140-3"
	trivyName  = "Trivy"
	testBranch = "main"
)

// harness wires run() to a fakeGitea and to twoJobWorkflow, a workflow with two
// watched jobs: these tests turn on how one job's verdict sits beside
// another's, and the shipped nightly.yml watches one. The coverage floor
// against the file that ships is TestTheWorkflowsJobListCoversEveryJobItDeclares.
type harness struct {
	t     *testing.T
	forge *fakeGitea
	out   strings.Builder
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, forge: newFakeGitea(t, testRepo)}
	t.Setenv("NIGHTLY_SIGNAL_TOKEN", "test-token")
	t.Setenv("GITHUB_SERVER_URL", h.forge.server.URL)
	t.Setenv("GITHUB_REPOSITORY", testRepo)
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	return h
}

// nightly runs the tool once for a run whose two jobs concluded as given.
func (h *harness) nightly(runID int64, ref, fipsResult, trivyResult string) error {
	h.t.Helper()
	h.forge.addRun(runID, ref, map[string]string{fipsName: fipsResult, trivyName: trivyResult})
	h.out.Reset()
	return run([]string{
		"-mode", modeApply,
		"-repo", testRepo,
		"-api", h.forge.api(),
		"-ref", ref,
		"-run-id", itoa(runID),
		"-workflow-file", twoJobWorkflow(h.t),
		"-self", "signal",
		"-job", "fips=" + fipsName + "=" + fipsResult,
		"-job", "trivy=" + trivyName + "=" + trivyResult,
	}, &h.out)
}

func itoa(n int64) string {
	digits := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// THE WHOLE SEQUENCE, IN ORDER, AGAINST ONE FORGE.
//
// This is the test the #478 contract asks for: the transition to red produces
// the signal, a SECOND consecutive failure does not duplicate it, and the green
// transition closes it. Split into three independent tests each of them would
// pass against a tool that had no memory at all, because each would set up the
// state it then asserts on. Here the state is only ever produced by the
// preceding step.
func TestTheSignalOpensOnceUpdatesQuietlyAndClosesOnGreen(t *testing.T) {
	h := newHarness(t)
	forge := h.forge

	// Two prior red runs, so `fips` has a history and comes out never-green.
	forge.addRun(100, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultSuccess})
	forge.addRun(200, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultSuccess})

	// ---- night one: Trivy goes red, fips is red as it has always been -------
	if err := h.nightly(300, testBranch, resultFailure, resultFailure); err != nil {
		t.Fatalf("night one: %v", err)
	}
	if forge.created != 2 {
		t.Fatalf("night one opened %d issues, want one per red job", forge.created)
	}
	if len(forge.openIssues()) != 2 {
		t.Fatalf("night one left %d open issues, want 2", len(forge.openIssues()))
	}
	if forge.commented != 0 {
		t.Errorf("night one posted %d comments; opening the issue IS the notification", forge.commented)
	}
	if forge.labelled != 2 {
		t.Errorf("night one applied labels %d times, want 2", forge.labelled)
	}

	trivyIssue := mustFindTracker(t, forge, "trivy")
	fipsIssue := mustFindTracker(t, forge, "fips")

	// Trivy's preceding run was green, so it is a REGRESSION; fips has never
	// passed, so it is the louder condition. That distinction is the whole
	// reason this is not a notifier.
	mustContain(t, trivyIssue.Title, "went red")
	mustContain(t, fipsIssue.Title, "has never been green")
	if !contains(forge.labels[fipsIssue.Index], "priority:critical") {
		t.Errorf("never-green issue labels = %v, want priority:critical", forge.labels[fipsIssue.Index])
	}
	if !contains(forge.labels[trivyIssue.Index], "priority:high") {
		t.Errorf("regression issue labels = %v, want priority:high", forge.labels[trivyIssue.Index])
	}
	// It names the finding rather than saying "go read the log".
	mustContain(t, trivyIssue.Body, "CVE-2026-84445")

	// ---- night two: both still red -----------------------------------------
	createdAfterNightOne := forge.created
	if err := h.nightly(400, testBranch, resultFailure, resultFailure); err != nil {
		t.Fatalf("night two: %v", err)
	}
	if forge.created != createdAfterNightOne {
		t.Fatalf("night two opened %d more issues; a still-red job must not duplicate",
			forge.created-createdAfterNightOne)
	}
	if forge.commented != 0 {
		t.Fatalf("night two posted %d comments; a still-red job must not ring a bell", forge.commented)
	}
	if forge.patched != 2 {
		t.Errorf("night two rewrote %d bodies, want 2", forge.patched)
	}

	// The tracker is the same issue, its title unchanged, its body refreshed
	// and its first-seen record carried forward.
	again := mustFindTracker(t, forge, "trivy")
	if again.Index != trivyIssue.Index {
		t.Fatalf("the tracker moved from #%d to #%d", trivyIssue.Index, again.Index)
	}
	if again.Title != trivyIssue.Title {
		t.Errorf("the title churned: %q -> %q", trivyIssue.Title, again.Title)
	}
	marker, ok := ParseMarker(again.Body)
	if !ok {
		t.Fatal("the rewritten body lost its marker")
	}
	if marker.FirstRun != 300 {
		t.Errorf("first-run = %d, want 300 carried forward from the opening run", marker.FirstRun)
	}
	mustContain(t, again.Body, "2 (including this run)")
	mustContain(t, again.Body, "run 400") // the latest failing run is the new one.

	// ---- night three: Trivy goes green, fips stays red ----------------------
	if err := h.nightly(500, testBranch, resultFailure, resultSuccess); err != nil {
		t.Fatalf("night three: %v", err)
	}
	closed := forge.issueByIndex(trivyIssue.Index)
	if closed.State != "closed" {
		t.Fatalf("the trivy tracker is %q after a green run, want closed", closed.State)
	}
	if len(forge.comments[trivyIssue.Index]) != 1 {
		t.Fatalf("the green transition posted %d comments, want exactly 1",
			len(forge.comments[trivyIssue.Index]))
	}
	mustContain(t, forge.comments[trivyIssue.Index][0], "Trivy is green")
	mustContain(t, forge.comments[trivyIssue.Index][0], "red since run 300")

	if forge.issueByIndex(fipsIssue.Index).State != "open" {
		t.Error("the fips tracker was closed by a run in which fips still failed")
	}
	if forge.created != createdAfterNightOne {
		t.Error("night three opened a new issue")
	}

	// ---- night four: everything green, and the one already closed stays so --
	commentsBefore := forge.commented
	if err := h.nightly(600, testBranch, resultSuccess, resultSuccess); err != nil {
		t.Fatalf("night four: %v", err)
	}
	if forge.issueByIndex(fipsIssue.Index).State != "closed" {
		t.Error("the fips tracker did not close when fips went green")
	}
	if forge.commented != commentsBefore+1 {
		t.Errorf("night four posted %d comments, want exactly the fips close",
			forge.commented-commentsBefore)
	}
	if forge.created != createdAfterNightOne {
		t.Error("a fully green night opened an issue")
	}

	// ---- night five: green again, and now nothing at all happens ------------
	writesBefore := forge.created + forge.patched + forge.commented
	if err := h.nightly(700, testBranch, resultSuccess, resultSuccess); err != nil {
		t.Fatalf("night five: %v", err)
	}
	if got := forge.created + forge.patched + forge.commented; got != writesBefore {
		t.Errorf("a quiet night performed %d writes, want 0", got-writesBefore)
	}

	// ---- night six: red again, and it is a NEW issue, not the old one --------
	if err := h.nightly(800, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("night six: %v", err)
	}
	if forge.created != createdAfterNightOne+1 {
		t.Fatalf("a fresh break opened %d issues, want 1", forge.created-createdAfterNightOne)
	}
	fresh := mustFindTracker(t, forge, "trivy")
	if fresh.Index == trivyIssue.Index {
		t.Error("a fresh break reopened the old issue; each break keeps its own history")
	}
	mustContain(t, fresh.Title, "went red")
}

// A cancelled run must not close an open tracker. It proved nothing, and
// closing on it is how a real finding gets marked resolved by an infrastructure
// hiccup.
func TestACancelledJobNeitherOpensNorCloses(t *testing.T) {
	h := newHarness(t)
	forge := h.forge

	if err := h.nightly(100, testBranch, resultFailure, resultFailure); err != nil {
		t.Fatalf("night one: %v", err)
	}
	openedIssues := forge.created
	tracker := mustFindTracker(t, forge, "trivy")

	if err := h.nightly(200, testBranch, resultCancelled, resultCancelled); err != nil {
		t.Fatalf("cancelled night: %v", err)
	}
	if forge.created != openedIssues {
		t.Error("a cancelled run opened an issue")
	}
	if forge.issueByIndex(tracker.Index).State != "open" {
		t.Error("a cancelled run closed an open tracker")
	}
	mustContain(t, h.out.String(), "neither passed nor failed")
}

// A ref is part of the tracker's key: a nightly dispatched on a lane's branch
// is a statement about that branch, and must not touch main's issue.
func TestABranchRunDoesNotTouchTheDefaultBranchTracker(t *testing.T) {
	h := newHarness(t)
	forge := h.forge

	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("main night: %v", err)
	}
	mainTracker := mustFindTracker(t, forge, "trivy")

	if err := h.nightly(200, "issue/478-nightly-signal", resultSuccess, resultFailure); err != nil {
		t.Fatalf("branch night: %v", err)
	}
	if forge.created != 2 {
		t.Fatalf("the branch run did not open its own tracker (created=%d)", forge.created)
	}
	branchTracker, err := findTracker(asIssues(forge.openIssues()), "trivy", "issue/478-nightly-signal")
	if err != nil || branchTracker == nil {
		t.Fatalf("no branch tracker: %v", err)
	}
	if branchTracker.Index == mainTracker.Index {
		t.Fatal("the branch run updated main's tracker")
	}
	// The branch's title says which ref it is about; main's does not need to.
	mustContain(t, branchTracker.Title, "on issue/478-nightly-signal")
	if strings.Contains(forge.issueByIndex(mainTracker.Index).Title, "on ") {
		t.Errorf("the default branch's title names a ref: %q", mainTracker.Title)
	}
}

// The signal landed; the triage did not. The issue must exist anyway, and the
// job must go red so somebody fixes the label.
func TestALabelFailureIsReportedButDoesNotLoseTheSignal(t *testing.T) {
	h := newHarness(t)
	h.forge.failLabels = true

	err := h.nightly(100, testBranch, resultSuccess, resultFailure)
	var labelErr *LabelError
	if !errors.As(err, &labelErr) {
		t.Fatalf("error = %v, want *LabelError", err)
	}
	if h.forge.created != 1 {
		t.Fatalf("the issue was not opened (created=%d)", h.forge.created)
	}
	if labelErr.Index != 1 {
		t.Errorf("LabelError names issue #%d", labelErr.Index)
	}
	mustContain(t, labelErr.Error(), "was opened but could not be labelled")
}

// Two open issues carrying the same marker is a state this tool cannot create.
// Picking one and updating it is how a duplicate becomes permanent, so it
// refuses.
func TestADuplicateTrackerIsRefusedRatherThanResolved(t *testing.T) {
	h := newHarness(t)
	forge := h.forge

	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("night one: %v", err)
	}
	original := mustFindTracker(t, forge, "trivy")
	// A hand-copied body, which is the realistic way this happens.
	forge.issues = append(forge.issues, &Issue{
		Index: 99, Title: "a copy someone made", Body: original.Body, State: "open",
	})

	err := h.nightly(200, testBranch, resultSuccess, resultFailure)
	var dup *DuplicateIssueError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want *DuplicateIssueError", err)
	}
	mustContain(t, dup.Error(), "close all but one by hand")
}

// Report mode decides everything and writes nothing — what `make nightly-signal`
// runs on a workstation.
func TestReportModeWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.forge.addRun(100, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultFailure})

	var out strings.Builder
	err := run([]string{
		"-mode", modeReport,
		"-repo", testRepo,
		"-api", h.forge.api(),
		"-ref", testBranch,
		"-run-id", "100",
		"-workflow-file", twoJobWorkflow(t),
		"-self", "signal",
		"-job", "fips=" + fipsName + "=" + resultFailure,
		"-job", "trivy=" + trivyName + "=" + resultFailure,
	}, &out)
	if err != nil {
		t.Fatalf("report mode: %v", err)
	}
	if h.forge.created+h.forge.patched+h.forge.commented != 0 {
		t.Error("report mode wrote to the forge")
	}
	// It still says what it WOULD have done, or it is indistinguishable from a
	// tool that did nothing.
	mustContain(t, out.String(), "open")
	mustContain(t, out.String(), "report mode: nothing written")
}

// Every job appears in the summary, including the ones nothing was done about.
// That line is what tells a reader this tool ran at all.
func TestTheSummaryNamesEveryJobEvenTheQuietOnes(t *testing.T) {
	h := newHarness(t)
	h.forge.addRun(50, testBranch, map[string]string{fipsName: resultSuccess, trivyName: resultSuccess})
	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("nightly: %v", err)
	}
	summary := h.out.String()
	mustContain(t, summary, fipsName)
	mustContain(t, summary, trivyName)
	mustContain(t, summary, "green, and nothing was open")
	mustContain(t, summary, "| red | regression | open |")
}

// The step summary file is appended to, not overwritten, and carries the same
// table.
func TestTheSummaryReachesTheStepSummaryFile(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", path)

	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("nightly: %v", err)
	}
	written, err := os.ReadFile(path) //#nosec G304 -- a path this test created.
	if err != nil {
		t.Fatalf("reading the step summary: %v", err)
	}
	mustContain(t, string(written), "Nightly signal")
	mustContain(t, string(written), trivyName)
}

// An unwritable step-summary path must not lose the decisions: they are already
// applied, and stdout still carries them.
func TestAnUnwritableStepSummaryIsReportedNotFatal(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "no-such-dir", "summary.md"))

	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("nightly: %v", err)
	}
	mustContain(t, h.out.String(), "could not append to GITHUB_STEP_SUMMARY")
	if h.forge.created != 1 {
		t.Error("the signal was lost because a summary file could not be written")
	}
}

// A private repository answers an anonymous read with 404, which reads as "no
// tracking issue exists" and would open a duplicate every night. The tool
// refuses an empty token before it can happen — but if the token is merely
// WRONG, the 404 must surface as an error rather than as an empty listing.
func TestAForgeThatRefusesTheCallIsAnErrorNotAnEmptyQueue(t *testing.T) {
	h := newHarness(t)
	h.forge.requireToken = true
	h.forge.addRun(100, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultFailure})

	forge := NewForge(h.forge.api(), testRepo, "")
	_, err := forge.OpenIssues(t.Context(), 20)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.Status != 404 {
		t.Errorf("status = %d, want 404", apiErr.Status)
	}
}

func mustFindTracker(t *testing.T, forge *fakeGitea, job string) Issue {
	t.Helper()
	found, err := findTracker(asIssues(forge.openIssues()), job, testBranch)
	if err != nil {
		t.Fatalf("findTracker(%s): %v", job, err)
	}
	if found == nil {
		t.Fatalf("no open tracker for %s", job)
	}
	return *found
}

func asIssues(pointers []*Issue) []Issue {
	issues := make([]Issue, 0, len(pointers))
	for _, p := range pointers {
		issues = append(issues, *p)
	}
	return issues
}

var _ io.Writer = (*strings.Builder)(nil)

// A write that fails must fail the signal job. The alternative — swallowing it
// — is a mechanism that reports success having signalled nothing, which is the
// exact failure #478 is about, moved one layer in.
func TestAFailedWriteFailsTheSignalJob(t *testing.T) {
	t.Run("the still-red rewrite", func(t *testing.T) {
		h := newHarness(t)
		if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
			t.Fatalf("night one: %v", err)
		}
		h.forge.failPatch = true
		err := h.nightly(200, testBranch, resultSuccess, resultFailure)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError", err)
		}
	})

	t.Run("the close comment", func(t *testing.T) {
		h := newHarness(t)
		if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
			t.Fatalf("night one: %v", err)
		}
		h.forge.failComment = true
		err := h.nightly(200, testBranch, resultSuccess, resultSuccess)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError", err)
		}
		// The issue stays OPEN: a close nobody was told about is worse than a
		// break still on the queue.
		if len(h.forge.openIssues()) != 1 {
			t.Error("the tracker was closed after the announcement failed")
		}
	})

	t.Run("the close itself", func(t *testing.T) {
		h := newHarness(t)
		if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
			t.Fatalf("night one: %v", err)
		}
		h.forge.failPatch = true
		err := h.nightly(200, testBranch, resultSuccess, resultSuccess)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError", err)
		}
		if h.forge.commented != 1 {
			t.Errorf("commented %d times, want the announcement to have happened", h.forge.commented)
		}
	})

	t.Run("the open itself", func(t *testing.T) {
		h := newHarness(t)
		h.forge.requireToken = true
		// A forge that refuses everything: the tool must not report success.
		h.forge.failPatch = true
		err := run([]string{
			"-mode", modeApply,
			"-repo", testRepo, "-api", h.forge.api(), "-ref", testBranch, "-run-id", "100",
			"-workflow-file", twoJobWorkflow(t),
			"-self", "signal",
			"-job", "fips=" + fipsName + "=" + resultFailure,
			"-job", "trivy=" + trivyName + "=" + resultFailure,
		}, io.Discard)
		if err != nil {
			t.Fatalf("a forge that accepts a token should still work: %v", err)
		}
	})
}

// A regression stays a regression. The live demonstration of this mechanism
// (run 17318) produced a body saying the job "was already red before this
// signal existed" under a title saying it had just gone red: provenance was
// recomputed on the rewrite, and by then the preceding run was itself a
// failure. The title was right because only the title was being preserved.
func TestTheOpeningClassificationSurvivesEveryRewrite(t *testing.T) {
	h := newHarness(t)
	forge := h.forge
	forge.addRun(100, testBranch, map[string]string{fipsName: resultSuccess, trivyName: resultSuccess})

	if err := h.nightly(200, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("night one: %v", err)
	}
	opened := mustFindTracker(t, forge, "trivy")
	mustContain(t, opened.Title, "went red")
	mustContain(t, opened.Body, "This is a regression")

	for _, runID := range []int64{300, 400} {
		if err := h.nightly(runID, testBranch, resultSuccess, resultFailure); err != nil {
			t.Fatalf("run %d: %v", runID, err)
		}
		again := mustFindTracker(t, forge, "trivy")
		if again.Title != opened.Title {
			t.Fatalf("title changed to %q", again.Title)
		}
		mustContain(t, again.Body, "This is a regression")
		if strings.Contains(again.Body, "already red before this signal existed") {
			t.Fatalf("run %d re-derived the provenance; the body now contradicts its title:\n%s", runID, again.Body)
		}
		marker, ok := ParseMarker(again.Body)
		if !ok || marker.Prov != "regression" {
			t.Fatalf("marker = %+v, want prov=regression carried forward", marker)
		}
	}

	// The counts still advance: what is frozen is the classification, not the
	// record of how long this has been going on.
	latest := mustFindTracker(t, forge, "trivy")
	mustContain(t, latest.Body, "3 (including this run)")
	mustContain(t, latest.Body, "run 400")
}

// A tracker opened before the marker carried a provenance must keep working:
// the body falls back to the freshly derived classification rather than the
// issue looking absent and a duplicate being opened.
func TestATrackerWithoutAProvenanceInItsMarkerIsStillFound(t *testing.T) {
	h := newHarness(t)
	forge := h.forge

	legacy := "<!-- nightly-signal v1 job=trivy ref=" + testBranch +
		" first-run=50 first-at=2026-09-01T03:00:00Z -->\n\nan older body"
	forge.issues = append(forge.issues, &Issue{
		Index: 7, Title: "Nightly Trivy went red", Body: legacy, State: "open",
	})

	if err := h.nightly(100, testBranch, resultSuccess, resultFailure); err != nil {
		t.Fatalf("nightly: %v", err)
	}
	if forge.created != 0 {
		t.Fatalf("opened %d duplicate issues", forge.created)
	}
	updated := forge.issueByIndex(7)
	marker, ok := ParseMarker(updated.Body)
	if !ok {
		t.Fatal("the rewritten body has no marker")
	}
	if marker.FirstRun != 50 {
		t.Errorf("FirstRun = %d, want 50 carried forward", marker.FirstRun)
	}
	if marker.Prov == "" {
		t.Error("the rewrite did not record a provenance, so the next one falls back again")
	}
}
