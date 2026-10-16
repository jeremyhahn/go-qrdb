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

package rebalanceconv

import (
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestResultFromProto_Nil(t *testing.T) {
	got := ResultFromProto(nil)
	if got == nil || got.MovesCompleted != 0 || got.MovesFailed != 0 || got.Failures != nil {
		t.Fatalf("ResultFromProto(nil) = %+v, want zero result", got)
	}
}

func TestResultFromProto_CarriesFailures(t *testing.T) {
	got := ResultFromProto(&pb.RebalanceExecutionResponse{
		MovesCompleted: 2,
		MovesFailed:    1,
		DurationNs:     int64(3 * time.Second),
		FinalImbalance: 0.5,
		Failures: []*pb.RebalanceMoveFailure{{
			ShardId: 9, FromNode: "a:1", ToNode: "b:2", Step: "remove-node", Error: "boom",
		}},
	})
	if got.MovesCompleted != 2 || got.MovesFailed != 1 || got.Duration != 3*time.Second || got.FinalImbalance != 0.5 {
		t.Fatalf("counters not carried: %+v", got)
	}
	want := transport.RebalanceMoveFailure{ShardID: 9, FromNode: "a:1", ToNode: "b:2", Step: "remove-node", Error: "boom"}
	if len(got.Failures) != 1 || got.Failures[0] != want {
		t.Fatalf("failures = %+v, want [%+v]", got.Failures, want)
	}
}
