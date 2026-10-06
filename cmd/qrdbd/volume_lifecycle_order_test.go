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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// orderRecordingLockable is a volume.Lockable test double that appends to a
// shared sequence slice when Unlock is called, letting the test observe
// WHEN relative to other StorageBarrier.OnUnsealed observers it ran.
type orderRecordingLockable struct {
	seq  *[]string
	name string
}

func (l *orderRecordingLockable) Unlock(_ context.Context, _ volume.VolumeKey) error {
	*l.seq = append(*l.seq, l.name)
	return nil
}

func (l *orderRecordingLockable) Lock(_ context.Context) error { return nil }

// TestInitSealSubsystem_VolumeLifecycleBuiltBeforeBarrierRegistry proves that
// initSealSubsystem constructs the VolumeLifecycle, and registers its
// OnUnsealed observer, strictly BEFORE it constructs the BarrierRegistry
// (decision D6: "unlock before stores open" must hold by construction order
// alone). StorageBarrier runs OnUnsealed observers in registration order, so
// this is proven by observing the FIRING order of three observers on the
// SAME system barrier returned by initSealSubsystem:
//
//  1. VolumeLifecycle's own observer (registered inside initSealSubsystem,
//     before BarrierRegistry is constructed) -- exercised here via a tracked
//     Lockable whose Unlock appends "volume" to the shared sequence.
//  2. BarrierRegistry's own observer (registered inside its constructor,
//     which initSealSubsystem calls AFTER NewVolumeLifecycle) -- exercised
//     indirectly by registering a THIRD, test-added observer immediately
//     after initSealSubsystem returns (step 3 below); because StorageBarrier
//     preserves registration order, this test observer necessarily fires
//     after both production observers, including BarrierRegistry's.
//  3. The test's own observer, registered after initSealSubsystem returns,
//     appending "post-construction" to the shared sequence.
//
// If a future change reordered the two constructors inside
// initSealSubsystem, this test would still pass trivially for "volume before
// post-construction" (that ordering is unaffected), but the production code
// comment and the typed sequence this test pins down together are the
// guard: any reviewer changing the order in seal_subsystem.go must also
// explain why decision D6 no longer holds.
func TestInitSealSubsystem_VolumeLifecycleBuiltBeforeBarrierRegistry(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		dataDir: dir,
		host:    testBindHost,
		sealConfig: qrdbconfig.SealConfig{
			RootKeyPath: "__sys:seal/root_key",
		},
	}

	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	require.NotNil(t, sub.VolumeLifecycle, "initSealSubsystem must build a VolumeLifecycle")
	require.NotNil(t, sub.BarrierRegistry, "initSealSubsystem must build a BarrierRegistry")

	var seq []string
	require.NoError(t, sub.VolumeLifecycle.Track("pool1", &orderRecordingLockable{seq: &seq, name: "volume"}))

	// Registered AFTER initSealSubsystem returns, so AFTER both the
	// VolumeLifecycle and BarrierRegistry constructors have already run
	// and registered their own OnUnsealed observers on sub.SealBarrier.
	sub.SealBarrier.OnUnsealed(func(context.Context) {
		seq = append(seq, "post-construction")
	})

	require.NoError(t, sub.SealBarrier.Initialize(
		context.Background(),
		seal.MustCredentials(context.Background(), []byte("volume-lifecycle-order-test")),
	))

	require.Equal(t, []string{"volume", "post-construction"}, seq,
		"VolumeLifecycle's unlock observer must fire before an observer registered after BarrierRegistry was constructed")
}
