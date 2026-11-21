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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestRebalancePlanToResponse_CarriesSkippedMoves is the Codex finding on
// #1517: the planner's refused moves and their reasons reach the wire.
func TestRebalancePlanToResponse_CarriesSkippedMoves(t *testing.T) {
	resp := rebalancePlanToResponse(&service.RebalancePlanResult{
		Skipped: []service.RebalanceSkippedMove{
			{ShardID: 7, FromNode: "a", ToNode: "b", Reason: "serving set"},
			{ShardID: 9, FromNode: "a", ToNode: "c", Reason: "home group", Err: errors.New("never moves")},
		},
	})
	if len(resp.Skipped) != 2 {
		t.Fatalf("skipped = %+v, want 2 entries", resp.Skipped)
	}
	if s := resp.Skipped[0]; s.ShardID != 7 || s.FromNode != "a" || s.ToNode != "b" || s.Reason != "serving set" || s.Error != "" {
		t.Fatalf("skipped[0] = %+v", s)
	}
	if s := resp.Skipped[1]; s.Reason != "home group" || s.Error != "never moves" {
		t.Fatalf("skipped[1] = %+v", s)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"skipped":[{"shard_id":7`) || !strings.Contains(string(body), `"error":"never moves"`) {
		t.Fatalf("wire body lacks the skipped moves: %s", body)
	}
}
