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
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Admin shard / topology request & response types
// ---------------------------------------------------------------------------

// startShardRequest is the JSON payload for POST /admin/shards. The wire
// format mirrors the SDK transport.StartShardRequest contract.
type startShardRequest struct {
	ShardID   uint64            `json:"shard_id"`
	ReplicaID uint64            `json:"replica_id"`
	Members   map[uint64]string `json:"members,omitempty"`
	Join      bool              `json:"join"`
}

// shardStateResponse mirrors the SDK transport.ShardStateResponse contract.
type shardStateResponse struct {
	ShardID           uint64 `json:"shard_id"`
	ConfigChangeIndex uint64 `json:"config_change_index"`
	Term              uint64 `json:"term"`
	LeaderID          uint64 `json:"leader_id"`
	HasLeader         bool   `json:"has_leader"`
}

// topologyResponse mirrors the SDK transport.TopologyResponse contract.
type topologyResponse struct {
	NodeID      string              `json:"node_id"`
	RaftAddress string              `json:"raft_address"`
	ShardCount  int                 `json:"shard_count"`
	Shards      []shardInfoResponse `json:"shards"`
}

// shardInfoResponse mirrors the SDK transport.ShardInfo contract.
type shardInfoResponse struct {
	ShardID   uint64            `json:"shard_id"`
	ReplicaID uint64            `json:"replica_id"`
	IsLeader  bool              `json:"is_leader"`
	LeaderID  uint64            `json:"leader_id"`
	Nodes     map[uint64]string `json:"nodes"`
}

// ---------------------------------------------------------------------------
// Admin shard / topology handlers
// ---------------------------------------------------------------------------

// handleStartShard initializes and starts a Raft group for the given shard.
// Maps to POST /admin/shards.
func (s *Server) handleStartShard(w http.ResponseWriter, r *http.Request) {
	var req startShardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.adminSvc.StartShard(ctx, &service.StartShardRequest{
		ShardID:   req.ShardID,
		ReplicaID: req.ReplicaID,
		Members:   req.Members,
		Join:      req.Join,
	}); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleGetTopology returns the full cluster topology as seen from this node.
// Maps to GET /admin/topology.
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	topo, err := s.adminSvc.GetTopology(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	shards := make([]shardInfoResponse, 0, len(topo.Shards))
	for _, si := range topo.Shards {
		shards = append(shards, shardInfoResponse{
			ShardID:   si.ShardID,
			ReplicaID: si.ReplicaID,
			IsLeader:  si.IsLeader,
			LeaderID:  si.LeaderID,
			Nodes:     si.Nodes,
		})
	}

	writeJSON(w, topologyResponse{
		NodeID:      topo.NodeID,
		RaftAddress: topo.RaftAddress,
		ShardCount:  topo.ShardCount,
		Shards:      shards,
	})
}

// handleAdminShardByID dispatches the per-shard routes carried under the
// /admin/shards/ prefix:
//
//	DELETE /admin/shards/{id}        -> stop the shard's Raft group
//	GET    /admin/shards/{id}/state  -> report the shard's Raft state
func (s *Server) handleAdminShardByID(w http.ResponseWriter, r *http.Request) {
	if s.adminSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "admin"})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/admin/shards/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "shard_id"})
		return
	}

	// GET /admin/shards/{id}/state.
	if idStr, found := strings.CutSuffix(rest, "/state"); found {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetShardState(w, r, idStr)
		return
	}

	// DELETE /admin/shards/{id}.
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleStopShard(w, r, rest)
}

// handleStopShard stops the Raft group for the shard identified by idStr.
func (s *Server) handleStopShard(w http.ResponseWriter, r *http.Request, idStr string) {
	shardID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.adminSvc.StopShard(ctx, shardID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleGetShardState returns the Raft state for the shard identified by idStr.
func (s *Server) handleGetShardState(w http.ResponseWriter, r *http.Request, idStr string) {
	shardID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	state, err := s.adminSvc.GetShardState(ctx, shardID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shardStateResponse{
		ShardID:           state.ShardID,
		ConfigChangeIndex: state.ConfigChangeIndex,
		Term:              state.Term,
		LeaderID:          state.LeaderID,
		HasLeader:         state.HasLeader,
	})
}
