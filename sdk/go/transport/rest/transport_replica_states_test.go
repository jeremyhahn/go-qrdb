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
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// replicaStatesServer answers GET /admin/replica-states with body and status,
// recording the raw query it was asked. It answers /health so the transport
// can connect.
func replicaStatesServer(t *testing.T, status int, body string, gotQuery *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/admin/replica-states" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
			return
		}
		*gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReplicaStates(t *testing.T) {
	var query string
	srv := replicaStatesServer(t, http.StatusOK,
		`{"node_id":"node-2","replicas":[{"shard_id":1,"replica_id":2,"applied_index":48211,"hash":9223372036854775813}]}`, &query)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.ReplicaStates(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if query != "shard_id=1" {
		t.Fatalf("query = %q, want shard_id=1", query)
	}
	want := &transport.ReplicaStatesResponse{NodeID: "node-2", Replicas: []transport.ReplicaState{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v; the full 64-bit hash must survive the JSON", resp, want)
	}
}

func TestReplicaStates_NoShardSendsNoQuery(t *testing.T) {
	query := "unset"
	srv := replicaStatesServer(t, http.StatusOK, `{"node_id":"node-2"}`, &query)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if query != "" {
		t.Fatalf("query = %q, want none so every shard is read", query)
	}
	if resp.Replicas == nil || len(resp.Replicas) != 0 {
		t.Fatalf("Replicas = %#v, want empty and non-nil", resp.Replicas)
	}
}

func TestReplicaStates_NotImplementedIsCapabilityUnsupported(t *testing.T) {
	var query string
	srv := replicaStatesServer(t, http.StatusNotImplemented, `{"error":"replica state read is not supported"}`, &query)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.ReplicaStates(context.Background(), 0)
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestReplicaStates_NotFoundCarriesStatus(t *testing.T) {
	var query string
	srv := replicaStatesServer(t, http.StatusNotFound, `{"error":"shard 9 has no replica on this node"}`, &query)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.ReplicaStates(context.Background(), 9)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound || reqErr.Method != "ReplicaStates" {
		t.Fatalf("err = %v (%T), want a 404 *transport.RequestError for ReplicaStates", err, err)
	}
}

func TestReplicaStates_JSONDecodeError(t *testing.T) {
	var query string
	srv := replicaStatesServer(t, http.StatusOK, `{not json`, &query)
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.ReplicaStates(context.Background(), 0)
	var decodeErr *jsonDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("err = %v (%T), want *jsonDecodeError", err, err)
	}
}

func TestReplicaStates_OnClosedTransport(t *testing.T) {
	var query string
	srv := replicaStatesServer(t, http.StatusOK, `{}`, &query)
	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.ReplicaStates(context.Background(), 0)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
