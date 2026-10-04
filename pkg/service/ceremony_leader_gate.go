// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// LeaderChecker reports whether THIS node is currently the Raft leader for
// the shard ceremony-initialize's durable pending-admin-cert record lives
// on, and the current leader's Raft address when it is not.
//
// hostLeaderChecker (cmd/qrdbd) is the production implementation, over
// service.Host.GetLeaderID / GetNodeHostInfo -- the same primitives every
// other NotLeaderError-reporting service in this package already uses.
type LeaderChecker interface {
	// IsLeader reports leadership and, when false, the current leader's
	// Raft address -- "" when no leader is known yet. A non-nil error means
	// leadership could not be determined at all (e.g. the shard is not
	// loaded on this node).
	IsLeader() (isLeader bool, leaderAddr string, err error)
}

// AdminCredentialNotLeaderError is returned when ceremony-initialize's
// admin-credential step (go-qrdb#548) is attempted on a node that is not
// the Raft leader for the shard its durable pending-admin-cert record lives
// on. It is refused UP FRONT, before anything is issued, signed or
// recorded: a follower cannot durably record what it would issue, since
// that record is itself a Raft write only the leader can commit. The
// operator's remedy is named directly: re-run `qrdb init` against the
// leader.
type AdminCredentialNotLeaderError struct {
	// LeaderAddress is the current leader's Raft address, or "" when no
	// leader is known yet (e.g. an election is still in progress).
	LeaderAddress string
}

// Error implements error.
func (e *AdminCredentialNotLeaderError) Error() string {
	if e.LeaderAddress == "" {
		return "service: ceremony-initialize's admin-credential step must run on the Raft leader; " +
			"no leader is known yet -- wait for an election to complete and retry"
	}
	return "service: ceremony-initialize's admin-credential step must run on the Raft leader; " +
		`re-run "qrdb init" against the leader at ` + e.LeaderAddress
}

// CeremonyServiceWithLeaderGate wraps a CeremonyServicer so that
// CeremonyInitialize is refused with *AdminCredentialNotLeaderError on any
// node that is not the current Raft leader, before it reaches the wrapped
// service (and, in particular, before CeremonyServiceWithAdminRebind's own
// admin-credential retry logic ever runs). It is the outermost decorator:
// compose it around CeremonyServiceWithAdminRebind, never the reverse, so
// the leader check always runs first.
//
// This does not apply to the pre-host "sealed window" ceremony service
// (pkg/server/lifecycle.BuildSealServicesOnly): no Raft host exists yet at
// that point, so there is no leader to check against, and the very first
// ceremony-initialize call in a fresh cluster necessarily reaches it.
type CeremonyServiceWithLeaderGate struct {
	CeremonyServicer
	leader LeaderChecker
}

// NewCeremonyServiceWithLeaderGate wraps inner with a leadership check
// gating every CeremonyInitialize call.
func NewCeremonyServiceWithLeaderGate(inner CeremonyServicer, leader LeaderChecker) *CeremonyServiceWithLeaderGate {
	return &CeremonyServiceWithLeaderGate{CeremonyServicer: inner, leader: leader}
}

// CeremonyInitialize refuses up front when this node is not the Raft
// leader, otherwise delegates unchanged.
func (s *CeremonyServiceWithLeaderGate) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *CeremonyInitializeOptions) (*CeremonyInitResult, error) {
	isLeader, leaderAddr, err := s.leader.IsLeader()
	if err != nil {
		return nil, err
	}
	if !isLeader {
		return nil, &AdminCredentialNotLeaderError{LeaderAddress: leaderAddr}
	}
	return s.CeremonyServicer.CeremonyInitialize(ctx, ca, opts)
}

// HostLeaderChecker is the production LeaderChecker: whether THIS replica
// is the current Raft leader for ShardID, and the current leader's Raft
// address from a static peer map when it is not (go-qrdb#548 review item
// 6). Every composer that gates ceremony-initialize's admin-credential
// step on leadership -- cmd/qrdbd, sdk/go/node, and any future embedder --
// shares this ONE implementation, so a retried `qrdb init` is judged
// identically regardless of which composer is running it.
type HostLeaderChecker struct {
	// Host answers GetLeaderID for ShardID.
	Host Host
	// ShardID is the shard ceremony-initialize's durable pending-admin-cert
	// record lives on.
	ShardID uint64
	// ReplicaID is this process's own replica ID on Host.
	ReplicaID uint64
	// Members maps a Raft replica ID to its advertised address, for naming
	// the current leader when this replica is not it. A nil map reports ""
	// for the leader address (unknown to this node), which
	// AdminCredentialNotLeaderError renders as "wait for an election to
	// complete".
	Members map[uint64]string
}

// IsLeader implements LeaderChecker.
func (c HostLeaderChecker) IsLeader() (isLeader bool, leaderAddr string, err error) {
	leaderID, hasLeader, err := c.Host.GetLeaderID(c.ShardID)
	if err != nil {
		return false, "", err
	}
	if !hasLeader {
		return false, "", nil
	}
	if leaderID == c.ReplicaID {
		return true, "", nil
	}
	return false, c.Members[leaderID], nil
}

// ComposeCeremonyServiceWithAdminCredentialWiring wraps ceremonySvc with the
// admin-credential rebind path and the Raft leader gate that must sit in
// front of it (go-qrdb#548 review item 6): every composer that first-boots
// through ceremony-initialize's pending-admin-cert hand-off -- cmd/qrdbd,
// sdk/go/node, and any future embedder -- wires the SAME two decorators in
// the SAME order (CeremonyServiceWithLeaderGate outermost, around
// CeremonyServiceWithAdminRebind), so a retried `qrdb init` behaves
// identically regardless of which composer is running it.
//
// notifier -- optional, nil is a no-op -- is told once a reissue binds a
// fresh admin certificate; a composer passes the SAME
// *firstboot.AwaitingAdminCredentialProbe it later installs as the
// transport gates' probe (go-qrdb#560 review B1), wired here BEFORE that
// probe's own boot-time lookup has necessarily run -- NewAwaitingAdminCredentialProbe's
// pointer is stable and usable as an AdminBoundNotifier from construction.
//
// Returns ceremonySvc UNCHANGED when it is nil, or when its underlying
// *ceremony.CeremonyService core cannot be resolved via CeremonyServiceCore
// -- a composition shape (e.g. the pre-host sealed-window ceremony service)
// neither wrapper can attach to safely; see CeremonyServiceWithLeaderGate's
// own doc comment for why the pre-host service is excepted.
func ComposeCeremonyServiceWithAdminCredentialWiring(ceremonySvc CeremonyServicer, users firstboot.AdminLookup, binder firstboot.CertBinder, leader LeaderChecker, notifier firstboot.AdminBoundNotifier) CeremonyServicer {
	if ceremonySvc == nil {
		return nil
	}
	core, ok := CeremonyServiceCore(ceremonySvc)
	if !ok {
		return ceremonySvc
	}
	rebound := NewCeremonyServiceWithAdminRebind(ceremonySvc, core, users, binder, notifier)
	return NewCeremonyServiceWithLeaderGate(rebound, leader)
}
