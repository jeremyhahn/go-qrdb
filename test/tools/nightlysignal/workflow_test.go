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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// repoRoot is the directory holding this module's go.mod, found by walking up
// rather than by a fixed `../../..` so that moving this package does not point
// the scan at a directory that does not exist.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func nightlyPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), ".github", "workflows", "nightly.yml")
}

func TestDeclaredJobsReadsTheRealNightlyWorkflow(t *testing.T) {
	jobs, err := DeclaredJobs(nightlyPath(t))
	if err != nil {
		t.Fatalf("DeclaredJobs: %v", err)
	}
	want := map[string]string{"fips": "FIPS 140-3", "trivy": "Trivy", "signal": "Signal"}
	if len(jobs) < len(want) {
		t.Fatalf("found %d jobs, want at least %d — discovery has broken", len(jobs), len(want))
	}
	for _, job := range jobs {
		name, ok := want[job.Key]
		if !ok {
			continue
		}
		if job.Name != name {
			t.Errorf("job %s declares name %q, want %q", job.Key, job.Name, name)
		}
		delete(want, job.Key)
	}
	for key := range want {
		t.Errorf("nightly.yml no longer declares a job %q", key)
	}
}

// THE GATE THAT MATTERS, and the reason it is a unit test and not only a
// runtime check: the runtime check fires at 03:00 on a night nobody is
// watching, which is the same failure this whole tool exists to end. Here it
// fires in the Lint job on the push that forgets the flag.
//
// It reads the `-job` list straight out of the workflow's own shell step, so
// adding a job to nightly.yml without adding a `-job` line reddens CI.
func TestTheWorkflowsJobListCoversEveryJobItDeclares(t *testing.T) {
	path := nightlyPath(t)
	declared, err := DeclaredJobs(path)
	if err != nil {
		t.Fatalf("DeclaredJobs: %v", err)
	}

	raw, err := os.ReadFile(path) //#nosec G304 -- a path inside this repository, derived from the test's own location.
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	specs, self := jobFlagsFrom(t, string(raw))

	if err := AssertCoverage(path, declared, specs, self); err != nil {
		t.Fatalf("the -job list in nightly.yml does not cover its jobs: %v", err)
	}
}

var (
	jobFlagPattern  = regexp.MustCompile(`-job\s+"([^"=]+)=([^"=]+)=([^"]*)"`)
	selfFlagPattern = regexp.MustCompile(`-self\s+(\S+)`)
)

// jobFlagsFrom lifts the -job and -self flags out of the workflow's shell step.
//
// A DISCOVERY FLOOR of its own: finding no flags would make the assertion above
// vacuously pass, which is precisely the shape of failure it exists to stop.
func jobFlagsFrom(t *testing.T, workflow string) ([]JobSpec, string) {
	t.Helper()
	matches := jobFlagPattern.FindAllStringSubmatch(workflow, -1)
	if len(matches) == 0 {
		t.Fatal("no -job flags found in nightly.yml: either the signal step is gone or its spelling changed, and this assertion would pass over anything")
	}
	specs := make([]JobSpec, 0, len(matches))
	for _, m := range matches {
		specs = append(specs, JobSpec{Key: m[1], Name: m[2], Result: m[3]})
	}

	self := selfFlagPattern.FindStringSubmatch(workflow)
	if self == nil {
		t.Fatal("no -self flag found in nightly.yml; the signal job would be asked to report on itself")
	}
	return specs, self[1]
}

// And the `-job` values must be Actions expressions reading `needs.<key>.result`
// for the job they name. A flag pointing at the wrong `needs` entry would watch
// one job while reporting another's verdict, and every assertion above would
// still pass.
func TestEachJobFlagReadsItsOwnNeedsResult(t *testing.T) {
	raw, err := os.ReadFile(nightlyPath(t)) //#nosec G304 -- a path inside this repository.
	if err != nil {
		t.Fatalf("reading nightly.yml: %v", err)
	}
	workflow := string(raw)
	specs, _ := jobFlagsFrom(t, workflow)

	for _, spec := range specs {
		// The shell step passes the result through an env var, so both halves
		// are checked: the flag reads ${RESULT_X} and RESULT_X is bound to
		// needs.<key>.result.
		variable := strings.TrimSuffix(strings.TrimPrefix(spec.Result, "${"), "}")
		if variable == "" || variable == spec.Result {
			t.Errorf("job %s takes its result from %q, which is not a shell variable", spec.Key, spec.Result)
			continue
		}
		binding := variable + ": ${{ needs." + spec.Key + ".result }}"
		if !strings.Contains(workflow, binding) {
			t.Errorf("job %s reads %s, but nightly.yml does not bind it with %q", spec.Key, variable, binding)
		}
	}
}

func TestAssertCoverageAcceptsAnExactMatch(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"trivy", "Trivy"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}, {Key: "trivy", Name: "Trivy"}}
	if err := AssertCoverage("nightly.yml", declared, specs, "signal"); err != nil {
		t.Fatalf("AssertCoverage: %v", err)
	}
}

