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
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// newLeaderHost is the go-quicraft surface of a shard whose leader was just
// elected: it knows its leader, and refuses the first notReady committed-
// membership reads with quicraft.ErrReadIndexNotReady, as go-quicraft does
// until the leader's no-op commits in its term. Every other method is the
// embedded nil interface: the identity resolve must not reach one.
type newLeaderHost struct {
	raftbackend.QuicraftHost
	notReady int64
	reads    atomic.Int64
}

func (h *newLeaderHost) LeaderID(uint64) (uint64, bool, error) { return 1, true, nil }

func (h *newLeaderHost) SyncGetShardMembership(context.Context, uint64) (*quicraft.Membership, error) {
	if h.reads.Add(1) <= h.notReady {
		return nil, quicraft.ErrReadIndexNotReady
	}
	return clusteredMockHost().memberships[shard.ShardKV], nil
}

// transportAdapter is the Raft host adapter of a node with a transport: the
// adapter's own NodeIdentity is "" without a concrete *quicraft.Host, and a
// transport-less host resolves no identities at all. The membership read is
// the adapter's.
type transportAdapter struct{ *raftbackend.HostAdapter }

var _ AdminHost = (*transportAdapter)(nil)

func (transportAdapter) NodeIdentity() string { return identityOne }

func newLeaderAdapter(inner *newLeaderHost) transportAdapter {
	return transportAdapter{raftbackend.NewWithInner(nil, inner)}
}

// TestResolveMemberIdentity_WaitsForNewLeadersFirstCommit drives the member-
// identity resolve an admin AddMember/AddObserverMember makes through the Raft
// host adapter while the shard's new leader has not yet committed in its
// term. The read waits for that commit and resolves the identity; it used to
// fail the admin write UNAVAILABLE (go-qrdb#668, run 53645).
func TestResolveMemberIdentity_WaitsForNewLeadersFirstCommit(t *testing.T) {
	inner := &newLeaderHost{notReady: 2}
	host := newLeaderAdapter(inner)

	got, err := ResolveMemberIdentity(context.Background(), host, shard.ShardKV, 2, "two.example:1", "")
	if err != nil {
		t.Fatalf("ResolveMemberIdentity = %v, want the read to wait out the new leader's first commit", err)
	}
	if got != identityTwo {
		t.Fatalf("identity = %q, want %q", got, identityTwo)
	}
	if n := inner.reads.Load(); n != 3 {
		t.Fatalf("membership reads = %d, want 3 (two refused, one served)", n)
	}
}

// TestResolveMemberIdentity_FirstCommitWaitEndsAtCallerDeadline proves the
// wait is the request's own: a leader that never commits fails the resolve at
// the caller's deadline with the membership-unavailable refusal, still
// matching quicraft.ErrReadIndexNotReady.
func TestResolveMemberIdentity_FirstCommitWaitEndsAtCallerDeadline(t *testing.T) {
	inner := &newLeaderHost{notReady: 1 << 40}
	host := newLeaderAdapter(inner)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ResolveMemberIdentity(ctx, host, shard.ShardKV, 2, "two.example:1", "")
	var mie *MemberIdentityError
	if !errors.As(err, &mie) || mie.Refusal() != IdentityMembershipUnavailable {
		t.Fatalf("ResolveMemberIdentity = %v, want *MemberIdentityError refusing IdentityMembershipUnavailable", err)
	}
	if !errors.Is(err, quicraft.ErrReadIndexNotReady) {
		t.Fatalf("ResolveMemberIdentity = %v, want it to match quicraft.ErrReadIndexNotReady", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("resolve took %s, want the caller's 50ms deadline to bound it", elapsed)
	}
}
