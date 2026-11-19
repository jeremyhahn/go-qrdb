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
	"context"
	"errors"
	"testing"
)

// fakeRuns is a table of runs and their per-job conclusions, newest first —
// the shape the runs API answers in.
type fakeRuns struct {
	runs     []Run
	jobs     map[int64][]Job
	runsErr  error
	jobsErr  error
	jobCalls int
}

func (f *fakeRuns) WorkflowRuns(_ context.Context, _ string, _ int) ([]Run, error) {
	return f.runs, f.runsErr
}

func (f *fakeRuns) RunJobs(_ context.Context, runID int64) ([]Job, error) {
	f.jobCalls++
	if f.jobsErr != nil {
		return nil, f.jobsErr
	}
	return f.jobs[runID], nil
}

// fakeRun builds one completed run on a ref with one job of the given conclusion.
func fakeRun(id int64, ref, conclusion string) (Run, []Job) {
	r := Run{ID: id, Status: "completed", HeadBranch: ref, StartedAt: "2026-09-01T03:00:00Z"}
	return r, []Job{{ID: id * 10, Name: "Trivy", Conclusion: conclusion}}
}

func newFake(entries ...struct {
	id         int64
	ref        string
	conclusion string
}) *fakeRuns {
	f := &fakeRuns{jobs: map[int64][]Job{}}
	for _, e := range entries {
		r, jobs := fakeRun(e.id, e.ref, e.conclusion)
		f.runs = append(f.runs, r)
		f.jobs[e.id] = jobs
	}
	return f
}

type entry = struct {
	id         int64
	ref        string
	conclusion string
}

// The condition this repository was actually in on 2026-09-12: every retained
// run red, and the absence of a green one was itself the finding.
func TestComputeHistoryReportsNeverGreen(t *testing.T) {
	f := newFake(
		entry{17044, "main", resultFailure},
		entry{16597, "main", resultFailure},
		entry{15475, "main", resultFailure},
	)
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 17300, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.LastGreenRun != 0 {
		t.Errorf("LastGreenRun = %d, want 0", h.LastGreenRun)
	}
	if h.Streak != 3 || h.Examined != 3 || h.Window != 3 {
		t.Errorf("history = %+v, want streak 3, examined 3, window 3", h)
	}
	if ProvenanceOf(h) != ProvenanceNeverGreen {
		t.Errorf("provenance = %v, want never-green", ProvenanceOf(h))
	}
}

// Newest first, stopping at the first green: nothing older can change an
// answer, and every extra run examined is an API call.
func TestComputeHistoryStopsAtTheLastGreenRun(t *testing.T) {
	f := newFake(
		entry{500, "main", resultFailure},
		entry{400, "main", resultSuccess},
		entry{300, "main", resultFailure},
		entry{200, "main", resultFailure},
	)
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.LastGreenRun != 400 || h.Streak != 1 {
		t.Errorf("history = %+v, want last green 400 and streak 1", h)
	}
	if f.jobCalls != 2 {
		t.Errorf("examined %d runs, want 2 — the walk did not stop at the green one", f.jobCalls)
	}
	if ProvenanceOf(h) != ProvenanceResumed {
		t.Errorf("provenance = %v, want resumed", ProvenanceOf(h))
	}
}

func TestComputeHistoryReportsARegression(t *testing.T) {
	f := newFake(entry{400, "main", resultSuccess}, entry{300, "main", resultSuccess})
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 500, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Streak != 0 || h.LastGreenRun != 400 {
		t.Errorf("history = %+v, want streak 0 and last green 400", h)
	}
	if ProvenanceOf(h) != ProvenanceRegression {
		t.Errorf("provenance = %v, want regression", ProvenanceOf(h))
	}
}

// A cancelled run proved nothing. Letting it break the streak would report a
// three-week-old breakage as a fresh regression, which is the single most
// misleading thing this tool could say.
func TestComputeHistorySkipsInconclusiveRuns(t *testing.T) {
	f := newFake(
		entry{500, "main", resultCancelled},
		entry{400, "main", resultSkipped},
		entry{300, "main", resultFailure},
		entry{200, "main", resultFailure},
	)
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Streak != 2 || h.Examined != 2 {
		t.Errorf("history = %+v, want streak 2 and examined 2", h)
	}
	if h.Window != 4 {
		t.Errorf("Window = %d, want 4: the window counts runs listed, not runs that said something", h.Window)
	}
}

