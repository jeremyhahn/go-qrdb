package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// skippingRebalanceSvc plans one move and refuses another, as the planner
// does for an isolated tenant whose serving set excludes the target.
type skippingRebalanceSvc struct {
	*minimalRebalanceSvc
}

func (s skippingRebalanceSvc) CreateRebalancePlan(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512}},
		Skipped: []service.RebalanceSkippedMove{{
			ShardID:  7,
			FromNode: "n1",
			ToNode:   "n3",
			Reason:   "serving_set",
			Err:      errors.New("target is outside the tenant's serving set"),
		}},
	}, nil
}

// TestMCP_Rebalance_CreateRebalancePlan_CarriesSkippedMoves: the plan an
// MCP client receives names each refused move and its reason, as the other
// transports' plans do.
func TestMCP_Rebalance_CreateRebalancePlan_CarriesSkippedMoves(t *testing.T) {
	tr, cleanup := connectRebalanceInProcess(t, skippingRebalanceSvc{newMinimalRebalanceSvc()})
	defer cleanup()

	plan, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{MaxMoves: 5})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	want := []transport.RebalanceSkippedMove{{ShardID: 7, FromNode: "n1", ToNode: "n3", Reason: "serving_set"}}
	if !reflect.DeepEqual(plan.Skipped, want) {
		t.Fatalf("plan.Skipped = %+v, want %+v", plan.Skipped, want)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].ShardID != 1 {
		t.Fatalf("plan.Moves = %+v, want the one planned move", plan.Moves)
	}
}

// TestMCP_PlanToMCP_RoundTripsSkippedMoves: a plan sent back for execution
// keeps its skipped entries on the MCP wire form.
func TestMCP_PlanToMCP_RoundTripsSkippedMoves(t *testing.T) {
	in := &transport.RebalancePlan{
		Moves:   []transport.RebalanceMove{{ShardID: 1, FromNode: "n1", ToNode: "n2"}},
		Skipped: []transport.RebalanceSkippedMove{{ShardID: 7, FromNode: "n1", ToNode: "n3", Reason: "home_group"}},
	}
	wire := planToMCP(in)
	out := wire.toRebalancePlan()
	if !reflect.DeepEqual(out.Skipped, in.Skipped) {
		t.Fatalf("round-tripped Skipped = %+v, want %+v", out.Skipped, in.Skipped)
	}
	if empty := planToMCP(&transport.RebalancePlan{}); empty.Skipped != nil {
		t.Fatalf("planToMCP of a plan with no skipped moves = %+v, want nil Skipped", empty.Skipped)
	}
}
