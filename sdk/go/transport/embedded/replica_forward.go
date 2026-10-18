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

package embedded

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	quicsdk "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
)

// ReplicaForwarder forwards a QUIC request frame the node refused because it
// holds no replica of the database to a replica, as caller, and returns the
// status and payload of the frame the replica answered. forwarded is false
// when no replica served it; the call is then answered the refusal. The
// node's *quic.FrameForwarder (pkg/api/quic) is one.
type ReplicaForwarder interface {
	Forward(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, refusal *placement.NotAReplicaError) (status uint16, body []byte, forwarded bool)

	// ForwardToLeader forwards a request frame this node refused with a
	// leader-only error (service.AdminCredentialNotLeaderError and its
	// siblings) to the leader at leaderAddr, as caller, and returns the
	// status and payload of the frame the leader answered. forwarded is
	// false when the leader could not be resolved or reached; the call is
	// then answered the refusal. Unlike Forward it is a single hop by
	// address, not a placement-based retry across a replica set.
	ForwardToLeader(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, leaderAddr string) (status uint16, body []byte, forwarded bool)
}

// replicaForwarding is what a database-scoped transport forwards with.
type replicaForwarding struct {
	forwarder ReplicaForwarder
	caller    auth.Authenticator
}

// WithReplicaForwarding makes a database-scoped transport (WithDatabase)
// forward a data-plane call the node refuses because it holds no replica of
// the database -- *placement.NotAReplicaError -- to a replica, over QUIC, as
// the QUIC request the caller would have sent there (docs/replica-
// forwarding). The call carries, in the signed on-behalf assertion, the
// identity caller authenticates for it: the identity the embedder configured,
// never the node's. A call caller authenticates no identity for, one whose
// identity belongs to another tenant than the refused database, and every
// stream is answered the refusal. A call a follower refuses with the leader's
// address -- *transport.NotLeaderError, a lease's remaining TTL -- goes to
// that leader the same way. With either argument nil nothing is forwarded.
func WithReplicaForwarding(forwarder ReplicaForwarder, caller auth.Authenticator) Option {
	return func(t *Transport) {
		if forwarder == nil || caller == nil {
			t.forwarding = nil
			return
		}
		t.forwarding = &replicaForwarding{forwarder: forwarder, caller: caller}
	}
}

// forwardingScope is a database-scoped transport that forwards what the node
// refuses as a non-replica or as a follower. Every method it does not override -- the control
// plane, and the streams -- is the scoped transport's own.
type forwardingScope struct {
	*Transport
	database string
	fwd      *replicaForwarding
}

// forwardTo returns the transport to make a call again with when err is the
// node's refusal as a non-replica and the call can be forwarded: one that
// sends each request frame to a replica through the forwarder, as the
// configured caller. It returns false otherwise, and the caller is answered
// err.
func (s *forwardingScope) forwardTo(ctx context.Context, err error) (*quicsdk.Transport, bool) {
	var refusal *placement.NotAReplicaError
	if !errors.As(err, &refusal) {
		return s.forwardToLeader(ctx, err)
	}
	caller, aerr := s.fwd.caller.Authenticate(ctx)
	if aerr != nil || caller == nil || caller.Subject == "" {
		slog.Warn("replicaforward: embedded call not forwarded: the configured caller did not authenticate",
			"tenant", refusal.TenantID, "database", refusal.Database, "error", aerr)
		return nil, false
	}
	if caller.TenantID != refusal.TenantID {
		slog.Warn("replicaforward: embedded call not forwarded: the configured caller belongs to another tenant",
			"tenant", refusal.TenantID, "database", refusal.Database, "caller_tenant", caller.TenantID)
		return nil, false
	}
	return quicsdk.NewFrameTransport(s.database, func(ctx context.Context, method, database string, payload []byte) (uint16, []byte, error) {
		status, body, ok := s.fwd.forwarder.Forward(ctx, caller, method, database, payload, refusal)
		if !ok {
			return 0, nil, refusal
		}
		return status, body, nil
	}), true
}

// forwardToLeader returns the transport to make a call again with when err is
// a follower's refusal naming its leader -- a lease's remaining TTL, which
// only the leader knows -- one that sends each request frame to that leader
// through the forwarder, as the configured caller, as the network transports
// forward a not-leader refusal. It returns false when err is no such refusal,
// the leader is unknown, or the caller does not authenticate, and the caller
// is then answered err.
func (s *forwardingScope) forwardToLeader(ctx context.Context, err error) (*quicsdk.Transport, bool) {
	var notLeader *transport.NotLeaderError
	if !errors.As(err, &notLeader) || notLeader.LeaderAddress == "" {
		return nil, false
	}
	caller, aerr := s.fwd.caller.Authenticate(ctx)
	if aerr != nil || caller == nil || caller.Subject == "" {
		slog.Warn("replicaforward: embedded call not forwarded to the leader: the configured caller did not authenticate",
			"database", s.database, "leader_raft_addr", notLeader.LeaderAddress, "error", aerr)
		return nil, false
	}
	return quicsdk.NewFrameTransport(s.database, func(ctx context.Context, method, database string, payload []byte) (uint16, []byte, error) {
		status, body, ok := s.fwd.forwarder.ForwardToLeader(ctx, caller, method, database, payload, notLeader.LeaderAddress)
		if !ok {
			return 0, nil, notLeader
		}
		return status, body, nil
	}), true
}

// forwardAdminTo returns the transport to make an admin/ceremony call again
// with when t.forwarding is configured and the node's own call refused it
// with a leader-only error (refusal) naming the leader at leaderAddr: one
// that sends the request frame to that leader through the forwarder, as the
// configured caller. It returns false when forwarding is not configured,
// leaderAddr is empty, or the caller does not authenticate, and the call is
// then answered refusal -- the node's own refusal -- exactly as forwardTo's caller is
// answered its own refusal on the same conditions.
func (t *Transport) forwardAdminTo(ctx context.Context, refusal error, leaderAddr string) (*quicsdk.Transport, bool) {
	if t.forwarding == nil || leaderAddr == "" {
		return nil, false
	}
	caller, aerr := t.forwarding.caller.Authenticate(ctx)
	if aerr != nil || caller == nil || caller.Subject == "" {
		slog.Warn("admin forwarding: embedded call not forwarded: the configured caller did not authenticate",
			"leader_raft_addr", leaderAddr, "error", aerr)
		return nil, false
	}
	return quicsdk.NewFrameTransport("", func(ctx context.Context, method, database string, payload []byte) (uint16, []byte, error) {
		status, body, ok := t.forwarding.forwarder.ForwardToLeader(ctx, caller, method, database, payload, leaderAddr)
		if !ok {
			return 0, nil, refusal
		}
		return status, body, nil
	}), true
}
