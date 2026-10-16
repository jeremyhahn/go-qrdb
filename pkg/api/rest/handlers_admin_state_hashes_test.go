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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stateHashAdmin is an admin double implementing service.ReplicaStateHasher,
// recording the shard IDs it was asked for.
type stateHashAdmin struct {
	mockAdminService
	called    bool
	gotShards []uint64
	resp      *service.StateHashesResponse
	err       error
}

var _ service.ReplicaStateHasher = (*stateHashAdmin)(nil)

func (a *stateHashAdmin) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	a.called = true
	a.gotShards = shardIDs
	return a.resp, a.err
}

func serveStateHashes(t *testing.T, admin service.AdminServicer, query string) *httptest.ResponseRecorder {
	t.Helper()
	srv := NewServer(&mockKVService{}, admin, "127.0.0.1:0")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/admin/state-hashes"+query, nil))
	return rec
}

func TestHandler_StateHashes_RepeatedShardParameter(t *testing.T) {
	admin := &stateHashAdmin{resp: &service.StateHashesResponse{
		NodeID:   "node-a",
		Replicas: []service.ReplicaStateHash{{ShardID: 1, ReplicaID: 2, AppliedIndex: 8, Records: 4, Hash: "ef"}},
	}}
	rec := serveStateHashes(t, admin, "?shard=1&shard=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !slices.Equal(admin.gotShards, []uint64{1, 7}) {
		t.Fatalf("service asked for %v, want [1 7]", admin.gotShards)
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["node_id"] != "node-a" {
		t.Fatalf("node_id = %v", out["node_id"])
	}
	replicas, _ := out["replicas"].([]any)
	if len(replicas) != 1 || replicas[0].(map[string]any)["applied_index"] != float64(8) {
		t.Fatalf("replicas = %v, want one entry with snake_case applied_index 8", out["replicas"])
	}
}

func TestHandler_StateHashes_NoShardSelectsAll(t *testing.T) {
	admin := &stateHashAdmin{resp: &service.StateHashesResponse{NodeID: "n"}}
	if rec := serveStateHashes(t, admin, ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if admin.gotShards != nil {
		t.Fatalf("service asked for %v, want nil (every shard)", admin.gotShards)
	}
}

func TestHandler_StateHashes_Refusals(t *testing.T) {
	malformed := &stateHashAdmin{}
	if rec := serveStateHashes(t, malformed, "?shard=x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed shard: status = %d, want 400", rec.Code)
	}
	if malformed.called {
		t.Fatal("a malformed shard parameter reached the service")
	}
	denied := &stateHashAdmin{err: &tenant.CrossTenantDeniedError{}}
	if rec := serveStateHashes(t, denied, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("service refusal: status = %d, want 403", rec.Code)
	}
	if rec := serveStateHashes(t, &mockAdminService{}, ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("admin without the capability: status = %d, want 501", rec.Code)
	}
}

func TestParseShardParams(t *testing.T) {
	if ids, err := parseShardParams(nil); ids != nil || err != nil {
		t.Fatalf("no values = (%v, %v), want (nil, nil)", ids, err)
	}
	if ids, err := parseShardParams([]string{"3", "18446744073709551615"}); err != nil || !slices.Equal(ids, []uint64{3, 1<<64 - 1}) {
		t.Fatalf("(%v, %v)", ids, err)
	}
	if _, err := parseShardParams([]string{"1", "-1"}); err == nil || err.Param != "shard" {
		t.Fatalf("negative shard: err = %v, want an InvalidParameterError on shard", err)
	}
}
