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

package server

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusterSealRetryable: the errors of a system shard electing, as the
// system store reports them, are transient; everything else is final.
func TestClusterSealRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// The #654 failure, verbatim: a linearizable read timing out.
		{"read timeout", &service.ReadError{Err: quicraft.ErrTimeout}, true},
		{"proposal dropped", quicraft.ErrDropped, true},
		{"aborted by a leadership change", quicraft.ErrAborted, true},
		{"busy", quicraft.ErrBusy, true},
		{"raft not leader", quicraft.ErrNotLeader, true},
		{"shard not ready", quicraft.ErrShardNotReady, true},
		{"read index not ready", &service.ReadError{Err: quicraft.ErrReadIndexNotReady}, true},
		{"no leader known", &service.NotLeaderError{}, true},
		{"leader moved", &service.NotLeaderError{LeaderID: 3}, true},
		{"leader moved past the transport", &transport.NotLeaderError{LeaderID: 3}, true},
		{"nil", nil, false},
		{"sealed", seal.ErrSealed, false},
		{"epoch conflict", &clusterseal.SealEpochConflictError{Barrier: clusterseal.TenantBarrier("rt1")}, false},
		{"cancelled", context.Canceled, false},
		{"raft cancelled", quicraft.ErrCanceled, false},
		{"host closed", quicraft.ErrClosed, false},
		{"shard not found", quicraft.ErrShardNotFound, false},
		{"anything else", errors.New("permission denied"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClusterSealRetryable(tc.err); got != tc.want {
				t.Fatalf("ClusterSealRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
