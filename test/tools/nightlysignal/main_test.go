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
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestJobListParsesAThreeFieldSpec(t *testing.T) {
	var list jobList
	if err := list.Set("fips=FIPS 140-3=failure"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := JobSpec{Key: "fips", Name: "FIPS 140-3", Result: "failure"}
	if list[0] != want {
		t.Errorf("parsed %+v, want %+v", list[0], want)
	}
	if err := list.Set("trivy=Trivy=success"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if list.String() != "fips,trivy" {
		t.Errorf("String() = %q", list.String())
	}
}

// A display name may carry an `=`; the key and the result may not, because the
// split is on the first two.
func TestJobListSplitsOnTheFirstTwoSeparators(t *testing.T) {
	var list jobList
	if err := list.Set("k=a name = with equals=failure"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if list[0].Name != "a name " {
		t.Errorf("Name = %q", list[0].Name)
	}
	if list[0].Result != " with equals=failure" {
		t.Errorf("Result = %q", list[0].Result)
	}
}

func TestJobListRefusesAMalformedSpec(t *testing.T) {
	cases := map[string]string{
		"too few fields": "fips=FIPS 140-3",
		"no separator":   "fips",
		"empty key":      "=FIPS 140-3=failure",
		"empty name":     "fips= =failure",
		"empty result":   "fips=FIPS 140-3=",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			var list jobList
			err := list.Set(spec)
			var specErr *JobSpecError
			if !errors.As(err, &specErr) {
				t.Fatalf("Set(%q) error = %v, want *JobSpecError", spec, err)
			}
			if len(list) != 0 {
				t.Error("a rejected spec was still appended")
			}
		})
	}
}

// Every refusal in validate, exercised. These are the branches a healthy run
// never reaches, which is exactly why they would otherwise ship untested —
// the same reasoning the scripts/check-*_test.sh guards carry.
func TestParseFlagsRefusesAnUnusableConfiguration(t *testing.T) {
	base := []string{
		"-repo", testRepo,
		"-api", "http://forge.example/api/v1",
		"-ref", "main",
		"-job", "fips=FIPS 140-3=failure",
		"-run-id", "1",
	}
	cases := []struct {
		name  string
		args  []string
		token string
		want  string
	}{
		{"bad mode", append(base, "-mode", "shout"), "t", "-mode"},
		{"no repo", []string{"-api", "x", "-ref", "main", "-job", "a=b=c"}, "t", "-repo"},
		{"repo without a slash", []string{"-repo", "goqrdb", "-api", "x", "-ref", "main", "-job", "a=b=c"}, "t", "-repo"},
		{"no api", []string{"-repo", testRepo, "-ref", "main", "-job", "a=b=c"}, "t", "-api"},
		{"no ref", []string{"-repo", testRepo, "-api", "x", "-job", "a=b=c"}, "t", "-ref"},
		{"no jobs", []string{"-repo", testRepo, "-api", "x", "-ref", "main"}, "t", "-job"},
		{"no token", base, "", "NIGHTLY_SIGNAL_TOKEN"},
		{"apply without a run id", append(append([]string{}, base[:len(base)-2]...), "-mode", "apply"), "t", "-run-id"},
		{"history limit of zero", append(base, "-history-limit", "0"), "t", "-history-limit"},
		{"page cap of zero", append(base, "-max-issue-pages", "0"), "t", "-max-issue-pages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIGHTLY_SIGNAL_TOKEN", tc.token)
			t.Setenv("GITHUB_SERVER_URL", "")
			t.Setenv("GITHUB_REPOSITORY", "")
			t.Setenv("GITHUB_REF_NAME", "")
			t.Setenv("GITHUB_RUN_ID", "")

			_, err := parseFlags(tc.args, io.Discard)
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("parseFlags(%v) error = %v, want *ConfigError", tc.args, err)
			}
			if cfgErr.What != tc.want {
				t.Errorf("refused %q, want %q", cfgErr.What, tc.want)
			}
		})
	}
}

