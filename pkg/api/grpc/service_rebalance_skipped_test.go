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

package grpc

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestRebalancePlanToProto_CarriesSkippedMoves is the Codex finding on
// #1517: the planner's refused moves and their reasons reach the wire.
func TestRebalancePlanToProto_CarriesSkippedMoves(t *testing.T) {
	out := rebalancePlanToProto(&service.RebalancePlanResult{
		Skipped: []service.RebalanceSkippedMove{
			{ShardID: 7, FromNode: "a", ToNode: "b", Reason: "serving set"},
			{ShardID: 9, FromNode: "a", ToNode: "c", Reason: "home group", Err: errors.New("never moves")},
		},
	})
	got := out.GetSkipped()
	if len(got) != 2 {
		t.Fatalf("skipped = %v, want 2 entries", got)
	}
	if got[0].GetShardId() != 7 || got[0].GetFromNode() != "a" || got[0].GetToNode() != "b" ||
		got[0].GetReason() != "serving set" || got[0].GetError() != "" {
		t.Fatalf("skipped[0] = %v", got[0])
	}
	if got[1].GetReason() != "home group" || got[1].GetError() != "never moves" {
		t.Fatalf("skipped[1] = %v", got[1])
	}
	if s := rebalancePlanToProto(&service.RebalancePlanResult{}).GetSkipped(); s != nil {
		t.Fatalf("skipped of a plan with none = %v, want nil", s)
	}
}
