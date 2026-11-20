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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestForgeReadsEveryRouteItUses(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	fake.addRun(100, testBranch, map[string]string{trivyName: resultFailure})
	forge := NewForge(fake.api(), testRepo, "token")
	ctx := t.Context()

	runs, err := forge.WorkflowRuns(ctx, "nightly.yml", 50)
	if err != nil || len(runs) != 1 || runs[0].ID != 100 {
		t.Fatalf("WorkflowRuns = %v, %v", runs, err)
	}

	one, err := forge.Run(ctx, 100)
	if err != nil || one.HeadBranch != testBranch {
		t.Fatalf("Run = %+v, %v", one, err)
	}

	jobs, err := forge.RunJobs(ctx, 100)
	if err != nil || len(jobs) != 1 || jobs[0].Name != trivyName {
		t.Fatalf("RunJobs = %v, %v", jobs, err)
	}

	log, err := forge.JobLog(ctx, jobs[0].ID)
	if err != nil || !strings.Contains(log, "CVE-2026-84445") {
		t.Fatalf("JobLog = %q, %v", log, err)
	}
}

func TestForgeWritesEveryRouteItUses(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	forge := NewForge(fake.api(), testRepo, "token")
	ctx := t.Context()

	created, err := forge.CreateIssue(ctx, "a title", "a body")
	if err != nil || created.Index == 0 {
		t.Fatalf("CreateIssue = %+v, %v", created, err)
	}
	if err := forge.AddLabels(ctx, created.Index, []string{"task:ready"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if err := forge.EditIssue(ctx, created.Index, "a new title", "a new body"); err != nil {
		t.Fatalf("EditIssue: %v", err)
	}
	if err := forge.CommentIssue(ctx, created.Index, "a comment"); err != nil {
		t.Fatalf("CommentIssue: %v", err)
	}
	if err := forge.CloseIssue(ctx, created.Index); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	issue := fake.issueByIndex(created.Index)
	if issue.Title != "a new title" || issue.Body != "a new body" || issue.State != "closed" {
		t.Errorf("issue after the sequence = %+v", issue)
	}
	if len(fake.comments[created.Index]) != 1 || !contains(fake.labels[created.Index], "task:ready") {
		t.Errorf("comments = %v, labels = %v", fake.comments, fake.labels)
	}
}

// A trailing slash on -api must not produce a double slash that 404s.
func TestNewForgeTrimsTheAPIRoot(t *testing.T) {
	fake := newFakeGitea(t, testRepo)
	forge := NewForge(fake.api()+"/", testRepo, "token")
	if _, err := forge.OpenIssues(t.Context(), 5); err != nil {
		t.Fatalf("OpenIssues against a trailing-slash root: %v", err)
	}
}

// The listing must REFUSE rather than stop short. Stopping would make "the
// tracker is on page 21" look like "there is no tracker", which opens a
// duplicate every night — the one failure this tool cannot be allowed to have.
func TestOpenIssuesRefusesRatherThanStoppingShort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var full []string
		for i := 0; i < 50; i++ {
			full = append(full, `{"number":`+strconv.Itoa(i+1)+`,"body":"x","state":"open"}`)
		}
		_, _ = w.Write([]byte("[" + strings.Join(full, ",") + "]"))
	}))
	defer server.Close()

	forge := NewForge(server.URL+"/api/v1", testRepo, "token")
	_, err := forge.OpenIssues(t.Context(), 3)
	var page *PaginationError
	if !errors.As(err, &page) {
		t.Fatalf("error = %v, want *PaginationError", err)
	}
	if page.MaxPages != 3 || page.Seen != 150 {
		t.Errorf("PaginationError = %+v", page)
	}
	mustContain(t, page.Error(), "would make an existing tracking issue look absent")
}

func TestForgeReportsANonSuccessStatusAsAnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"no such workflow"}`, http.StatusNotFound)
	}))
	defer server.Close()

	forge := NewForge(server.URL+"/api/v1", testRepo, "token")
	_, err := forge.WorkflowRuns(t.Context(), "nightly.yml", 10)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusNotFound || !strings.Contains(apiErr.Body, "no such workflow") {
		t.Errorf("APIError = %+v", apiErr)
	}
	mustContain(t, apiErr.Error(), "/actions/workflows/nightly.yml/runs")
}

// A long error body is truncated so an HTML error page does not become the
// whole log line.
func TestForgeTruncatesAHugeErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer server.Close()

	forge := NewForge(server.URL+"/api/v1", testRepo, "token")
	_, err := forge.RunJobs(t.Context(), 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	mustContain(t, apiErr.Body, "(truncated)")
	if len(apiErr.Body) > 500 {
		t.Errorf("body is %d bytes after truncation", len(apiErr.Body))
	}
}

func TestForgeReportsAMalformedAnswerAsADecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer server.Close()

	forge := NewForge(server.URL+"/api/v1", testRepo, "token")
	_, err := forge.Run(t.Context(), 1)
	var decode *DecodeError
	if !errors.As(err, &decode) {
		t.Fatalf("error = %v, want *DecodeError", err)
	}
	if errors.Unwrap(decode) == nil {
		t.Error("DecodeError does not wrap the cause")
	}
}

func TestForgeReportsAnUnreachableHostAsATransportError(t *testing.T) {
	// A port nothing listens on: the connection is refused rather than hanging.
	forge := NewForge("http://127.0.0.1:1/api/v1", testRepo, "token")
	_, err := forge.OpenIssues(t.Context(), 1)
	var transport *TransportError
	if !errors.As(err, &transport) {
		t.Fatalf("error = %v, want *TransportError", err)
	}
	if errors.Unwrap(transport) == nil {
		t.Error("TransportError does not wrap the cause")
	}
}

func TestForgeReportsAnUnbuildableRequestAsATransportError(t *testing.T) {
	forge := NewForge("://not a url", testRepo, "token")
	_, err := forge.Run(t.Context(), 1)
	var transport *TransportError
	if !errors.As(err, &transport) {
		t.Fatalf("error = %v, want *TransportError", err)
	}
}

// The token must reach the wire, or every call is anonymous and a private
// repository answers 404 to all of them.
func TestForgeSendsTheToken(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	forge := NewForge(server.URL+"/api/v1", testRepo, "s3cret")
	if _, err := forge.OpenIssues(t.Context(), 1); err != nil {
		t.Fatalf("OpenIssues: %v", err)
	}
	if seen != "token s3cret" {
		t.Errorf("Authorization = %q, want %q", seen, "token s3cret")
	}
}

func TestClampColumn(t *testing.T) {
	if got := clampColumn("short"); got != "short" {
		t.Errorf("clampColumn altered a short line: %q", got)
	}
	long := strings.Repeat("x", maxExcerptColumn+10)
	got := clampColumn(long)
	if !strings.HasSuffix(got, " …") || len(got) != maxExcerptColumn+len(" …") {
		t.Errorf("clampColumn produced %d bytes", len(got))
	}
}