// The environment the runner sets is enough on its own: a workflow passes only
// the flags that have no environment equivalent.
func TestParseFlagsTakesItsDefaultsFromTheRunnerEnvironment(t *testing.T) {
	t.Setenv("NIGHTLY_SIGNAL_TOKEN", "t")
	t.Setenv("GITHUB_SERVER_URL", "http://forge.example/")
	t.Setenv("GITHUB_REPOSITORY", testRepo)
	t.Setenv("GITHUB_REF_NAME", "issue/478-nightly-signal")
	t.Setenv("GITHUB_RUN_ID", "17271")

	cfg, err := parseFlags([]string{"-job", "fips=FIPS 140-3=failure"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.api != "http://forge.example/api/v1" {
		t.Errorf("api = %q", cfg.api)
	}
	if cfg.repo != testRepo || cfg.ref != "issue/478-nightly-signal" || cfg.runID != 17271 {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.mode != modeReport {
		t.Errorf("mode = %q, want the safe default %q", cfg.mode, modeReport)
	}
}

func TestParseFlagsReportsAnUnknownFlag(t *testing.T) {
	t.Setenv("NIGHTLY_SIGNAL_TOKEN", "t")
	if _, err := parseFlags([]string{"-nonesuch"}, io.Discard); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

func TestRunRefusesAWorkflowFileItCannotRead(t *testing.T) {
	t.Setenv("NIGHTLY_SIGNAL_TOKEN", "t")
	err := run([]string{
		"-repo", testRepo, "-api", "http://forge.example/api/v1", "-ref", "main",
		"-job", "fips=FIPS 140-3=failure",
		"-workflow-file", filepath.Join(t.TempDir(), "absent.yml"),
	}, io.Discard)
	var parse *WorkflowParseError
	if !errors.As(err, &parse) {
		t.Fatalf("error = %v, want *WorkflowParseError", err)
	}
}

// Forgetting a -job line when a job is added to the workflow must fail the
// signal job rather than quietly watching one job fewer.
func TestRunRefusesAnIncompleteJobList(t *testing.T) {
	t.Setenv("NIGHTLY_SIGNAL_TOKEN", "t")
	err := run([]string{
		"-repo", testRepo, "-api", "http://forge.example/api/v1", "-ref", "main",
		"-job", "fips=FIPS 140-3=failure",
		"-workflow-file", twoJobWorkflow(t),
		"-self", "signal",
	}, io.Discard)
	var coverage *JobCoverageError
	if !errors.As(err, &coverage) {
		t.Fatalf("error = %v, want *JobCoverageError", err)
	}
	// Only fips was passed, so the other declared job must be reported.
	want := []string{"trivy"}
	got := append([]string(nil), coverage.Missing...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Missing = %v, want %v", coverage.Missing, want)
	}
}

// An unrecognised result reaches run() as a refusal, not as a silent "not red".
func TestRunRefusesAnUnknownJobResult(t *testing.T) {
	h := newHarness(t)
	h.forge.addRun(100, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultFailure})

	err := run([]string{
		"-mode", modeApply,
		"-repo", testRepo, "-api", h.forge.api(), "-ref", testBranch, "-run-id", "100",
		"-workflow-file", twoJobWorkflow(t),
		"-self", "signal",
		"-job", "fips=" + fipsName + "=neutral",
		"-job", "trivy=" + trivyName + "=" + resultSuccess,
	}, io.Discard)

	var unknown *UnknownResultError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *UnknownResultError", err)
	}
	if h.forge.created != 0 {
		t.Error("an unreadable result still wrote to the forge")
	}
}

// One job failing to decide must not stop the others: the second job's finding
// is not less true because the first job's result was unreadable.
func TestOneUndecidableJobDoesNotStopTheRest(t *testing.T) {
	h := newHarness(t)
	h.forge.addRun(100, testBranch, map[string]string{fipsName: resultFailure, trivyName: resultFailure})

	err := run([]string{
		"-mode", modeApply,
		"-repo", testRepo, "-api", h.forge.api(), "-ref", testBranch, "-run-id", "100",
		"-workflow-file", twoJobWorkflow(t),
		"-self", "signal",
		"-job", "fips=" + fipsName + "=neutral",
		"-job", "trivy=" + trivyName + "=" + resultFailure,
	}, io.Discard)

	if err == nil {
		t.Fatal("the undecidable job did not fail the run")
	}
	if h.forge.created != 1 {
		t.Errorf("the decidable job opened %d issues, want 1", h.forge.created)
	}
}

// The run reference falls back to the environment when the API cannot answer,
// because every field it supplies is decoration: refusing to signal at all
// because a start time could not be read would lose the finding to protect a
// table cell.
func TestCurrentRunFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "http://forge.example")
	t.Setenv("GITHUB_SHA", "0123456789abcdef")
	t.Setenv("GITHUB_EVENT_NAME", "schedule")

	cfg := config{repo: testRepo, runID: 17271}
	// A forge that cannot answer at all.
	got := currentRun(t.Context(), cfg, NewForge("http://127.0.0.1:1/api/v1", testRepo, "t"))

	if got.ID != 17271 || got.SHA != "0123456789abcdef" || got.Event != "schedule" {
		t.Errorf("RunRef = %+v", got)
	}
	if got.URL != "http://forge.example/"+testRepo+"/actions/runs/17271" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.StartedAt == "" {
		t.Error("StartedAt is empty; the body would show a dash for a run happening now")
	}
}

func TestCurrentRunWithoutARunIDBuildsNoLink(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "http://forge.example")
	got := currentRun(t.Context(), config{repo: testRepo}, NewForge("http://127.0.0.1:1/api/v1", testRepo, "t"))
	if got.URL != "" {
		t.Errorf("URL = %q, want none: there is no run to link to", got.URL)
	}
}

