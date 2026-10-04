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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SystemShardSettleBudget bounds how long WhileSystemShardSettling waits out
// the system shard settling.
const SystemShardSettleBudget = 15 * time.Second

// systemShardSettlePoll paces WhileSystemShardSettling's attempts.
const systemShardSettlePoll = 100 * time.Millisecond

// WhileSystemShardSettling runs fn until it succeeds, fails with anything but
// the system shard settling, ctx ends, or the settling outlasts
// SystemShardSettleBudget. Settling is the ReadIndex warm-up of a freshly
// elected leader, or an election in progress: a boot waited for a leader,
// but a peer that restarts meanwhile -- every node in turn during a rolling
// enable-encryption -- can take it away before the boot's next read.
//
// fn is not run once ctx has ended: a read that does not itself consult ctx
// -- an in-process one -- would otherwise let a boot its caller abandoned
// carry on.
func WhileSystemShardSettling(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(SystemShardSettleBudget)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil || !SystemShardSettling(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(systemShardSettlePoll):
		}
	}
}

// SystemShardSettling reports whether err is the system shard settling: the
// ReadIndex warm-up of a new leader, or no leader known while one is elected.
// A not-leader answer that names a leader is not settling.
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
		return nl.LeaderID == 0
	}
	var tnl *transport.NotLeaderError
	if errors.As(err, &tnl) {
		return tnl.LeaderID == 0
	}
	return false
}
