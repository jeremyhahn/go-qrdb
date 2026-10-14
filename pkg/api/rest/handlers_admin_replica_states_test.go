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
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// replicaStateAdmin is a mockAdminService that can read replica states, as
// the real AdminService can.
type replicaStateAdmin struct {
	*mockAdminService
	replicaStatesFn func(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error)
}

func (a *replicaStateAdmin) ReplicaStates(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
	return a.replicaStatesFn(ctx, shardID)
}

var _ service.ReplicaStateReader = (*replicaStateAdmin)(nil)

func getReplicaStates(t *testing.T, admin service.AdminServicer, target string) *httptest.ResponseRecorder {
	t.Helper()
	srv := NewServer(&mockKVService{}, admin, "127.0.0.1:0")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandler_GetReplicaStates_AllReplicas(t *testing.T) {
	var gotShard uint64 = 99
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-2", Replicas: []service.ReplicaState{
				{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 11},
				{ShardID: 7, ReplicaID: 3, AppliedIndex: 9, Hash: 22},
			}}, nil
		}}
	rec := getReplicaStates(t, admin, "/admin/replica-states")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotShard != 0 {
		t.Fatalf("shard passed to the service = %d, want 0 (every shard)", gotShard)
	}
	var resp replicaStatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := replicaStatesResponse{NodeID: "node-2", Replicas: []replicaStateResponse{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 11},
		{ShardID: 7, ReplicaID: 3, AppliedIndex: 9, Hash: 22},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("resp = %+v, want %+v", resp, want)
	}
}

func TestHandler_GetReplicaStates_FilterPassedThrough(t *testing.T) {
	var gotShard uint64
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-1", Replicas: []service.ReplicaState{}}, nil
		}}
	rec := getReplicaStates(t, admin, "/admin/replica-states?shard_id=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotShard != 7 {
		t.Fatalf("shard passed to the service = %d, want 7", gotShard)
	}
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, `"replicas":[]`) {
		t.Fatalf("body = %s, want an empty replicas array rather than null", body)
	}
}

func TestHandler_GetReplicaStates_BadShardID(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			t.Fatal("the service was called with an unparsable shard_id")
			return nil, nil
		}}
	for _, raw := range []string{"abc", "-1"} {
		rec := getReplicaStates(t, admin, "/admin/replica-states?shard_id="+raw)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("shard_id=%s: status = %d, want 400", raw, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "shard_id") {
			t.Fatalf("shard_id=%s: body = %s, want it to name the parameter", raw, rec.Body.String())
		}
	}
}

func TestHandler_GetReplicaStates_ServiceErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		code int
	}{
		"not hosted":   {&service.ReplicaNotHostedError{ShardID: 9}, http.StatusNotFound},
		"standalone":   {&service.ReplicaStateUnsupportedError{}, http.StatusNotImplemented},
		"unhashable":   {&service.StateMachineHashUnsupportedError{ShardID: 6}, http.StatusNotImplemented},
		"host refused": {&service.ReplicaStateError{ShardID: 2, Err: context.DeadlineExceeded}, http.StatusGatewayTimeout},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
				replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
					return nil, tc.err
				}}
			rec := getReplicaStates(t, admin, "/admin/replica-states?shard_id=9")
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestHandler_GetReplicaStates_AdminServiceCannotRead(t *testing.T) {
	rec := getReplicaStates(t, &mockAdminService{}, "/admin/replica-states")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "replica states") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
