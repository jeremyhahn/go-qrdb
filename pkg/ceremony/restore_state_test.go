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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errRestoreStateRead is the read failure restoreStateFailBackend injects for
// every key under its barrier's own RootKeyPath (but not the root key path
// itself, so construction's own reads are unaffected) -- the two reads
// restoreState makes: CeremonyOperational's marker and Initialized's
// metadata, both namespaced under RootKeyPath.
var errRestoreStateRead = errors.New("ceremony test: injected restore-state read failure")

// restoreStateFailBackend fails every Get under rootKeyPath except the bare
// root key path itself, so a barrier built over it answers
// CeremonyOperational/Initialized with a real error instead of "not found".
type restoreStateFailBackend struct {
	seal.StorageBackend
	rootKeyPath string
}

func (b *restoreStateFailBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if key != b.rootKeyPath && strings.HasPrefix(key, b.rootKeyPath) {
		return nil, errRestoreStateRead
	}
	return b.StorageBackend.Get(ctx, key)
}

// restoreStateFailBarrier builds a sealed *seal.StorageBarrier whose
// CeremonyOperational and Initialized reads both fail, so restoreState (and
// anything built over it) observes a genuine read failure rather than a
// clean "false".
func restoreStateFailBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	const rootKeyPath = "test/root-key"
	backend := &restoreStateFailBackend{StorageBackend: seal.NewMemoryBackend(), rootKeyPath: rootKeyPath}
	b, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{RootKeyPath: rootKeyPath}, seal.NewNoneStrategy())
	require.NoError(t, err)
	return b
}

// TestNewCeremonyService_RestoreReadFailure_FailsClosed is the red/green
// proof for the re-review's restoreState fail-closed fix: before it,
// restoreState treated ANY read failure identically to "no record, no
// error" and returned StateAwaitingInit, so a process that could not tell
// whether an earlier run already completed the ceremony would let
// Initialize run anyway. StateRestoreFailed is correct here because the
// barrier's own marker reads are failing, not merely slow -- restoreState
// does not retry.
func TestNewCeremonyService_RestoreReadFailure_FailsClosed(t *testing.T) {
	barrier := restoreStateFailBarrier(t)
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	require.Equal(t, StateRestoreFailed, svc.State(),
		"a barrier whose own state reads fail must construct into StateRestoreFailed, never StateAwaitingInit")
}

// TestInitialize_RefusesWhenRestoreFailed proves Initialize refuses to run
// at all against StateRestoreFailed -- treating it as "fresh, go ahead"
// (StateAwaitingInit's behavior) could double-initialize a barrier an
// earlier, successful run already brought up, since this process genuinely
// does not know which case it is in.
func TestInitialize_RefusesWhenRestoreFailed(t *testing.T) {
	barrier := restoreStateFailBarrier(t)
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, barrier)
	require.Equal(t, StateRestoreFailed, svc.State())

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{SOPin: "123456", UserPin: "654321", AdminGenerate: true})
	require.ErrorIs(t, err, ErrRestoreFailed)
}

// TestReissueAdminCredential_RefusesWhenRestoreFailed proves
// ReissueAdminCredential refuses the same way: before this fix, its guard
// only checked for StateAwaitingInit, so StateRestoreFailed fell through to
// the live credential-issuance path on a ceremony whose actual state this
// process could not determine.
func TestReissueAdminCredential_RefusesWhenRestoreFailed(t *testing.T) {
	barrier := restoreStateFailBarrier(t)
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, barrier)
	require.Equal(t, StateRestoreFailed, svc.State())

	_, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{SOPin: "123456"})
	require.ErrorIs(t, err, ErrRestoreFailed)
}
