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
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SystemShardSettleBudget bounds how long WhileSystemShardSettling waits on
// a system shard whose leader has committed in its term. The time without
// such a leader does not count: the cluster is still forming, and that wait
// runs for as long as the boot does.
const SystemShardSettleBudget = 15 * time.Second

// systemShardSettlePoll paces WhileSystemShardSettling's attempts.
const systemShardSettlePoll = 100 * time.Millisecond

// WhileSystemShardSettling runs fn until it succeeds, fails with anything but
// the system shard settling, ctx ends, or the settling outlasts
// SystemShardSettleBudget counted over the time the system shard has a
// leader that has committed in its term. Settling is the ReadIndex warm-up of
// a freshly elected leader, or an election in progress: a boot waited for a
// leader, but a peer that restarts meanwhile -- every node in turn during a
// rolling enable-encryption -- can take it away before the boot's next read.
//
// After each attempt that fails with a settling answer or is cut off
// unanswered, leaders is asked whether the system shard has a leader that
// has committed in its term (systemShardCommitted). While it has not, the
// wait is the open-ended leader wait: it runs on under ctx, its progress
// logged, and only a leader that has committed and still does not answer
// within the budget fails the boot. A node does not exit because the
// cluster is still forming (docs/decisions/startup-wait-cascade.md, option
// A). A nil leaders counts all of the time.
//
// It is a startup read (RunStartupRead), and op names it. fn gets each
// attempt's context, bounded by StartupReadAttemptTimeout, and a read in fn
// uses it so an attempt that never returns is cut off and retried rather
// than holding the boot. A step that must outlive the attempt -- a write, or
// a start that keeps its context -- uses its own.
//
// fn is not run once ctx has ended: a read that does not itself consult ctx
// -- an in-process one -- would otherwise let a boot its caller abandoned
// carry on.
func WhileSystemShardSettling(ctx context.Context, leaders LeaderSource, op string, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	read := StartupRead{
		Op:        op,
		Budget:    SystemShardSettleBudget,
		Delay:     systemShardSettlePoll,
		Retryable: SystemShardSettling,
	}
	if leaders != nil {
		read.Counts = func(ctx context.Context) bool { return systemShardCommitted(ctx, leaders) }
	}
	_, err := RunStartupRead(ctx, read, func(actx context.Context) (struct{}, error) {
		return struct{}{}, fn(actx)
	})
	return err
}

// systemShardCommitted reports whether the system shard has a leader that has
// committed in its term: leaders knows one, and, when leaders can probe it, a
// linearizable read answers. A leader without the probe is taken as
// committed once known.
func systemShardCommitted(ctx context.Context, leaders LeaderSource) bool {
	if _, ok, err := leaders.LeaderID(shard.ShardKV); err != nil || !ok {
		return false
	}
	var probe shardLeaderSource
	switch s := leaders.(type) {
	case shardLeaderSource:
		probe = s
	case *quicraft.Host:
		probe = quicraftShardSource{host: s}
	default:
		return true
	}
	return probe.ReadReady(ctx, shard.ShardKV) == nil
}

// systemShardLeaders is host as a LeaderSource for WhileSystemShardSettling:
// nil when there is no host, rather than an interface holding a nil pointer.
func systemShardLeaders(host *quicraft.Host) LeaderSource {
	if host == nil {
		return nil
	}
	return host
}

// SystemShardSettling reports whether err is the system shard settling: the
// ReadIndex warm-up of a new leader, no leader known while one is elected, or
// a leader change that failed a read pending on this follower. go-quicraft
// fails such a read with a not-leader error naming the new leader but no
// address, so the caller retries against it; a refusal that names a leader
// AND its address is an answer, not settling.
//
// Two typed not-leader errors are checked, not one: a caller fed through the
// embedded transport (e.g. the feature gate's self-advertisement, which
// writes through an in-process sdk.Client) sees *transport.NotLeaderError,
// translated from *service.NotLeaderError at the transport boundary with no
// Unwrap chain back to it (sdk/go/transport/embedded's translateError
// deliberately reconstructs a fresh value, not a wrapper) -- so recognizing
// only the service-layer type silently stopped retrying for every caller one
// layer removed from the service package.
func SystemShardSettling(err error) bool {
	if errors.Is(err, quicraft.ErrReadIndexNotReady) {
		return true
	}
	var nl *service.NotLeaderError
	if errors.As(err, &nl) {
		return leaderSettling(nl.LeaderID, nl.LeaderAddress)
	}
	var tnl *transport.NotLeaderError
	if errors.As(err, &tnl) {
		return leaderSettling(tnl.LeaderID, tnl.LeaderAddress)
	}
	return false
}

// leaderSettling reports whether a not-leader answer leaves the leader still
// to be reached: none is known, or a leader change named one whose address
// the notice does not carry.
func leaderSettling(leaderID uint64, leaderAddress string) bool {
	return leaderID == 0 || leaderAddress == ""
}
