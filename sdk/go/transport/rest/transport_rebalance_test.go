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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTRebalance_SendsTheDocumentedRequest pins each rebalance RPC to its
// verb, path and body on the wire, and checks the wire answer is converted:
// moves and node loads copied, and millisecond durations read as durations.
// ExecuteRebalance of a nil plan sends an empty plan rather than failing.
func TestRESTRebalance_SendsTheDocumentedRequest(t *testing.T) {
	wireMove := restRebalanceMove{ShardID: 7, FromNode: "n1", ToNode: "n2", EstimatedBytes: 4096}
	plan := &transport.RebalancePlan{
		Moves:             []transport.RebalanceMove{{ShardID: 7, FromNode: "n1", ToNode: "n2", EstimatedBytes: 4096}},
		EstimatedDuration: 1500 * time.Millisecond,
		CurrentImbalance:  0.4,
	}
	executed := restRebalanceExecution{MovesCompleted: 1, DurationMs: 900, FinalImbalance: 0.1}
	checkExecuted := func(res *transport.RebalanceResult, err error) error {
		if err == nil && (res.MovesCompleted != 1 || res.Duration != 900*time.Millisecond || res.FinalImbalance != 0.1) {
			return errors.New("execution result not converted, want 1 move in 900ms")
		}
		return err
	}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "GetShardStats", verb: http.MethodGet, path: "/v1/rebalance/shards/7",
			answer: restShardStats{ShardID: 7, KeyCount: 10, ByteCount: 4096, LeaderID: 2, ReplicaCount: 3},
			callCheck: func(ctx context.Context, rt *Transport) error {
				st, err := rt.GetShardStats(ctx, 7)
				if err == nil && (st.ShardID != 7 || st.KeyCount != 10 || st.ByteCount != 4096 || st.LeaderID != 2 || st.ReplicaCount != 3) {
					return errors.New("shard stats not converted")
				}
				return err
			}},
		{name: "CreateRebalancePlan", verb: http.MethodPost, path: "/v1/rebalance/plan",
			wantBody: restRebalancePlanRequest{MaxMoves: 4, DryRun: true, TargetBalance: 0.2},
			answer:   restRebalancePlan{Moves: []restRebalanceMove{wireMove}, EstimatedDurationMs: 1500, CurrentImbalance: 0.4},
			callCheck: func(ctx context.Context, rt *Transport) error {
				got, err := rt.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{MaxMoves: 4, DryRun: true, TargetBalance: 0.2})
				if err == nil && (len(got.Moves) != 1 || got.Moves[0] != plan.Moves[0] ||
					got.EstimatedDuration != plan.EstimatedDuration || got.CurrentImbalance != plan.CurrentImbalance) {
					return errors.New("plan not converted, want one move of shard 7 in 1.5s")
				}
				return err
			}},
		{name: "ExecuteRebalance", verb: http.MethodPost, path: "/v1/rebalance/execute",
			wantBody: restRebalancePlan{Moves: []restRebalanceMove{wireMove}, EstimatedDurationMs: 1500, CurrentImbalance: 0.4},
			answer:   executed,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkExecuted(rt.ExecuteRebalance(ctx, plan))
			}},
		{name: "ExecuteRebalance/NilPlan", verb: http.MethodPost, path: "/v1/rebalance/execute",
			wantBody: restRebalancePlan{}, answer: executed,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkExecuted(rt.ExecuteRebalance(ctx, nil))
			}},
		{name: "GetImbalanceReport", verb: http.MethodGet, path: "/v1/rebalance/report",
			answer: restImbalanceReport{Nodes: []restNodeLoad{{NodeID: "n1", Address: "db.example.com:4001", ShardCount: 3,
				TotalKeys: 30, TotalBytes: 9000, LeaderCount: 2}}, ImbalanceScore: 0.5, Hotspots: []string{"n1"}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				rep, err := rt.GetImbalanceReport(ctx)
				want := transport.NodeLoad{NodeID: "n1", Address: "db.example.com:4001", ShardCount: 3, TotalKeys: 30, TotalBytes: 9000, LeaderCount: 2}
				if err == nil && (len(rep.Nodes) != 1 || rep.Nodes[0] != want || rep.ImbalanceScore != 0.5 ||
					len(rep.Hotspots) != 1 || rep.Hotspots[0] != "n1") {
					return errors.New("imbalance report not converted, want node n1 as the hotspot")
				}
				return err
			}},
	})
}

// TestRESTRebalance_SurfacesAFailedAnswer checks every rebalance RPC surfaces
// a refused or undecodable answer as a RequestError naming it.
func TestRESTRebalance_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"GetShardStats", http.MethodGet, "/v1/rebalance/shards/7", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetShardStats(ctx, 7)
			return err
		}},
		{"CreateRebalancePlan", http.MethodPost, "/v1/rebalance/plan", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{})
			return err
		}},
		{"ExecuteRebalance", http.MethodPost, "/v1/rebalance/execute", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ExecuteRebalance(ctx, nil)
			return err
		}},
		{"GetImbalanceReport", http.MethodGet, "/v1/rebalance/report", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetImbalanceReport(ctx)
			return err
		}},
	})
}

// TestRESTRebalance_Closed checks every rebalance RPC refuses on a closed
// transport before anything reaches the wire.
func TestRESTRebalance_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"GetShardStats": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetShardStats(ctx, 7)
			return err
		},
		"CreateRebalancePlan": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{})
			return err
		},
		"ExecuteRebalance": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ExecuteRebalance(ctx, nil)
			return err
		},
		"GetImbalanceReport": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetImbalanceReport(ctx)
			return err
		},
	})
}
