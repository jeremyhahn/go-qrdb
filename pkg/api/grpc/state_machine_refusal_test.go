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

package grpc_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"google.golang.org/grpc/codes"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// smHost is a service.Host whose SyncPropose applies the command to a REAL
// MVCC state machine and returns the result that state machine produced.
type smHost struct {
	service.Host
	sm *store.MVCCStateMachine
}

var _ service.Host = (*smHost)(nil)

func (h *smHost) SyncPropose(ctx context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	results := make([]sm.Result, 1)
	if err := h.sm.Update(ctx, []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		return sm.Result{}, err
	}
	return results[0], nil
}

func openSM(t *testing.T, withLessor bool) *store.MVCCStateMachine {
	t.Helper()
	s := store.NewMVCCStateMachine(nil)
	if withLessor {
		lr := lease.NewLessor()
		s.SetLessor(lr)
		t.Cleanup(lr.Close)
	}
	ctx := context.Background()
	if _, err := s.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

// TestStateMachineRefusals_KeepTheirKindEndToEnd is go-qrdb#557's finish of
// the #141 split, end to end: an invalid TTL refused by a real state machine
// reaches a gRPC caller as InvalidArgument, and a grant on a state machine
// built without a lessor as Unimplemented. Both answered Internal while every
// refusal shared SMNotConfiguredError. The state machine is real; only the
// Raft round trip is replaced.
func TestStateMachineRefusals_KeepTheirKindEndToEnd(t *testing.T) {
	ctx := context.Background()

	_, err := service.NewLeaseService(&smHost{sm: openSM(t, true)}, 1).Grant(ctx, 0)
	if got := grpcapi.ServiceErrorCode(err); got != codes.InvalidArgument {
		t.Errorf("invalid TTL: gRPC code = %s (%v), want InvalidArgument", got, err)
	}

	_, err = service.NewLeaseService(&smHost{sm: openSM(t, false)}, 1).Grant(ctx, 60)
	if got := grpcapi.ServiceErrorCode(err); got != codes.Unimplemented {
		t.Errorf("no lessor: gRPC code = %s (%v), want Unimplemented", got, err)
	}
}
