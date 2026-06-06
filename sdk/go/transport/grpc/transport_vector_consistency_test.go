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
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestVectorConsistencyToProto_gRPC verifies the mapping from
// transport.ConsistencyLevel (uint8) to proto ConsistencyLevel for the gRPC
// SDK transport. The zero value (ConsistencyStrong) must map to STRONG
// (not UNSPECIFIED) so that the server applies the same behavior that
// existing clients received when the field was an int32.
func TestVectorConsistencyToProto_gRPC(t *testing.T) {
	tests := []struct {
		name  string
		level transport.ConsistencyLevel
		want  pb.ConsistencyLevel
	}{
		{
			name:  "strong maps to STRONG (preserves old int32=0 behavior)",
			level: transport.ConsistencyStrong,
			want:  pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG,
		},
		{
			name:  "linearizable maps to LINEARIZABLE",
			level: transport.ConsistencyLinearizable,
			want:  pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE,
		},
		{
			name:  "eventual maps to EVENTUAL",
			level: transport.ConsistencyEventual,
			want:  pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := vectorConsistencyToProto(uint8(tc.level))
			if got != tc.want {
				t.Errorf("vectorConsistencyToProto(%v) = %v, want %v", tc.level, got, tc.want)
			}
		})
	}
}
