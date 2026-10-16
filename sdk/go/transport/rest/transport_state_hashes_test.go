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
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stateHashServer serves /admin/state-hashes the way the REST server does,
// recording every request's shard parameters. status, when not 200, makes
// every call fail with it; body replaces the encoded response.
func stateHashServer(t *testing.T, status int, body string) (*httptest.Server, *[][]string) {
	t.Helper()
	var (
		mu     sync.Mutex
		shards [][]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if r.URL.Path != "/admin/state-hashes" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		shards = append(shards, r.URL.Query()["shard"])
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"refused"}`))
			return
		}
		if body != "" {
			_, _ = w.Write([]byte(body))
			return
		}
		_ = json.NewEncoder(w).Encode(transport.StateHashesResponse{
			NodeID: "node-a",
			Replicas: []transport.ReplicaStateHash{
				{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &shards
}

func TestStateHashes_RepeatedShardParameter(t *testing.T) {
	srv, shards := stateHashServer(t, http.StatusOK, "")
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()
	ctx := context.Background()

	got, err := rt.StateHashes(ctx, []uint64{3, 18446744073709551615})
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	want := transport.ReplicaStateHash{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"}
	if got.NodeID != "node-a" || len(got.Replicas) != 1 || got.Replicas[0] != want {
		t.Fatalf("StateHashes = %+v, want node-a [%+v]", got, want)
	}
	if _, err := rt.StateHashes(ctx, nil); err != nil {
		t.Fatalf("StateHashes(nil): %v", err)
	}
	if len(*shards) != 2 || !slices.Equal((*shards)[0], []string{"3", "18446744073709551615"}) || (*shards)[1] != nil {
		t.Fatalf("shard parameters sent = %q, want [[3 18446744073709551615] []]", *shards)
	}
}

func TestStateHashes_EmptyReplicasAreNotNil(t *testing.T) {
	srv, _ := stateHashServer(t, http.StatusOK, `{"node_id":"node-b"}`)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	got, err := rt.StateHashes(context.Background(), nil)
	if err != nil || got.NodeID != "node-b" || got.Replicas == nil || len(got.Replicas) != 0 {
		t.Fatalf("StateHashes = %+v, %v; want node-b with an empty replica list", got, err)
	}
}

func TestStateHashes_Failures(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusForbidden, http.StatusNotImplemented} {
		srv, _ := stateHashServer(t, status, "")
		rt := newTestTransport(t, srv.URL)
		if _, err := rt.StateHashes(ctx, nil); err == nil {
			t.Fatalf("StateHashes answered %d succeeded", status)
		}
		_ = rt.Close()
	}

	srv, _ := stateHashServer(t, http.StatusOK, "{not json")
	rt := newTestTransport(t, srv.URL)
	if _, err := rt.StateHashes(ctx, nil); err == nil {
		t.Fatal("an undecodable answer was accepted")
	}
	_ = rt.Close()
	var closed *transport.ClosedError
	if _, err := rt.StateHashes(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("StateHashes on a closed transport = %v, want *transport.ClosedError", err)
	}
}
