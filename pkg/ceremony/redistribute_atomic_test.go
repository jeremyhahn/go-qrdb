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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errStorageFault is the fault faultBackend injects.
var errStorageFault = errors.New("injected storage fault")

// faultBackend is a storage backend that fails the writes its predicates
// select, standing in for a disk that fails mid-operation. Everything else
// is the real in-memory backend.
type faultBackend struct {
	seal.StorageBackend
	mu         sync.Mutex
	failPut    func(key string) bool
	failDelete func(key string) bool
}

func (f *faultBackend) Put(ctx context.Context, key string, value []byte) error {
	f.mu.Lock()
	fail := f.failPut != nil && f.failPut(key)
	f.mu.Unlock()
	if fail {
		return errStorageFault
	}
	return f.StorageBackend.Put(ctx, key, value)
}

func (f *faultBackend) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	fail := f.failDelete != nil && f.failDelete(key)
	f.mu.Unlock()
	if fail {
		return errStorageFault
	}
	return f.StorageBackend.Delete(ctx, key)
}

func (f *faultBackend) set(put, del func(string) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPut, f.failDelete = put, del
}

// newFaultHolderEnv is newHolderEnv over a faultBackend.
func newFaultHolderEnv(t *testing.T, threshold, total int) (*holderEnv, *faultBackend) {
	t.Helper()
	fb := &faultBackend{StorageBackend: seal.NewMemoryBackend()}
	e := &holderEnv{t: t, backend: fb, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, fb, threshold, total)
	e.svc = e.newService(e.barrier)
	return e, fb
}

// unsealBlob reports whether key is the unseal domain's wrapped-key blob.
func unsealBlob(key string) bool {
	return strings.HasSuffix(key, ".wrapped") && !strings.HasSuffix(key, ".recovery.wrapped")
}

// TestRedistribute_FailedPersistLeavesThePreviousEnvelopes: a redistribution
// seals the new envelopes before it persists the blob they belong to. When
// the persist fails, the previous blob, and so the previous share set, stays
// in force. The envelopes of that set nobody has claimed yet must stay too,
// and none of the new set, for a key that was never stored, may be handed
// out.
func TestRedistribute_FailedPersistLeavesThePreviousEnvelopes(t *testing.T) {
	ctx := context.Background()
	e, fb := newFaultHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.register(seal.DomainUnseal, a, b, c)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)
	e.claim(seal.DomainUnseal, a)
	e.claim(seal.DomainUnseal, b)

	fb.set(unsealBlob, nil)
	_, err = e.barrier.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{Threshold: 2})
	require.NoError(t, err)
	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, a, "")
	require.NoError(t, err)
	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, b, "")
	require.ErrorIs(t, err, errStorageFault, "the rekey's persist failed")
	fb.set(nil, nil)

	// carol's envelope is still the one for the blob in force: alice and bob
	// seal with their shares, and alice and carol unseal.
	e.claim(seal.DomainUnseal, c)
	for _, h := range []*testHolder{a, b} {
		_, err := e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpSeal, h, "")
		require.NoError(t, err, h.username)
	}
	require.True(t, e.barrier.IsSealed())
	for _, h := range []*testHolder{a, c} {
		_, err := e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpUnseal, h, "")
		require.NoError(t, err, h.username)
	}
	require.False(t, e.barrier.IsSealed(), "carol's envelope opens the blob in force")
}

// TestRedistribute_RevokedHolderStaysRevoked: a revocation's blob is
// persisted before the revoked holder's record is deleted. If the delete
// fails, the record survives, but the holder is gone from the roster in
// force. The next distribution must not deal them a share again: a holder
// who took part in an earlier distribution and is absent from the current
// roster was removed.
func TestRedistribute_RevokedHolderStaysRevoked(t *testing.T) {
	ctx := context.Background()
	e, fb := newFaultHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.initShamir("", a, b, c)

	fb.set(nil, func(key string) bool { return strings.HasSuffix(key, "/officers/carol") })
	_, err := e.svc.RevokeOfficer(ctx, seal.DomainUnseal, "carol", 2)
	require.NoError(t, err)
	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, a, "")
	require.NoError(t, err)
	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, b, "")
	require.ErrorIs(t, err, errStorageFault, "the record delete failed after the blob was stored")
	fb.set(nil, nil)

	current, _, err := e.barrier.HolderRoster(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	_, inForce := current.Holder("carol")
	require.False(t, inForce, "the revocation's roster is in force")

	// A holder enrolled since still joins the next distribution; carol does not.
	d := newHolder(t, "dave", "")
	e.register(seal.DomainUnseal, d)
	next, err := e.svc.PrepareRoster(ctx, seal.DomainUnseal, 2, "", nil)
	require.NoError(t, err)
	_, dealt := next.Holder("carol")
	require.False(t, dealt, "a revoked holder is never dealt a share again")
	_, joined := next.Holder("dave")
	require.True(t, joined, "a newly enrolled holder joins")
	require.Equal(t, 3, next.Total)
}
