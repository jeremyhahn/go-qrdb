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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Forge is the subset of the Gitea REST API this tool needs.
//
// Every route below is one of the three sanctioned reads in
// conventions/devops.md §10 or an issues route from the same swagger document.
// In particular NOTHING here touches /actions/tasks: that route lists only jobs
// already dispatched to a runner, so a queued job is absent rather than
// paginated away, and its ids are a different id space from the job ids the log
// route accepts.
type Forge struct {
	// api is the API root, e.g. http://forge.example/api/v1 — no trailing slash.
	api string
	// repo is "owner/name".
	repo string
	// token authenticates every call. Empty is legal: the read routes below
	// work anonymously against a public repository, and report mode on a
	// developer's machine has no token. A write with an empty token is refused
	// before it is attempted, in run(), rather than being sent as an anonymous
	// request that Gitea answers with a permissions error — the two failures
	// look nothing alike to whoever reads the log.
	token  string
	client *http.Client
}

// NewForge returns a Forge talking to api as repo, authenticating with token.
func NewForge(api, repo, token string) *Forge {
	return &Forge{
		api:    strings.TrimRight(api, "/"),
		repo:   repo,
		token:  token,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Run is one entry from the actions-runs listing.
//
// conclusion and head_branch are ABSENT rather than null while a run is still
// going, so both decode to the empty string and every caller must treat "" as
// "not completed" rather than as a conclusion. Measured against Gitea 1.27 on
// 2026-09-12: an in-progress run's object carries no `conclusion` key at all.
type Run struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Event      string `json:"event"`
	StartedAt  string `json:"started_at"`
	HTMLURL    string `json:"html_url"`
}

// Job is one entry from a run's jobs listing, with the same absent-key
// behaviour as Run.
type Job struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	Steps      []Step `json:"steps"`
}

