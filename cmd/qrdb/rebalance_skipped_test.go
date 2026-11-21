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

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestRebalancePlanCmd_PrintsSkippedMoves is the Codex finding on #1517: a
// plan the planner shortened must say which moves it refused and why.
func TestRebalancePlanCmd_PrintsSkippedMoves(t *testing.T) {
	svc := &cliMockRebalanceService{
		createRebalancePlanFn: func(context.Context, *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return &service.RebalancePlanResult{
				CurrentImbalance: 0.4,
				Skipped: []service.RebalanceSkippedMove{
					{ShardID: 7, FromNode: "node-a:9100", ToNode: "node-b:9100", Reason: "target outside the tenant's serving set"},
					{ShardID: 9, FromNode: "node-a:9100", ToNode: "node-c:9100", Reason: "home group", Err: errors.New("tenant home group never moves")},
				},
			}, nil
		},
	}
	out, err := executeRebalanceCmd(t, svc, "plan")
	if err != nil {
		t.Fatalf("rebalance plan: %v", err)
	}
	for _, want := range []string{
		"Skipped:           shard 7 node-a:9100 -> node-b:9100: target outside the tenant's serving set",
		"Skipped:           shard 9 node-a:9100 -> node-c:9100: home group (tenant home group never moves)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output lacks %q:\n%s", want, out)
		}
	}
}
