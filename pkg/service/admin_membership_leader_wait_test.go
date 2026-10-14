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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// electingLeader is a leader resolver for a shard in the middle of an
// election: it reports no leader for the first unknownFor lookups and
// replica 2 from then on.
type electingLeader struct {
	unknownFor int
	calls      int
}

func (e *electingLeader) GetLeaderID(uint64) (uint64, bool, error) {
	e.calls++
	if e.calls <= e.unknownFor {
		return 0, false, nil
	}
	return 2, true, nil
}

func (e *electingLeader) known() bool { return e.calls > e.unknownFor }

// membershipOp is one AdminService membership change, with the mockHost
// hook it proposes through.
type membershipOp struct {
	name string
	// wire installs propose as the host hook this op proposes through.
	wire func(h *mockHost, propose func() (*quicraft.RequestState, error))
	call func(ctx context.Context, svc *AdminService) error
}

const leaderWaitTestIdentity = "spki:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func membershipOps() []membershipOp {
	return []membershipOp{
		{
			name: "add",
			wire: func(h *mockHost, propose func() (*quicraft.RequestState, error)) {
				h.requestAddNodeFn = func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) { return propose() }
			},
			call: func(ctx context.Context, svc *AdminService) error {
				return svc.AddMember(ctx, 1, 4, "10.0.0.4:9100", leaderWaitTestIdentity)
			},
		},
		{
			name: "add-observer",
			wire: func(h *mockHost, propose func() (*quicraft.RequestState, error)) {
				h.requestAddObserverFn = func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) { return propose() }
			},
			call: func(ctx context.Context, svc *AdminService) error {
				return svc.AddObserverMember(ctx, 1, 4, "10.0.0.4:9100", leaderWaitTestIdentity)
			},
		},
		{
			name: "remove",
			wire: func(h *mockHost, propose func() (*quicraft.RequestState, error)) {
				h.requestRemoveNodeFn = func(context.Context, uint64, uint64) (*quicraft.RequestState, error) { return propose() }
			},
			call: func(ctx context.Context, svc *AdminService) error {
				return svc.RemoveMember(ctx, 1, 3)
			},
		},
		{
			name: "promote",
			wire: func(h *mockHost, propose func() (*quicraft.RequestState, error)) {
				h.memberships = map[uint64]*quicraft.Membership{1: {
					Observers:  map[uint64]string{3: "10.0.0.3:9100"},
					Identities: map[uint64]string{3: leaderWaitTestIdentity},
				}}
				h.requestAddNodeFn = func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) { return propose() }
			},
			call: func(ctx context.Context, svc *AdminService) error {
				return svc.PromoteMember(ctx, 1, 3)
			},
		},
	}
}

// newLeaderWaitAdmin returns an AdminService over host whose leader wait
// polls every millisecond for at most timeout.
func newLeaderWaitAdmin(host *mockHost, timeout time.Duration) *AdminService {
	svc := NewAdminService(host).(*AdminService)
	svc.leaderWaitTimeout = timeout
	svc.leaderWaitInterval = time.Millisecond
	return svc
}

// TestAdminService_MembershipWaitsForKnownLeader is Q13 option A: a
// membership change that arrives while shard 1 is electing waits for the
// election instead of proposing into it, then proposes exactly once.
func TestAdminService_MembershipWaitsForKnownLeader(t *testing.T) {
	for _, op := range membershipOps() {
		t.Run(op.name, func(t *testing.T) {
			leader := &electingLeader{unknownFor: 3}
			host := &mockHost{getLeaderIDFn: leader.GetLeaderID}
			proposals := 0
			op.wire(host, func() (*quicraft.RequestState, error) {
				proposals++
				if !leader.known() {
					return nil, &quicraft.NotLeaderError{ShardID: 1}
				}
				return newCompletedRequestState(quicraft.RequestResult{}), nil
			})

			svc := newLeaderWaitAdmin(host, 10*time.Second)
			if err := op.call(context.Background(), svc); err != nil {
				t.Fatalf("%s while electing: %v", op.name, err)
			}
			if proposals != 1 {
				t.Fatalf("%s proposed %d times, want exactly 1", op.name, proposals)
			}
		})
	}
}

// TestAdminService_MembershipLeaderWaitTimesOut proves the wait is bounded
// and fails closed: no leader within the bound is a *ShardLeaderTimeoutError
// (Unavailable, so 503) and nothing is proposed.
func TestAdminService_MembershipLeaderWaitTimesOut(t *testing.T) {
	for _, op := range membershipOps() {
		t.Run(op.name, func(t *testing.T) {
			host := &mockHost{getLeaderIDFn: func(uint64) (uint64, bool, error) { return 0, false, nil }}
			proposals := 0
			op.wire(host, func() (*quicraft.RequestState, error) {
				proposals++
				return newCompletedRequestState(quicraft.RequestResult{}), nil
			})

			svc := newLeaderWaitAdmin(host, 20*time.Millisecond)
			err := op.call(context.Background(), svc)
			var timeout *ShardLeaderTimeoutError
			if !errors.As(err, &timeout) {
				t.Fatalf("%s with no leader: want *ShardLeaderTimeoutError, got %T: %v", op.name, err, err)
			}
			if timeout.ShardID != 1 {
				t.Fatalf("ShardLeaderTimeoutError.ShardID = %d, want 1", timeout.ShardID)
			}
			if proposals != 0 {
				t.Fatalf("%s proposed %d times without a leader, want 0", op.name, proposals)
			}
		})
	}
}

// TestAdminService_MembershipLeaderLookupErrorIsFinal: a shard this node
// does not host never grows a leader by waiting, so the change fails at once
// with the lookup error rather than after the full bound.
func TestAdminService_MembershipLeaderLookupErrorIsFinal(t *testing.T) {
	for _, op := range membershipOps() {
		t.Run(op.name, func(t *testing.T) {
			host := &mockHost{getLeaderIDFn: func(uint64) (uint64, bool, error) {
				return 0, false, quicraft.ErrShardNotFound
			}}
			op.wire(host, func() (*quicraft.RequestState, error) {
				t.Fatalf("%s proposed for a shard this node does not host", op.name)
				return nil, nil
			})

			svc := newLeaderWaitAdmin(host, time.Hour)
			err := op.call(context.Background(), svc)
			if !errors.Is(err, quicraft.ErrShardNotFound) {
				t.Fatalf("%s: want ErrShardNotFound, got %T: %v", op.name, err, err)
			}
			var me *MembershipError
			if !errors.As(err, &me) || me.Op != op.name {
				t.Fatalf("%s: want *MembershipError{Op: %q}, got %T: %v", op.name, op.name, err, err)
			}
		})
	}
}

// TestAdminService_MembershipLeaderWaitHonoursContext: a caller that gives
// up while the shard is electing gets its own context error, not a timeout
// of the service's choosing, and nothing is proposed.
func TestAdminService_MembershipLeaderWaitHonoursContext(t *testing.T) {
	for _, op := range membershipOps() {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			host := &mockHost{getLeaderIDFn: func(uint64) (uint64, bool, error) {
				cancel()
				return 0, false, nil
			}}
			op.wire(host, func() (*quicraft.RequestState, error) {
				t.Fatalf("%s proposed after its caller gave up", op.name)
				return nil, nil
			})

			svc := newLeaderWaitAdmin(host, time.Hour)
			if err := op.call(ctx, svc); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: want context.Canceled, got %T: %v", op.name, err, err)
			}
		})
	}
}
