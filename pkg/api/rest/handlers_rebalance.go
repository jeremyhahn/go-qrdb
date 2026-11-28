// Copyright 2026 Jeremy Hahn
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
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// rebalanceRequestTimeout bounds rebalance read/plan endpoints. Plan execution
// can move shard data, so it gets the wider backupRequestTimeout instead.
const rebalanceRequestTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// Rebalance request/response types
// ---------------------------------------------------------------------------

// rebalancePlanRequest is the JSON payload for POST /v1/rebalance/plan.
type rebalancePlanRequest struct {
	MaxMoves      int     `json:"max_moves,omitempty"`
	DryRun        bool    `json:"dry_run,omitempty"`
	TargetBalance float64 `json:"target_balance,omitempty"`
}

// shardStatsResponse is the JSON representation of a single shard's statistics.
type shardStatsResponse struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}

// rebalanceMoveResponse is the JSON representation of a single planned move.
type rebalanceMoveResponse struct {
	ShardID        uint64 `json:"shard_id"`
	FromNode       string `json:"from_node"`
	ToNode         string `json:"to_node"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
}

// rebalancePlanResponse is the JSON representation of a rebalance plan. It is
// both the response of POST /v1/rebalance/plan and the request body of
// POST /v1/rebalance/execute.
type rebalancePlanResponse struct {
	Moves               []rebalanceMoveResponse `json:"moves"`
	EstimatedDurationMs int64                   `json:"estimated_duration_ms"`
	CurrentImbalance    float64                 `json:"current_imbalance"`
	// Skipped names the moves the planner refused, each with its reason.
	Skipped []rebalanceSkippedMoveResponse `json:"skipped,omitempty"`
}

// rebalanceSkippedMoveResponse is a move the planner considered and
// refused, with the reason.
type rebalanceSkippedMoveResponse struct {
	ShardID  uint64 `json:"shard_id"`
	FromNode string `json:"from_node"`
	ToNode   string `json:"to_node"`
	Reason   string `json:"reason"`
	Error    string `json:"error,omitempty"`
}

// rebalanceExecutionResponse is the JSON payload returned by
// POST /v1/rebalance/execute.
type rebalanceExecutionResponse struct {
	MovesCompleted int                            `json:"moves_completed"`
	MovesFailed    int                            `json:"moves_failed"`
	Failures       []rebalanceMoveFailureResponse `json:"failures,omitempty"`
	DurationMs     int64                          `json:"duration_ms"`
	FinalImbalance float64                        `json:"final_imbalance"`
}

// rebalanceMoveFailureResponse names a move that failed and why.
type rebalanceMoveFailureResponse struct {
	ShardID  uint64 `json:"shard_id"`
	FromNode string `json:"from_node"`
	ToNode   string `json:"to_node"`
	Step     string `json:"step,omitempty"`
	Error    string `json:"error"`
}

// rebalanceFailuresToResponse converts the service's per-move failures to
// their JSON form; no failures convert to nil.
func rebalanceFailuresToResponse(failures []service.RebalanceMoveFailure) []rebalanceMoveFailureResponse {
	if len(failures) == 0 {
		return nil
	}
	out := make([]rebalanceMoveFailureResponse, len(failures))
	for i, f := range failures {
		out[i] = rebalanceMoveFailureResponse{
			ShardID:  f.ShardID,
			FromNode: f.FromNode,
			ToNode:   f.ToNode,
			Step:     f.Step,
			Error:    f.Error,
		}
	}
	return out
}

// nodeLoadResponse is the JSON representation of a single node's load.
type nodeLoadResponse struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	ShardCount  int    `json:"shard_count"`
	TotalKeys   uint64 `json:"total_keys"`
	TotalBytes  uint64 `json:"total_bytes"`
	LeaderCount int    `json:"leader_count"`
}

// imbalanceReportResponse is the JSON payload returned by GET /v1/rebalance/report.
// Hotspots has NO omitempty: it is a collection the client iterates
// unconditionally (RebalancePanel reads report.hotspots.length), so it must
// always serialize as a JSON array — never be omitted (a balanced cluster has
// zero hotspots, the common case) and never be null. The build site below
// guarantees a non-nil slice so the wire value is [] rather than null.
type imbalanceReportResponse struct {
	Nodes          []nodeLoadResponse `json:"nodes"`
	ImbalanceScore float64            `json:"imbalance_score"`
	Hotspots       []string           `json:"hotspots"`
}

// ---------------------------------------------------------------------------
// Rebalance handlers
// ---------------------------------------------------------------------------

// handleRebalanceShardStats handles GET /v1/rebalance/shards/{id} -- returns
// statistics for a single shard.
func (s *Server) handleRebalanceShardStats(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rebalance"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/v1/rebalance/shards/")
	shardID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "id", Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), rebalanceRequestTimeout)
	defer cancel()
	stats, err := s.rebalanceSvc.GetShardStats(ctx, shardID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shardStatsResponse{
		ShardID:      stats.ShardID,
		KeyCount:     stats.KeyCount,
		ByteCount:    stats.ByteCount,
		LeaderID:     stats.LeaderID,
		ReplicaCount: stats.ReplicaCount,
	})
}

// handleRebalancePlan handles POST /v1/rebalance/plan -- generates a move plan.
func (s *Server) handleRebalancePlan(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rebalance"})
		return
	}

	var req rebalancePlanRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), rebalanceRequestTimeout)
	defer cancel()
	plan, err := s.rebalanceSvc.CreateRebalancePlan(ctx, &service.RebalancePlanOptions{
		MaxMoves:      req.MaxMoves,
		DryRun:        req.DryRun,
		TargetBalance: req.TargetBalance,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, rebalancePlanToResponse(plan))
}

// handleRebalanceExecute handles POST /v1/rebalance/execute -- executes a
// plan. A dedicated-tenant shard MOVE within the plan must run on that
// group's current Raft leader; when the local node is not it,
// RebalanceService.ExecuteRebalance returns *service.NotLeaderError and this
// handler transparently proxies the whole request to the leader (see
// tryForwardWrite), mirroring the KV PUT/DELETE and database-scale handlers.
// The body is buffered up front (rather than decoded directly off r.Body) so
// the original bytes remain available for forwarding.
func (s *Server) handleRebalanceExecute(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rebalance"})
		return
	}

	body, ok := readBoundedBody(w, r, maxRebalancePlanRequestBytes)
	if !ok {
		return
	}

	var req rebalancePlanResponse
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupRequestTimeout)
	defer cancel()
	result, err := s.rebalanceSvc.ExecuteRebalance(ctx, responseToRebalancePlan(&req))
	if err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, rebalanceExecutionResponse{
		MovesCompleted: result.MovesCompleted,
		MovesFailed:    result.MovesFailed,
		Failures:       rebalanceFailuresToResponse(result.Failures),
		DurationMs:     result.Duration.Milliseconds(),
		FinalImbalance: result.FinalImbalance,
	})
}

// handleImbalanceReport handles GET /v1/rebalance/report -- returns the
// cluster's current load distribution.
func (s *Server) handleImbalanceReport(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rebalance"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), rebalanceRequestTimeout)
	defer cancel()
	report, err := s.rebalanceSvc.GetImbalanceReport(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	nodes := make([]nodeLoadResponse, len(report.Nodes))
	for i := range report.Nodes {
		nodes[i] = nodeLoadResponse{
			NodeID:      report.Nodes[i].NodeID,
			Address:     report.Nodes[i].Address,
			ShardCount:  report.Nodes[i].ShardCount,
			TotalKeys:   report.Nodes[i].TotalKeys,
			TotalBytes:  report.Nodes[i].TotalBytes,
			LeaderCount: report.Nodes[i].LeaderCount,
		}
	}
	// Guarantee a non-nil slice so the "hotspots" field serializes as [] (not
	// null, and — with the omitempty removed above — never omitted) when the
	// cluster is balanced, so clients can iterate report.hotspots safely.
	hotspots := report.Hotspots
	if hotspots == nil {
		hotspots = []string{}
	}
	writeJSON(w, imbalanceReportResponse{
		Nodes:          nodes,
		ImbalanceScore: report.ImbalanceScore,
		Hotspots:       hotspots,
	})
}

// ---------------------------------------------------------------------------
// Rebalance conversion helpers
// ---------------------------------------------------------------------------

// rebalancePlanToResponse converts a service.RebalancePlanResult to its JSON
// response.
func rebalancePlanToResponse(plan *service.RebalancePlanResult) rebalancePlanResponse {
	if plan == nil {
		return rebalancePlanResponse{}
	}
	moves := make([]rebalanceMoveResponse, len(plan.Moves))
	for i := range plan.Moves {
		moves[i] = rebalanceMoveResponse{
			ShardID:        plan.Moves[i].ShardID,
			FromNode:       plan.Moves[i].FromNode,
			ToNode:         plan.Moves[i].ToNode,
			EstimatedBytes: plan.Moves[i].EstimatedBytes,
		}
	}
	return rebalancePlanResponse{
		Moves:               moves,
		EstimatedDurationMs: plan.EstimatedDuration.Milliseconds(),
		CurrentImbalance:    plan.CurrentImbalance,
		Skipped:             rebalanceSkippedToResponse(plan.Skipped),
	}
}

// rebalanceSkippedToResponse carries the planner's refused moves and their
// reasons into the response, so an operator sees why a plan is short.
func rebalanceSkippedToResponse(skipped []service.RebalanceSkippedMove) []rebalanceSkippedMoveResponse {
	if len(skipped) == 0 {
		return nil
	}
	out := make([]rebalanceSkippedMoveResponse, len(skipped))
	for i, s := range skipped {
		out[i] = rebalanceSkippedMoveResponse{ShardID: s.ShardID, FromNode: s.FromNode, ToNode: s.ToNode, Reason: s.Reason}
		if s.Err != nil {
			out[i].Error = s.Err.Error()
		}
	}
	return out
}

// responseToRebalancePlan converts a JSON rebalance plan back to its
// service-layer representation for execution.
func responseToRebalancePlan(resp *rebalancePlanResponse) *service.RebalancePlanResult {
	if resp == nil {
		return nil
	}
	moves := make([]service.RebalanceMovePlan, len(resp.Moves))
	for i := range resp.Moves {
		moves[i] = service.RebalanceMovePlan{
			ShardID:        resp.Moves[i].ShardID,
			FromNode:       resp.Moves[i].FromNode,
			ToNode:         resp.Moves[i].ToNode,
			EstimatedBytes: resp.Moves[i].EstimatedBytes,
		}
	}
	return &service.RebalancePlanResult{
		Moves:             moves,
		EstimatedDuration: time.Duration(resp.EstimatedDurationMs) * time.Millisecond,
		CurrentImbalance:  resp.CurrentImbalance,
	}
}
