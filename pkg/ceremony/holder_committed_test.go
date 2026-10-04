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

package ceremony

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestHolderScope_CommittedWithoutStagedSetIsRefused: Committed is told a
// blob is durable and swaps in the envelopes staged for it. When nothing is
// staged for that distribution -- another distribution replaced it -- the
// envelopes in force do not open the durable blob, and saying so is the only
// safe answer: Committed refuses with *StagedDistributionMissingError and
// changes nothing, rather than recording a share set no holder can claim.
func TestHolderScope_CommittedWithoutStagedSetIsRefused(t *testing.T) {
	ctx := context.Background()
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", seal.NewMemoryBackend(), DefaultShareTTL, func() bool { return false }, testLogger())
	require.NoError(t, err)
	inForce := newSealedShareStore(DefaultShareTTL)
	require.NoError(t, inForce.Store(context.Background(), "alice", []byte("envelope-d0")))
	h.store = inForce
	staged := newSealedShareStore(DefaultShareTTL)
	require.NoError(t, staged.Store(context.Background(), "alice", []byte("envelope-d2")))
	h.staged, h.stagedDistribution = staged, "d2"

	err = h.committed(ctx, nil, &seal.HolderRoster{Domain: seal.DomainUnseal, Distribution: "d1"}, nil)
	var missing *StagedDistributionMissingError
	require.True(t, errors.As(err, &missing), "got %v", err)
	require.Equal(t, "d1", missing.Distribution)
	require.Same(t, inForce, h.store, "the envelopes in force are untouched")
	require.Same(t, staged, h.staged, "another distribution's staged set is untouched")

	require.NoError(t, h.committed(ctx, nil, &seal.HolderRoster{Domain: seal.DomainUnseal, Distribution: "d2"}, nil))
	require.Same(t, staged, h.store, "the matching staged set is swapped in")
}
