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

import "context"

// runJobLister is the slice of Forge the history walk needs, so the walk can be
// tested against a table of runs rather than against a live forge.
type runJobLister interface {
	WorkflowRuns(ctx context.Context, workflow string, limit int) ([]Run, error)
	RunJobs(ctx context.Context, runID int64) ([]Job, error)
}

// ComputeHistory walks the retained run window backwards from the current run
// and answers three questions about one job on one ref: has it ever been green,
// when was it last green, and how many runs in a row has it failed.
//
// NEWEST FIRST, STOPPING AT THE FIRST GREEN. Nothing older than the last green
// run changes any answer, and every run examined costs an API call.
//
// A run whose conclusion is neither success nor failure is SKIPPED ENTIRELY —
// it neither breaks the failure streak nor counts as history. A cancelled
// nightly proved nothing about the job, and letting it break the streak would
// report a three-week-old breakage as a fresh regression.
//
// Only runs on the SAME REF count. A nightly dispatched on a branch is a
// statement about that branch; mixing branch runs into main's history would
// make a lane's experiment look like main's regression.
func ComputeHistory(ctx context.Context, f runJobLister, workflow, ref string, currentRunID int64, jobName string, limit int) (History, error) {
	runs, err := f.WorkflowRuns(ctx, workflow, limit)
	if err != nil {
		return History{}, err
	}

	var candidates []Run
	for _, r := range runs {
		if r.HeadBranch != ref || r.Status != "completed" {
			continue
		}
		// Strictly PRIOR. Run ids are monotonic here, so this also excludes a
		// run that started after this one — which happens when a lane
		// dispatches while a scheduled run is still going, and which must not
		// be read as history.
		if currentRunID != 0 && r.ID >= currentRunID {
			continue
		}
		candidates = append(candidates, r)
	}

	history := History{Window: len(candidates)}
	for _, r := range candidates {
		jobs, err := f.RunJobs(ctx, r.ID)
		if err != nil {
			return History{}, err
		}
		job, ok := findJob(jobs, jobName)
		if !ok {
			// The job did not exist in that run — it was added since, or
			// renamed. Not evidence either way.
			continue
		}
		switch job.Conclusion {
		case resultSuccess:
			history.Examined++
			history.LastGreenRun = r.ID
			history.LastGreenAt = r.StartedAt
			return history, nil
		case resultFailure:
			history.Examined++
			history.Streak++
		default:
			// Cancelled, skipped, or still running: no information.
		}
	}
	return history, nil
}

// findJob locates a job by the display name the runs API reports.
func findJob(jobs []Job, name string) (Job, bool) {
	for _, j := range jobs {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}