func TestCurrentRunPrefersWhatTheAPISays(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	fake.addRun(100, testBranch, map[string]string{trivyName: resultFailure})
	t.Setenv("GITHUB_SERVER_URL", fake.server.URL)
	t.Setenv("GITHUB_SHA", "ffffffffffffffff")

	got := currentRun(t.Context(), config{repo: testRepo, runID: 100}, NewForge(fake.api(), testRepo, "t"))
	if got.SHA != "133fa5699947792772bac23fbfb6fb051d600775" {
		t.Errorf("SHA = %q, want the run's own head", got.SHA)
	}
	if got.StartedAt != "2026-09-11T03:00:00Z" || got.Event != "schedule" {
		t.Errorf("RunRef = %+v", got)
	}
}

// A job whose log cannot be located says so in the issue rather than rendering
// an empty findings section.
func TestExcerptForReportsWhatItCouldNotRead(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	forge := NewForge(fake.api(), testRepo, "t")

	listErr := errors.New("the jobs listing failed")
	if got := excerptFor(t.Context(), forge, trivyName, nil, listErr); !errors.Is(got.Err, listErr) {
		t.Errorf("Err = %v, want the listing failure", got.Err)
	}

	absent := excerptFor(t.Context(), forge, "No Such Job", []Job{{Name: trivyName, ID: 1}}, nil)
	var cfgErr *ConfigError
	if !errors.As(absent.Err, &cfgErr) {
		t.Fatalf("Err = %v, want *ConfigError", absent.Err)
	}
	mustContain(t, absent.Err.Error(), "does not appear in this run's job listing")

	noLog := excerptFor(t.Context(), forge, trivyName, []Job{{Name: trivyName, ID: 999}}, nil)
	var apiErr *APIError
	if !errors.As(noLog.Err, &apiErr) {
		t.Fatalf("Err = %v, want *APIError", noLog.Err)
	}
	mustContain(t, noLog.Render(), "defect in this signal")
}

func TestExcerptForReadsTheLogWhenItCan(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	fake.addRun(100, testBranch, map[string]string{trivyName: resultFailure})
	forge := NewForge(fake.api(), testRepo, "t")

	jobs, err := forge.RunJobs(t.Context(), 100)
	if err != nil {
		t.Fatalf("RunJobs: %v", err)
	}
	got := excerptFor(t.Context(), forge, trivyName, jobs, nil)
	if got.Err != nil {
		t.Fatalf("Err = %v", got.Err)
	}
	mustContain(t, strings.Join(got.Lines, "\n"), "CVE-2026-84445")
	if len(got.FailedSteps) != 1 {
		t.Errorf("FailedSteps = %v, want the one failing step", got.FailedSteps)
	}
}

func TestNoteForNoneCoversEveryVerdict(t *testing.T) {
	if !strings.Contains(noteForNone(VerdictGreen), "nothing was open") {
		t.Error("green note")
	}
	if !strings.Contains(noteForNone(VerdictInconclusive), "neither passed nor failed") {
		t.Error("inconclusive note")
	}
	if noteForNone(VerdictRed) != "red" {
		t.Error("red note")
	}
}

func TestFindTrackerOnAnEmptyQueue(t *testing.T) {
	got, err := findTracker(nil, "trivy", "main")
	if err != nil || got != nil {
		t.Errorf("findTracker = %v, %v, want nil, nil", got, err)
	}
}

// Every typed error renders something a human can act on, and the wrapping ones
// unwrap. Cheap to assert and it catches the empty-message case, where a log
// line reads "nightlysignal: " and says nothing.
func TestEveryTypedErrorSaysSomething(t *testing.T) {
	cause := errors.New("the cause")
	wrapped := []error{
		&WorkflowParseError{Path: "w.yml", Err: cause},
		&TransportError{Method: "GET", Route: "/x", Err: cause},
		&DecodeError{Route: "/x", Err: cause},
		&EncodeError{Route: "/x", Err: cause},
		&LabelError{Index: 1, Labels: []string{"task:ready"}, Err: cause},
	}
	for _, err := range wrapped {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}

	all := append([]error{
		&ConfigError{What: "-repo", Why: "missing"},
		&JobCoverageError{WorkflowFile: "w.yml", Missing: []string{"a"}, Unexpected: []string{"b"}},
		&JobSpecError{Spec: "x", Why: "bad"},
		&UnknownResultError{Job: "fips", Result: "neutral"},
		&APIError{Method: "GET", Route: "/x", Status: 500, Body: "boom"},
		&PaginationError{Route: "issues", MaxPages: 3, Seen: 150},
		&DuplicateIssueError{Marker: "m", Indexes: []int64{2, 1}},
	}, wrapped...)
	for _, err := range all {
		message := err.Error()
		if len(message) < 20 || !strings.Contains(message, "nightlysignal") {
			t.Errorf("%T renders %q", err, message)
		}
	}
	mustContain(t, (&DuplicateIssueError{Marker: "m", Indexes: []int64{2, 1}}).Error(), "#1, #2")
	mustContain(t, (&JobCoverageError{WorkflowFile: "w.yml", Unexpected: []string{"b"}}).Error(), "passed but not declared: b")
}

var _ io.Writer = io.Discard