// Only the same ref counts. A lane dispatching the nightly on its branch must
// not make main's history look broken, or main's look fixed.
func TestComputeHistoryIgnoresOtherRefs(t *testing.T) {
	f := newFake(
		entry{500, "issue/478-nightly-signal", resultSuccess},
		entry{400, "main", resultFailure},
	)
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.LastGreenRun != 0 || h.Streak != 1 {
		t.Errorf("history = %+v: a branch run leaked into main's history", h)
	}
}

// Strictly PRIOR. A scheduled run that starts while a dispatch is still going
// gets a higher id, and reading it as history would report the future.
func TestComputeHistoryIgnoresTheCurrentAndLaterRuns(t *testing.T) {
	f := newFake(
		entry{700, "main", resultSuccess},
		entry{600, "main", resultSuccess},
		entry{500, "main", resultFailure},
	)
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Window != 1 || h.Streak != 1 {
		t.Errorf("history = %+v, want only run 500 considered", h)
	}
}

// A run that is still going is not history either.
func TestComputeHistoryIgnoresUnfinishedRuns(t *testing.T) {
	f := &fakeRuns{jobs: map[int64][]Job{}}
	f.runs = append(f.runs, Run{ID: 500, Status: "in_progress", HeadBranch: "main"})
	r, jobs := fakeRun(400, "main", resultFailure)
	f.runs = append(f.runs, r)
	f.jobs[400] = jobs

	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Window != 1 {
		t.Errorf("Window = %d, want 1", h.Window)
	}
}

// A job the run did not contain — added since, or renamed — is not evidence
// either way, and must not be counted as a pass.
func TestComputeHistorySkipsRunsWithoutTheJob(t *testing.T) {
	f := &fakeRuns{jobs: map[int64][]Job{
		500: {{Name: "Some Other Job", Conclusion: resultSuccess}},
		400: {{Name: "Trivy", Conclusion: resultFailure}},
	}}
	f.runs = []Run{
		{ID: 500, Status: "completed", HeadBranch: "main"},
		{ID: 400, Status: "completed", HeadBranch: "main"},
	}
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Examined != 1 || h.Streak != 1 || h.LastGreenRun != 0 {
		t.Errorf("history = %+v: a run without the job was counted", h)
	}
}

// An empty window is UNKNOWN, not never-green: "no green run was found" and "no
// run was found" are different facts and the issue body says so differently.
func TestComputeHistoryOnAnEmptyWindow(t *testing.T) {
	h, err := ComputeHistory(context.Background(), newFake(), "nightly.yml", "main", 600, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Examined != 0 || h.Window != 0 {
		t.Errorf("history = %+v, want an empty window", h)
	}
	if ProvenanceOf(h) != ProvenanceUnknown {
		t.Errorf("provenance = %v, want unknown-history", ProvenanceOf(h))
	}
}

// With no current run id (a local report) every retained run is history.
func TestComputeHistoryWithoutACurrentRun(t *testing.T) {
	f := newFake(entry{500, "main", resultFailure}, entry{400, "main", resultSuccess})
	h, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 0, "Trivy", 50)
	if err != nil {
		t.Fatalf("ComputeHistory: %v", err)
	}
	if h.Window != 2 || h.LastGreenRun != 400 {
		t.Errorf("history = %+v, want both runs considered", h)
	}
}

func TestComputeHistoryPropagatesAPIFailures(t *testing.T) {
	boom := errors.New("boom")

	f := &fakeRuns{runsErr: boom}
	if _, err := ComputeHistory(context.Background(), f, "nightly.yml", "main", 600, "Trivy", 50); !errors.Is(err, boom) {
		t.Errorf("listing failure = %v, want boom", err)
	}

	g := newFake(entry{400, "main", resultFailure})
	g.jobsErr = boom
	if _, err := ComputeHistory(context.Background(), g, "nightly.yml", "main", 600, "Trivy", 50); !errors.Is(err, boom) {
		t.Errorf("jobs failure = %v, want boom", err)
	}
}

func TestFindJob(t *testing.T) {
	// The exact match is reached through ComputeHistory by every test above.
	jobs := []Job{{Name: "FIPS 140-3", ID: 1}, {Name: "Trivy", ID: 2}}
	if _, ok := findJob(jobs, "trivy"); ok {
		t.Error("findJob matched case-insensitively; the API's names are exact")
	}
	if _, ok := findJob(nil, "Trivy"); ok {
		t.Error("findJob found a job in an empty listing")
	}
}
