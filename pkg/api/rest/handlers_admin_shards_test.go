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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// POST /admin/shards (StartShard)
// ---------------------------------------------------------------------------

func TestHandler_StartShard_Success(t *testing.T) {
	var got *service.StartShardRequest
	adminSvc := &mockAdminService{
		startShardFn: func(_ context.Context, req *service.StartShardRequest) error {
			got = req
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := strings.NewReader(`{"shard_id":7,"replica_id":2,"members":{"2":"127.0.0.1:7000"},"join":false}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/shards", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got == nil || got.ShardID != 7 || got.ReplicaID != 2 || got.Members[2] != "127.0.0.1:7000" {
		t.Fatalf("service got = %+v", got)
	}
}

func TestHandler_StartShard_BadBody(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/admin/shards", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_StartShard_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		startShardFn: func(_ context.Context, _ *service.StartShardRequest) error {
			return errors.New("start failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/admin/shards", strings.NewReader(`{"shard_id":1}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// GET /admin/topology (GetTopology)
// ---------------------------------------------------------------------------

func TestHandler_GetTopology_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		getTopologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return &service.TopologyResponse{
				NodeID:      "node-1",
				RaftAddress: "127.0.0.1:7000",
				ShardCount:  1,
				Shards: []service.TopologyShardInfo{
					{ShardID: 1, ReplicaID: 1, IsLeader: true, LeaderID: 1, Nodes: map[uint64]string{1: "127.0.0.1:7000"}},
				},
			}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/admin/topology", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp topologyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NodeID != "node-1" || resp.ShardCount != 1 || len(resp.Shards) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if !resp.Shards[0].IsLeader || resp.Shards[0].Nodes[1] != "127.0.0.1:7000" {
		t.Fatalf("shard = %+v", resp.Shards[0])
	}
}

func TestHandler_GetTopology_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		getTopologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return nil, errors.New("topo failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/admin/topology", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// GET /admin/shards/{id}/state (GetShardState)
// ---------------------------------------------------------------------------

func TestHandler_GetShardState_Success(t *testing.T) {
	adminSvc := &mockAdminService{
		getShardStateFn: func(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
			return &service.ShardStateResponse{
				ShardID: shardID, ConfigChangeIndex: 4, Term: 9, LeaderID: 1, HasLeader: true,
			}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/admin/shards/42/state", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp shardStateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ShardID != 42 || resp.Term != 9 || resp.LeaderID != 1 || !resp.HasLeader {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHandler_GetShardState_InvalidID(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/admin/shards/notanum/state", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetShardState_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		getShardStateFn: func(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
			return nil, errors.New("state failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/admin/shards/1/state", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_GetShardState_WrongMethod(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/admin/shards/1/state", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// DELETE /admin/shards/{id} (StopShard)
// ---------------------------------------------------------------------------

func TestHandler_StopShard_Success(t *testing.T) {
	var gotID uint64
	adminSvc := &mockAdminService{
		stopShardFn: func(_ context.Context, shardID uint64) error {
			gotID = shardID
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/admin/shards/13", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotID != 13 {
		t.Fatalf("stopped shard = %d, want 13", gotID)
	}
}

func TestHandler_StopShard_InvalidID(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodDelete, "/admin/shards/xyz", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_StopShard_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		stopShardFn: func(_ context.Context, _ uint64) error {
			return errors.New("stop failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodDelete, "/admin/shards/1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_AdminShardByID_WrongMethod(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	// PUT on /admin/shards/{id} (non-state) is not allowed.
	req := httptest.NewRequest(http.MethodPut, "/admin/shards/1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
