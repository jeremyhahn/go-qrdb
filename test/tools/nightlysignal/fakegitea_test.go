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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeGitea is a forge that remembers what was done to it.
//
// Deliberately a STATEFUL double rather than a canned-response stub. The
// property under test is a sequence — open, then do not duplicate, then close —
// and a stub that answers each call independently can satisfy every individual
// assertion while the sequence is wrong. It is also why it counts writes: "no
// second issue was opened" is only checkable against something that would have
// recorded one.
type fakeGitea struct {
	t    *testing.T
	repo string

	issues   []*Issue
	comments map[int64][]string
	labels   map[int64][]string

	runs []Run
	jobs map[int64][]Job
	logs map[int64]string

	created   int
	patched   int
	commented int
	labelled  int

	// failLabels makes the label route answer 500, for the case where the
	// signal lands but the triage does not.
	failLabels bool
	// failPatch makes every issue edit fail, which is both the still-red
	// rewrite and the close.
	failPatch bool
	// failComment makes the comment route fail, so a close that cannot announce
	// itself is exercised.
	failComment bool
	// requireToken rejects an unauthenticated call the way a private repo does.
	requireToken bool

	server *httptest.Server
}

func newFakeGitea(t *testing.T, repo string) *fakeGitea {
	t.Helper()
	f := &fakeGitea{
		t:        t,
		repo:     repo,
		comments: map[int64][]string{},
		labels:   map[int64][]string{},
		jobs:     map[int64][]Job{},
		logs:     map[int64]string{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// api is the value for -api.
func (f *fakeGitea) api() string { return f.server.URL + "/api/v1" }

// addRun records a completed run and its jobs, so history has something to
// walk.
func (f *fakeGitea) addRun(id int64, ref string, conclusions map[string]string) {
	f.runs = append([]Run{{
		ID: id, Status: "completed", HeadBranch: ref, HeadSHA: "133fa5699947792772bac23fbfb6fb051d600775",
		Event: "schedule", StartedAt: "2026-09-11T03:00:00Z",
		HTMLURL: f.server.URL + "/" + f.repo + "/actions/runs/" + strconv.FormatInt(id, 10),
	}}, f.runs...)

	var jobs []Job
	for name, conclusion := range conclusions {
		jobID := id*100 + int64(len(jobs))
		jobs = append(jobs, Job{
			ID: jobID, RunID: id, Name: name, Status: "completed", Conclusion: conclusion,
			Steps: []Step{{Name: "the work", Conclusion: conclusion}},
		})
		f.logs[jobID] = trivyRedLog
	}
	f.jobs[id] = jobs
}

func (f *fakeGitea) openIssues() []*Issue {
	var open []*Issue
	for _, issue := range f.issues {
		if issue.State == "open" {
			open = append(open, issue)
		}
	}
	return open
}

func (f *fakeGitea) issueByIndex(index int64) *Issue {
	for _, issue := range f.issues {
		if issue.Index == index {
			return issue
		}
	}
	return nil
}

func (f *fakeGitea) handle(w http.ResponseWriter, r *http.Request) {
	if f.requireToken && r.Header.Get("Authorization") == "" {
		// What a private repository actually answers: not 401, but 404 — which
		// is exactly why an anonymous read looks like an empty repo.
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	prefix := "/api/v1/repos/" + f.repo
	path := strings.TrimPrefix(r.URL.Path, prefix)

	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/actions/workflows/"):
		f.writeJSON(w, map[string]any{"workflow_runs": f.runs, "total_count": len(f.runs)})

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/jobs"):
		id := idFrom(path, "/actions/runs/", "/jobs")
		f.writeJSON(w, map[string]any{"jobs": f.jobs[id], "total_count": len(f.jobs[id])})

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/logs"):
		id := idFrom(path, "/actions/jobs/", "/logs")
		log, ok := f.logs[id]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(log))

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/actions/runs/"):
		id := idFrom(path, "/actions/runs/", "")
		for _, run := range f.runs {
			if run.ID == id {
				f.writeJSON(w, run)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/issues"):
		// One page is enough for every case here; OpenIssues stops when a page
		// comes back short, and the pagination REFUSAL has its own test.
		page := r.URL.Query().Get("page")
		if page != "" && page != "1" {
			f.writeJSON(w, []Issue{})
			return
		}
		listed := make([]Issue, 0, len(f.issues))
		for _, issue := range f.openIssues() {
			listed = append(listed, *issue)
		}
		f.writeJSON(w, listed)

	case r.Method == http.MethodPost && path == "/issues":
		var req struct{ Title, Body string }
		f.decode(r, &req)
		f.created++
		issue := &Issue{
			Index: int64(len(f.issues) + 1), Title: req.Title, Body: req.Body, State: "open",
			HTMLURL: f.server.URL + "/" + f.repo + "/issues/" + strconv.Itoa(len(f.issues)+1),
		}
		f.issues = append(f.issues, issue)
		f.writeJSON(w, *issue)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/labels"):
		if f.failLabels {
			http.Error(w, `{"message":"no such label"}`, http.StatusUnprocessableEntity)
			return
		}
		index := idFrom(path, "/issues/", "/labels")
		var req struct{ Labels []string }
		f.decode(r, &req)
		f.labelled++
		f.labels[index] = append(f.labels[index], req.Labels...)
		f.writeJSON(w, []any{})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
		if f.failComment {
			http.Error(w, `{"message":"no"}`, http.StatusInternalServerError)
			return
		}
		index := idFrom(path, "/issues/", "/comments")
		var req struct{ Body string }
		f.decode(r, &req)
		f.commented++
		f.comments[index] = append(f.comments[index], req.Body)
		f.writeJSON(w, map[string]any{"id": 1})

	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/issues/"):
		if f.failPatch {
			http.Error(w, `{"message":"no"}`, http.StatusInternalServerError)
			return
		}
		index := idFrom(path, "/issues/", "")
		var req map[string]string
		f.decode(r, &req)
		issue := f.issueByIndex(index)
		if issue == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		if state, ok := req["state"]; ok {
			issue.State = state
		}
		if body, ok := req["body"]; ok {
			f.patched++
			issue.Body = body
		}
		if title, ok := req["title"]; ok {
			issue.Title = title
		}
		f.writeJSON(w, *issue)

	default:
		f.t.Errorf("fakeGitea: unhandled %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"message":"unhandled"}`, http.StatusNotImplemented)
	}
}

func (f *fakeGitea) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("fakeGitea: encoding response: %v", err)
	}
}

func (f *fakeGitea) decode(r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		f.t.Errorf("fakeGitea: decoding request: %v", err)
	}
}

// idFrom lifts the numeric id between two path fragments.
func idFrom(path, after, before string) int64 {
	rest := path
	if i := strings.Index(rest, after); i >= 0 {
		rest = rest[i+len(after):]
	}
	if before != "" {
		rest = strings.TrimSuffix(rest, before)
	}
	id, _ := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
	return id
}