func TestAssertCoverageRefusesAnUnwatchedJob(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"trivy", "Trivy"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var coverage *JobCoverageError
	if !errors.As(err, &coverage) {
		t.Fatalf("error = %v, want *JobCoverageError", err)
	}
	if len(coverage.Missing) != 1 || coverage.Missing[0] != "trivy" {
		t.Errorf("Missing = %v, want [trivy]", coverage.Missing)
	}
	mustContain(t, coverage.Error(), "declared but not passed: trivy")
}

// Every unwatched job is reported, not just the first: a coverage check that
// named one gap and stopped would let a second missing job through on the
// retry. The real nightly.yml now declares only two watched jobs, so this is
// proved on a fixture rather than through run().
func TestAssertCoverageReportsEveryUnwatchedJob(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"trivy", "Trivy"}, {"extra", "Extra"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var coverage *JobCoverageError
	if !errors.As(err, &coverage) {
		t.Fatalf("error = %v, want *JobCoverageError", err)
	}
	got := append([]string(nil), coverage.Missing...)
	slices.Sort(got)
	if want := []string{"extra", "trivy"}; !slices.Equal(got, want) {
		t.Errorf("Missing = %v, want %v", coverage.Missing, want)
	}
}

func TestAssertCoverageRefusesAJobTheWorkflowDoesNotDeclare(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}, {Key: "gone", Name: "Gone"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var coverage *JobCoverageError
	if !errors.As(err, &coverage) {
		t.Fatalf("error = %v, want *JobCoverageError", err)
	}
	if len(coverage.Unexpected) != 1 || coverage.Unexpected[0] != "gone" {
		t.Errorf("Unexpected = %v, want [gone]", coverage.Unexpected)
	}
}

// A display name that disagrees with the workflow is refused rather than
// tolerated: the runs API reports the NAME, so a wrong one reads every prior
// run's history as absent, forever, silently.
func TestAssertCoverageRefusesAMisnamedJob(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var spec *JobSpecError
	if !errors.As(err, &spec) {
		t.Fatalf("error = %v, want *JobSpecError", err)
	}
	mustContain(t, spec.Error(), "the runs API reports the NAME")
}

func TestAssertCoverageRefusesADuplicateJob(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}, {Key: "fips", Name: "FIPS 140-3"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var spec *JobSpecError
	if !errors.As(err, &spec) {
		t.Fatalf("error = %v, want *JobSpecError", err)
	}
	mustContain(t, spec.Error(), "each job is decided once")
}

func TestAssertCoverageRefusesTheSignalJobReportingOnItself(t *testing.T) {
	declared := []DeclaredJob{{"fips", "FIPS 140-3"}, {"signal", "Signal"}}
	specs := []JobSpec{{Key: "fips", Name: "FIPS 140-3"}, {Key: "signal", Name: "Signal"}}

	err := AssertCoverage("nightly.yml", declared, specs, "signal")
	var spec *JobSpecError
	if !errors.As(err, &spec) {
		t.Fatalf("error = %v, want *JobSpecError", err)
	}
	mustContain(t, spec.Error(), "cannot report on its own result")
}

func TestDeclaredJobsRefusesAFileItCannotRead(t *testing.T) {
	_, err := DeclaredJobs(filepath.Join(t.TempDir(), "absent.yml"))
	var parse *WorkflowParseError
	if !errors.As(err, &parse) {
		t.Fatalf("error = %v, want *WorkflowParseError", err)
	}
	if errors.Unwrap(parse) == nil {
		t.Error("WorkflowParseError does not wrap the cause")
	}
}

func TestDeclaredJobsRefusesAWorkflowWithNoJobs(t *testing.T) {
	cases := map[string]string{
		"no jobs key":     "name: X\non:\n  schedule: []\n",
		"jobs not a map":  "name: X\njobs: []\n",
		"jobs empty map":  "name: X\njobs: {}\n",
		"not yaml at all": "\tthis: [is: not\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w.yml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := DeclaredJobs(path)
			var parse *WorkflowParseError
			if !errors.As(err, &parse) {
				t.Fatalf("error = %v, want *WorkflowParseError", err)
			}
		})
	}
}

// A job with no `name:` is reported under its key, which is what Gitea does.
func TestDeclaredJobsFallsBackToTheKeyWhenAJobHasNoName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.yml")
	content := "jobs:\n  bare:\n    runs-on: ubuntu-latest\n  named:\n    name: Pretty\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	jobs, err := DeclaredJobs(path)
	if err != nil {
		t.Fatalf("DeclaredJobs: %v", err)
	}
	want := []DeclaredJob{{"bare", "bare"}, {"named", "Pretty"}}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v, want %v", jobs, want)
	}
	for i := range want {
		if jobs[i] != want[i] {
			t.Errorf("jobs[%d] = %+v, want %+v", i, jobs[i], want[i])
		}
	}
}
