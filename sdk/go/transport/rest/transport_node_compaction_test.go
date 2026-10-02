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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionServer serves /admin/node-compaction the way the REST server
// does: GET answers the policy, PUT replaces it (an absent compaction is off)
// and answers the policy now in force. refuse makes every call a 403.
func nodeCompactionServer(t *testing.T, refuse bool) (*httptest.Server, *[]string) {
	t.Helper()
	var (
		mu      sync.Mutex
		policy  *transport.DatabaseCompactionConfig
		methods []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if r.URL.Path != "/admin/node-compaction" {
			http.NotFound(w, r)
			return
		}
		methods = append(methods, r.Method)
		if refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"permission denied"}`))
			return
		}
		if r.Method == http.MethodPut {
			var body nodeCompactionBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			policy = body.Compaction
			if policy != nil && (policy.Mode == "" || policy.Mode == "off") {
				policy = nil
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodeCompactionBody{Compaction: policy})
	}))
	t.Cleanup(srv.Close)
	return srv, &methods
}

func TestNodeCompaction_SetReadAndClear(t *testing.T) {
	srv, methods := nodeCompactionServer(t, false)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()
	ctx := context.Background()

	if got, err := rt.NodeCompaction(ctx); err != nil || got != nil {
		t.Fatalf("NodeCompaction before any policy = %+v, %v; want none", got, err)
	}
	set, err := rt.SetNodeCompaction(ctx, &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 3})
	if err != nil || set == nil || set.RetainRevisions != 3 {
		t.Fatalf("SetNodeCompaction = %+v, %v; want the revision policy", set, err)
	}
	if got, err := rt.NodeCompaction(ctx); err != nil || got == nil || got.Mode != "revision" {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
	if off, err := rt.SetNodeCompaction(ctx, nil); err != nil || off != nil {
		t.Fatalf("SetNodeCompaction(nil) = %+v, %v; want off", off, err)
	}
	want := []string{http.MethodGet, http.MethodPut, http.MethodGet, http.MethodPut}
	if len(*methods) != len(want) {
		t.Fatalf("methods = %v, want %v", *methods, want)
	}
	for i := range want {
		if (*methods)[i] != want[i] {
			t.Fatalf("methods = %v, want %v", *methods, want)
		}
	}
}

func TestNodeCompaction_Failures(t *testing.T) {
	srv, _ := nodeCompactionServer(t, true)
	rt := newTestTransport(t, srv.URL)
	ctx := context.Background()
	if _, err := rt.NodeCompaction(ctx); err == nil {
		t.Fatal("a refused NodeCompaction succeeded")
	}
	if _, err := rt.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("a refused SetNodeCompaction succeeded")
	}
	_ = rt.Close()
	if _, err := rt.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a closed transport succeeded")
	}
	if _, err := rt.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a closed transport succeeded")
	}
}
