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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/volume"

// ---------------------------------------------------------------------------
// Volume seams
//
// A volume pool's storage comes from a VolumeProvider, and a lockable volume's
// key from a VolumeKeyProvider. An embedder with its own block devices or key
// custody implements these and passes them to node.WithVolumeProvider and
// node.WithVolumeKeyProvider. (Aliases pkg/volume.)
// ---------------------------------------------------------------------------

// VolumeProvider opens the volume that backs a pool on this node. (Aliases
// pkg/volume.Provider.)
type VolumeProvider = volume.Provider

// VolumeSpec names the volume a VolumeProvider opens: its pool, node, path
// and provider-specific options. (Aliases pkg/volume.Spec.)
type VolumeSpec = volume.Spec

// Volume is an opened volume: its root, usage, health and Close. (Aliases
// pkg/volume.Volume.)
type Volume = volume.Volume

// VolumeUsage is a volume's capacity. (Aliases pkg/volume.Usage.)
type VolumeUsage = volume.Usage

// VolumeHealth is a volume's state with its detail and the time it was
// checked. (Aliases pkg/volume.Health.)
type VolumeHealth = volume.Health

// VolumeState is a volume's readiness. (Aliases pkg/volume.State.)
type VolumeState = volume.State

// Volume states.
const (
	// VolumeStateReady says the volume serves reads and writes.
	VolumeStateReady = volume.StateReady
	// VolumeStateLocked says the volume's data is unreadable until unlocked.
	VolumeStateLocked = volume.StateLocked
	// VolumeStateUnavailable says the volume cannot be used.
	VolumeStateUnavailable = volume.StateUnavailable
)

// VolumeLockable is the capability a Volume may additionally offer: its data
// made readable with a VolumeKey and unreadable again. (Aliases
// pkg/volume.Lockable.)
type VolumeLockable = volume.Lockable

// VolumeKeyProvider returns the key that unlocks a node's volume on a pool,
// without saying where the key is held. (Aliases pkg/volume.KeyProvider.)
type VolumeKeyProvider = volume.KeyProvider

// VolumeKey is a 32-byte volume key; the caller wipes it after use. (Aliases
// pkg/volume.VolumeKey.)
type VolumeKey = volume.VolumeKey

// ---------------------------------------------------------------------------
// Volume errors a provider returns
// ---------------------------------------------------------------------------

// VolumeInvalidSpecError says a VolumeSpec failed the validation common to
// every provider. (Aliases pkg/volume.InvalidSpecError.)
type VolumeInvalidSpecError = volume.InvalidSpecError

// VolumeOpenError says a volume cannot be made available on this node.
// (Aliases pkg/volume.OpenError.)
type VolumeOpenError = volume.OpenError

// VolumeUsageError says a volume's capacity query failed. (Aliases
// pkg/volume.UsageError.)
type VolumeUsageError = volume.UsageError

// VolumeClosedError says a call was made on a volume after Close. (Aliases
// pkg/volume.ClosedVolumeError.)
type VolumeClosedError = volume.ClosedVolumeError
