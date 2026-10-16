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

package rebalancepb

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestExecutionToProto_Nil(t *testing.T) {
	if got := ExecutionToProto(nil); got != nil {
		t.Fatalf("ExecutionToProto(nil) = %v, want nil", got)
	}
}

func TestExecutionToProto_CarriesFailures(t *testing.T) {
	got := ExecutionToProto(&service.RebalanceExecutionResult{
		MovesCompleted: 1,
		MovesFailed:    1,
		Failures: []service.RebalanceMoveFailure{{
			ShardID: 7, FromNode: "a:1", ToNode: "b:2", Step: "add-observer", Error: "boom",
		}},
		Duration:       2 * time.Second,
		FinalImbalance: 0.25,
	})
	if got.GetMovesCompleted() != 1 || got.GetMovesFailed() != 1 ||
		got.GetDurationNs() != int64(2*time.Second) || got.GetFinalImbalance() != 0.25 {
		t.Fatalf("counters not carried: %v", got)
	}
	if len(got.GetFailures()) != 1 {
		t.Fatalf("failures = %d, want 1", len(got.GetFailures()))
	}
	f := got.GetFailures()[0]
	if f.GetShardId() != 7 || f.GetFromNode() != "a:1" || f.GetToNode() != "b:2" ||
		f.GetStep() != "add-observer" || f.GetError() != "boom" {
		t.Fatalf("failure not carried: %v", f)
	}
}

func TestExecutionToProto_NoFailures(t *testing.T) {
	got := ExecutionToProto(&service.RebalanceExecutionResult{MovesCompleted: 2})
	if got.GetFailures() != nil {
		t.Fatalf("failures = %v, want nil", got.GetFailures())
	}
}
