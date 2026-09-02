// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GetUserByEmail must POST the canonical login key in the BODY, never in the URL.
//
// This client method and its endpoint were both added by this PR with nothing
// exercising them, so a route mismatch or a wire-field typo would have compiled
// and shipped (Codex, PR #191). The URL assertion is the load-bearing one:
// email is the canonical login identifier, and a path parameter is captured by
// access logs, proxies and tracing on every successful lookup.
func TestRESTGetUserByEmail_PostsTheKeyInTheBody(t *testing.T) {
	var gotPath, gotMethod, gotQuery string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotQuery = r.URL.Path, r.Method, r.URL.RawQuery
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "42", "username": "alice", "email": "alice@example.com", "tenant_id": "t1",
		})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	u, err := tr.GetUserByEmail(t.Context(), "t1", "alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/users/lookup" {
		t.Fatalf("path = %q, want /users/lookup", gotPath)
	}

	// The login key must appear NOWHERE in the URL.
	if strings.Contains(gotPath, "alice") || strings.Contains(gotQuery, "alice") {
		t.Fatalf("the login key leaked into the URL: path=%q query=%q", gotPath, gotQuery)
	}

	if gotBody["email"] != "alice@example.com" {
		t.Fatalf("email not in the body: %v", gotBody["email"])
	}
	if gotBody["tenant_id"] != "t1" {
		t.Fatalf("tenant_id not in the body: %v", gotBody["tenant_id"])
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("response email = %q", u.Email)
	}
}

func TestRESTGetUserByEmail_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := newWiredTransport(srv).GetUserByEmail(t.Context(), "t1", "nobody@example.com"); err == nil {
		t.Fatal("expected a not-found error")
	}
}
