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

package planner_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// TestOpString verifies every defined Op constant returns the correct
// string representation and that an undefined Op returns "unknown".
func TestOpString(t *testing.T) {
	tests := []struct {
		op   planner.Op
		want string
	}{
		{planner.OpEq, "="},
		{planner.OpNeq, "!="},
		{planner.OpGt, ">"},
		{planner.OpGte, ">="},
		{planner.OpLt, "<"},
		{planner.OpLte, "<="},
		{planner.OpBetween, "BETWEEN"},
		{planner.OpPrefix, "PREFIX"},
		{planner.OpIn, "IN"},
		{planner.Op(0), "unknown"},
		{planner.Op(255), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.op.String()
			if got != tc.want {
				t.Errorf("Op(%d).String() = %q, want %q", tc.op, got, tc.want)
			}
		})
	}
}

// TestPlanTypeString verifies every defined PlanType returns the correct
// string representation and that an undefined PlanType returns "unknown".
func TestPlanTypeString(t *testing.T) {
	tests := []struct {
		pt   planner.PlanType
		want string
	}{
		{planner.PlanFullScan, "FullScan"},
		{planner.PlanIndexLookup, "IndexLookup"},
		{planner.PlanIndexScan, "IndexScan"},
		{planner.PlanType(255), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.pt.String()
			if got != tc.want {
				t.Errorf("PlanType(%d).String() = %q, want %q", tc.pt, got, tc.want)
			}
		})
	}
}
