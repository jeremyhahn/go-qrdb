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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// transferLeadership type-asserts svc's LeaderTransferrer capability, which
// NewAdminService's AdminServicer return type does not expose directly, and
// calls it -- mirroring how every transport binding reaches it.
func transferLeadership(t *testing.T, svc AdminServicer, shardID, targetReplicaID uint64) error {
	t.Helper()
	lt, ok := svc.(LeaderTransferrer)
	if !ok {
		t.Fatalf("%T does not implement LeaderTransferrer", svc)
	}
	return lt.TransferLeadership(context.Background(), shardID, targetReplicaID)
}

// newTransferHost returns a mockHost whose shard 1 is led by replica 1 until
// RequestLeaderTransfer is called, and by whatever lead returns after that.
func newTransferHost(lead func(target uint64) (uint64, bool, error)) *mockHost {
	var target atomic.Uint64
	return &mockHost{
		getLeaderIDFn: func(uint64) (uint64, bool, error) {
			if to := target.Load(); to != 0 {
				return lead(to)
			}
			return 1, true, nil
		},
		requestLeaderTransferFn: func(_ context.Context, _, to uint64) error {
			target.Store(to)
			return nil
		},
	}
}

func TestAdminService_TransferLeadership_Success(t *testing.T) {
	var gotShard, gotTarget uint64
	host := newTransferHost(func(to uint64) (uint64, bool, error) { return to, true, nil })
	request := host.requestLeaderTransferFn
	host.requestLeaderTransferFn = func(ctx context.Context, shardID, to uint64) error {
		gotShard, gotTarget = shardID, to
		return request(ctx, shardID, to)
	}

	if err := transferLeadership(t, NewAdminService(host), 1, 2); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	if gotShard != 1 || gotTarget != 2 {
		t.Fatalf("RequestLeaderTransfer(%d, %d), want (1, 2)", gotShard, gotTarget)
	}
}

func TestAdminService_TransferLeadership_WaitsThroughElection(t *testing.T) {
	var polls atomic.Int32
	host := newTransferHost(func(to uint64) (uint64, bool, error) {
		// Leaderless, then a failed read, then the target: the wait must
		// treat the first two as readings to poll past, not answers.
		switch polls.Add(1) {
		case 1:
			return 0, false, nil
		case 2:
			return 0, false, quicraft.ErrShardNotReady
		}
		return to, true, nil
	})

	if err := transferLeadership(t, NewAdminService(host), 1, 3); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	if got := polls.Load(); got < 3 {
		t.Fatalf("polled the leader %d times, want at least 3", got)
	}
}

func TestAdminService_TransferLeadership_ToCurrentLeader(t *testing.T) {
	// The host treats a transfer to itself as done; the first poll agrees.
	host := newTransferHost(func(uint64) (uint64, bool, error) { return 1, true, nil })
	if err := transferLeadership(t, NewAdminService(host), 1, 1); err != nil {
		t.Fatalf("TransferLeadership to the leader: %v", err)
	}
}

func TestAdminService_TransferLeadership_Timeout(t *testing.T) {
	// The target never takes over, as when it is stopped.
	host := newTransferHost(func(uint64) (uint64, bool, error) { return 1, true, nil })
	svc := &AdminService{host: host, leaderTransferTimeout: 50 * time.Millisecond}

	err := svc.TransferLeadership(context.Background(), 1, 3)
	var lte *LeaderTransferTimeoutError
	if !errors.As(err, &lte) {
		t.Fatalf("got %T (%v), want *LeaderTransferTimeoutError", err, err)
	}
	want := LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 50 * time.Millisecond}
	if *lte != want {
		t.Fatalf("got %+v, want %+v", *lte, want)
	}
}

func TestAdminService_TransferLeadership_DefaultTimeout(t *testing.T) {
	// The bound is the shared default when none is set; a ctx that ends
	// first returns its own error rather than waiting it out.
	host := newTransferHost(func(uint64) (uint64, bool, error) { return 1, true, nil })
	svc := &AdminService{host: host}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := svc.TransferLeadership(ctx, 1, 3)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
}

func TestAdminService_TransferLeadership_ZeroTarget(t *testing.T) {
	called := false
	host := &mockHost{requestLeaderTransferFn: func(context.Context, uint64, uint64) error {
		called = true
		return nil
	}}

	err := transferLeadership(t, NewAdminService(host), 7, 0)
	var ure *UnknownReplicaError
	if !errors.As(err, &ure) || ure.ShardID != 7 || ure.ReplicaID != 0 {
		t.Fatalf("got %T (%v), want *UnknownReplicaError{ShardID: 7}", err, err)
	}
	if called {
		t.Fatal("RequestLeaderTransfer was called for replica 0")
	}
}

func TestAdminService_TransferLeadership_LeaderLookupFails(t *testing.T) {
	host := &mockHost{getLeaderIDFn: func(uint64) (uint64, bool, error) {
		return 0, false, quicraft.ErrShardNotFound
	}}

	err := transferLeadership(t, NewAdminService(host), 9, 2)
	var use *UnknownShardError
	if !errors.As(err, &use) || use.ShardID != 9 {
		t.Fatalf("got %T (%v), want *UnknownShardError{ShardID: 9}", err, err)
	}
}