// Step is one step of a Job.
type Step struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// Issue is the subset of a Gitea issue this tool reads or writes.
type Issue struct {
	// Index is the per-repository number a human writes as #N. The `id` field
	// is a global database key and is NOT what the issue routes take.
	Index   int64  `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// WorkflowRuns lists the most recent runs of one workflow file, newest first.
func (f *Forge) WorkflowRuns(ctx context.Context, workflow string, limit int) ([]Run, error) {
	var payload struct {
		Runs []Run `json:"workflow_runs"`
	}
	path := "/repos/" + f.repo + "/actions/workflows/" + url.PathEscape(workflow) +
		"/runs?limit=" + strconv.Itoa(limit)
	if err := f.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	return payload.Runs, nil
}

// Run reads one run by its DATABASE id.
//
// The id, not the run_number the web UI shows: they are different values and
// the number 404s here (conventions/devops.md §10). GITHUB_RUN_ID carries the
// database id, which is what makes this route reachable from inside the run it
// describes — verified end to end on Gitea 1.27, run 17285.
func (f *Forge) Run(ctx context.Context, runID int64) (Run, error) {
	var run Run
	path := "/repos/" + f.repo + "/actions/runs/" + strconv.FormatInt(runID, 10)
	err := f.do(ctx, http.MethodGet, path, nil, &run)
	return run, err
}

// RunJobs lists every job of a run, in every state.
func (f *Forge) RunJobs(ctx context.Context, runID int64) ([]Job, error) {
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	path := "/repos/" + f.repo + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/jobs"
	if err := f.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	return payload.Jobs, nil
}

// JobLog returns a job's full plain-text log.
func (f *Forge) JobLog(ctx context.Context, jobID int64) (string, error) {
	path := "/repos/" + f.repo + "/actions/jobs/" + strconv.FormatInt(jobID, 10) + "/logs"
	body, err := f.raw(ctx, http.MethodGet, path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// OpenIssues lists every open issue, walking pages until one comes back short.
//
// maxPages is a REFUSAL, not a convenience. Silently stopping at a page cap
// would make "the tracking issue is on page 21" indistinguishable from "there
// is no tracking issue", and this tool's whole job is to tell those two apart —
// the second opens a duplicate every night.
func (f *Forge) OpenIssues(ctx context.Context, maxPages int) ([]Issue, error) {
	const perPage = 50
	var all []Issue
	for page := 1; page <= maxPages; page++ {
		var batch []Issue
		path := "/repos/" + f.repo + "/issues?state=open&type=issues" +
			"&limit=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
		if err := f.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			return all, nil
		}
	}
	return nil, &PaginationError{Route: "issues", MaxPages: maxPages, Seen: len(all)}
}

// CreateIssue opens an issue and returns it.
func (f *Forge) CreateIssue(ctx context.Context, title, body string) (Issue, error) {
	var created Issue
	req := map[string]string{"title": title, "body": body}
	err := f.do(ctx, http.MethodPost, "/repos/"+f.repo+"/issues", req, &created)
	return created, err
}

// EditIssue rewrites an open issue's title and body in place.
//
// A rewrite rather than a comment, deliberately. Gitea does not notify on an
// edit, so a still-red nightly refreshes the record without ringing a bell that
// would be rung every night until the reader stopped hearing it.
func (f *Forge) EditIssue(ctx context.Context, index int64, title, body string) error {
	req := map[string]string{"title": title, "body": body}
	return f.do(ctx, http.MethodPatch, f.issuePath(index), req, nil)
}

// CommentIssue posts a comment. Used only on the green transition, which is the
// one event worth a notification.
func (f *Forge) CommentIssue(ctx context.Context, index int64, body string) error {
	req := map[string]string{"body": body}
	return f.do(ctx, http.MethodPost, f.issuePath(index)+"/comments", req, nil)
}

// CloseIssue sets an issue's state to closed.
func (f *Forge) CloseIssue(ctx context.Context, index int64) error {
	req := map[string]string{"state": "closed"}
	return f.do(ctx, http.MethodPatch, f.issuePath(index), req, nil)
}

// AddLabels applies labels BY NAME.
//
// Gitea's AddLabels accepts names as well as ids on this route, which matters:
// ids are per-instance and hard-coding them here would make this tool silently
// mislabel on any other deployment, whereas a missing NAME is an error someone
// can read.
func (f *Forge) AddLabels(ctx context.Context, index int64, names []string) error {
	req := map[string][]string{"labels": names}
	return f.do(ctx, http.MethodPost, f.issuePath(index)+"/labels", req, nil)
}

func (f *Forge) issuePath(index int64) string {
	return "/repos/" + f.repo + "/issues/" + strconv.FormatInt(index, 10)
}

// do performs one API call, decoding a JSON response into out when out is
// non-nil.
func (f *Forge) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &EncodeError{Route: path, Err: err}
		}
		payload = bytes.NewReader(encoded)
	}

	raw, err := f.send(ctx, method, path, payload)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &DecodeError{Route: path, Err: err}
	}
	return nil
}

// raw performs one API call and returns the body undecoded.
func (f *Forge) raw(ctx context.Context, method, path string) ([]byte, error) {
	return f.send(ctx, method, path, nil)
}

func (f *Forge) send(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.api+path, body)
	if err != nil {
		return nil, &TransportError{Method: method, Route: path, Err: err}
	}
	if f.token != "" {
		req.Header.Set("Authorization", "token "+f.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, &TransportError{Method: method, Route: path, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded so a runaway response cannot exhaust the runner. 32 MiB is far
	// above the largest job log measured here (36 KB) and far below anything
	// that would matter.
	const maxBody = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &TransportError{Method: method, Route: path, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{
			Method: method,
			Route:  path,
			Status: resp.StatusCode,
			Body:   truncate(strings.TrimSpace(string(raw)), 400),
		}
	}
	return raw, nil
}

// truncate shortens s to at most n bytes, marking that it did so.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "… (truncated)"
}
