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
