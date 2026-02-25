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
// from go-quicraft. It discards the sm.Result value from SyncPropose,
// returning only the error.
type HostProposerAdapter struct {
	host Host
}

// NewHostProposerAdapter creates a new adapter wrapping the given Host.
func NewHostProposerAdapter(host Host) *HostProposerAdapter {
	return &HostProposerAdapter{host: host}
}

// SyncPropose proposes a command through the underlying Host and discards
// the result value. This satisfies the batch.Proposer interface.
func (a *HostProposerAdapter) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) error {
	_, err := a.host.SyncPropose(ctx, shardID, cmd)
	return err
}
