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

import "context"

// HostProposerAdapter wraps a Host to satisfy the batch.Proposer interface
// from go-quicraft, whose SyncPropose returns only an error.
type HostProposerAdapter struct {
	host Host
}

// NewHostProposerAdapter creates a new adapter wrapping the given Host.
func NewHostProposerAdapter(host Host) *HostProposerAdapter {
	return &HostProposerAdapter{host: host}
}

// SyncPropose proposes a command through the underlying Host, converting an
// application error carried in the result into a Go error.
//
// The narrower interface cannot return an sm.Result, but that is not a licence
// to drop what the result says. The state machine encodes a not-configured
// subsystem INTO the result rather than returning it to Raft — returning it
// would stall every subsequent read on the shard — so an adapter that looked
// only at the Raft error reported a missing subsystem as a successful batch
// (go-qrdb#62). checkResultError answers nil for an ordinary result, so this
// changes nothing for a batch that actually applied.
func (a *HostProposerAdapter) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) error {
	result, err := propose(ctx, a.host, shardID, cmd)
	if err != nil {
		return err
	}
	return checkResultError(result)
}
