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
	"os"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// DeclaredJob is one entry of a workflow file's `jobs:` mapping.
type DeclaredJob struct {
	// Key is the YAML key, which is what `needs.<key>.result` uses.
	Key string
	// Name is the job's `name:`, which is what the runs API reports. Gitea
	// falls back to the key when a job declares no name, so this does too.
	Name string
}

// DeclaredJobs reads a workflow file and returns its jobs in file order.
//
// PARSED YAML, not grep. The same reason test/buildgates/action_pins_test.go
// gives: a line-oriented reader cannot see the spellings Actions accepts, and
// it reports every `jobs:`-looking line in a block scalar as a declaration.
// Here the consequence is worse than a false positive — a MISSED job is a job
// nothing watches, which is the defect this whole tool exists to end.
func DeclaredJobs(path string) ([]DeclaredJob, error) {
	raw, err := os.ReadFile(path) //#nosec G304 -- an operator-supplied flag naming a file in this repository, never a request value.
	if err != nil {
		return nil, &WorkflowParseError{Path: path, Err: err}
	}

	var doc struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, &WorkflowParseError{Path: path, Err: err}
	}
	if doc.Jobs.Kind != yaml.MappingNode {
		return nil, &WorkflowParseError{
			Path: path,
			Err:  &ConfigError{What: "jobs:", Why: "is not a mapping, so no job could be discovered"},
		}
	}

	// A mapping node's Content alternates key, value.
	jobs := make([]DeclaredJob, 0, len(doc.Jobs.Content)/2)
	for i := 0; i+1 < len(doc.Jobs.Content); i += 2 {
		key := doc.Jobs.Content[i].Value
		job := DeclaredJob{Key: key, Name: key}
		value := doc.Jobs.Content[i+1]
		if value.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(value.Content); j += 2 {
				if value.Content[j].Value == "name" {
					job.Name = value.Content[j+1].Value
				}
			}
		}
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		return nil, &WorkflowParseError{
			Path: path,
			Err:  &ConfigError{What: "jobs:", Why: "is empty, so this check would be vacuous"},
		}
	}
	return jobs, nil
}

// AssertCoverage is the discovery floor.
//
// Every job the workflow declares, except the signal job itself, must be
// covered by exactly one -job flag, and the display name in that flag must be
// the one the workflow declares — because the runs API reports the NAME, and a
// flag whose name is wrong finds no history and silently reports "no prior run
// retained" forever.
//
// The alternative shape — have the tool read `needs` or enumerate the jobs from
// the API — was rejected deliberately. conventions/devops.md §1 records that
// `join(needs.*.result)` is a GitHub extension that evaluates to the empty
// string on Gitea, so a gate built on it passes while jobs fail; the ruling
// there is to list every job explicitly and fail closed when the results cannot
// be read. This is that list, and this function is the "fail closed".
func AssertCoverage(file string, declared []DeclaredJob, specs []JobSpec, self string) error {
	byKey := make(map[string]DeclaredJob, len(declared))
	for _, d := range declared {
		byKey[d.Key] = d
	}

	covered := make(map[string]int, len(specs))
	var unexpected []string
	for _, s := range specs {
		covered[s.Key]++
		d, ok := byKey[s.Key]
		if !ok {
			unexpected = append(unexpected, s.Key)
			continue
		}
		if d.Name != s.Name {
			return &JobSpecError{
				Spec: s.Key + "=" + s.Name + "=" + s.Result,
				Why: "the workflow declares this job's name as " + strconv.Quote(d.Name) +
					"; the runs API reports the NAME, so a mismatch would read every job's history as absent",
			}
		}
	}

	var missing []string
	for _, d := range declared {
		if d.Key == self {
			continue
		}
		if covered[d.Key] == 0 {
			missing = append(missing, d.Key)
		}
	}
	for key, n := range covered {
		if n > 1 {
			return &JobSpecError{
				Spec: key,
				Why:  "passed " + strconv.Itoa(n) + " times; each job is decided once",
			}
		}
	}

	if len(missing) > 0 || len(unexpected) > 0 {
		sort.Strings(missing)
		sort.Strings(unexpected)
		return &JobCoverageError{WorkflowFile: file, Missing: missing, Unexpected: unexpected}
	}
	if covered[self] > 0 {
		return &JobSpecError{
			Spec: self,
			Why:  "is the signal job itself, which cannot report on its own result",
		}
	}
	return nil
}