func TestAdminService_TransferLeadership_RequestRefused(t *testing.T) {
	other := errors.New("engine busy")
	tests := []struct {
		name  string
		err   error
		check func(t *testing.T, err error)
	}{
		{
			name: "not leader",
			err:  &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:9100"},
			check: func(t *testing.T, err error) {
				var nle *NotLeaderError
				if !errors.As(err, &nle) || nle.LeaderID != 2 || nle.LeaderAddress != "10.0.0.2:9100" {
					t.Fatalf("got %T (%v), want *NotLeaderError naming replica 2", err, err)
				}
			},
		},
		{
			name: "unknown shard",
			err:  quicraft.ErrShardNotFound,
			check: func(t *testing.T, err error) {
				var use *UnknownShardError
				if !errors.As(err, &use) || use.ShardID != 1 {
					t.Fatalf("got %T (%v), want *UnknownShardError{ShardID: 1}", err, err)
				}
			},
		},
		{
			name: "unknown replica",
			err:  &quicraft.ReplicaNotFoundError{ShardID: 1, ReplicaID: 9},
			check: func(t *testing.T, err error) {
				var ure *UnknownReplicaError
				if !errors.As(err, &ure) || ure.ShardID != 1 || ure.ReplicaID != 9 {
					t.Fatalf("got %T (%v), want *UnknownReplicaError{1, 9}", err, err)
				}
			},
		},
		{
			name: "observer target",
			err:  &quicraft.InvalidMemberError{ShardID: 1, ReplicaID: 4, Reason: "leader transfer target is a non-voting observer"},
			check: func(t *testing.T, err error) {
				var lte *LeaderTransferTargetError
				if !errors.As(err, &lte) || lte.ReplicaID != 4 || !strings.Contains(lte.Reason, "observer") {
					t.Fatalf("got %T (%v), want *LeaderTransferTargetError for replica 4", err, err)
				}
			},
		},
		{
			name: "anything else",
			err:  other,
			check: func(t *testing.T, err error) {
				var me *MembershipError
				if !errors.As(err, &me) || me.Op != "transfer-leadership" || !errors.Is(err, other) {
					t.Fatalf("got %T (%v), want *MembershipError wrapping the cause", err, err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := &mockHost{requestLeaderTransferFn: func(context.Context, uint64, uint64) error {
				return tc.err
			}}
			tc.check(t, transferLeadership(t, NewAdminService(host), 1, 2))
		})
	}
}

func TestTranslateLeaderTransferError_WrappedCauses(t *testing.T) {
	// The host's refusals arrive wrapped by the backend; the typed answer
	// must survive that.
	wrapped := &MembershipError{Op: "inner", Err: &quicraft.ReplicaNotFoundError{ShardID: 3, ReplicaID: 8}}
	var ure *UnknownReplicaError
	if err := translateLeaderTransferError(3, wrapped); !errors.As(err, &ure) || ure.ReplicaID != 8 {
		t.Fatalf("got %T (%v), want *UnknownReplicaError for replica 8", err, err)
	}

	wrapped = &MembershipError{Op: "inner", Err: quicraft.ErrShardNotFound}
	var use *UnknownShardError
	if err := translateLeaderTransferError(5, wrapped); !errors.As(err, &use) || use.ShardID != 5 {
		t.Fatalf("got %T (%v), want *UnknownShardError{ShardID: 5}", err, err)
	}
}

func TestTranslateLeaderTransferError_NotLeaderIsNotMembership(t *testing.T) {
	err := translateLeaderTransferError(1, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 3})
	var me *MembershipError
	if errors.As(err, &me) {
		t.Fatalf("NotLeaderError was wrapped as MembershipError: %v", err)
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) || nle.ShardID != 1 || nle.LeaderID != 3 {
		t.Fatalf("got %T (%v), want *NotLeaderError{1, 3}", err, err)
	}
}

func TestAwaitLeaderTransfer_SettlesOnFirstPoll(t *testing.T) {
	timeoutErr := &LeaderTransferTimeoutError{Timeout: time.Hour}
	err := awaitLeaderTransfer(context.Background(),
		func() (uint64, bool, error) { return 2, true, nil },
		func(id uint64) bool { return id == 2 },
		timeoutErr)
	if err != nil {
		t.Fatalf("awaitLeaderTransfer: %v", err)
	}
}

func TestAwaitLeaderTransfer_ReturnsTheGivenTimeoutError(t *testing.T) {
	timeoutErr := &LeaderTransferTimeoutError{ShardID: 4, FromReplicaID: 1, Timeout: 20 * time.Millisecond}
	err := awaitLeaderTransfer(context.Background(),
		func() (uint64, bool, error) { return 1, true, nil },
		func(id uint64) bool { return id != 1 },
		timeoutErr)
	if err != timeoutErr {
		t.Fatalf("got %v, want the timeout error it was given", err)
	}
}

func TestAwaitLeaderTransfer_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitLeaderTransfer(ctx,
		func() (uint64, bool, error) { return 0, false, errors.New("no leader") },
		func(uint64) bool { return true },
		&LeaderTransferTimeoutError{Timeout: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestLeaderTransferErrors_Messages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{
			&LeaderTransferTimeoutError{ShardID: 5, FromReplicaID: 1, Timeout: 5 * time.Second},
			"service: shard 5: leadership transfer away from replica 1 did not complete after 5s",
		},
		{
			&LeaderTransferTimeoutError{ShardID: 5, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second},
			"service: shard 5: leadership transfer away from replica 1 to replica 3 did not complete after 5s",
		},
		{&UnknownShardError{ShardID: 9}, "service: shard 9 is not loaded on this host"},
		{&UnknownReplicaError{ShardID: 1, ReplicaID: 9}, "service: shard 1 has no replica 9"},
		{
			&LeaderTransferTargetError{ShardID: 1, ReplicaID: 4, Reason: "it is a witness"},
			"service: shard 1: replica 4 cannot take leadership: it is a witness",
		},
		{&LeaderTransferUnsupportedError{}, "service: this admin service does not support leadership transfer"},
	}
	for _, tc := range tests {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
	}
}
