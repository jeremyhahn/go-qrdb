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

import (
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// ---------------------------------------------------------------------------
// Volume pool subsystem
//
// A dedicated-isolation tenant (IsolationBarrier / IsolationVolume /
// IsolationRaft) draws its per-tenant Pebble path from a volume pool. A
// composing service builds the subsystem from its data directory and the
// declared pool config, then attaches the resources to its BarrierRegistry via
// WithVolumePool + WithPerTenantRegistry (types_seal.go). Without an attached
// pool, RegisterTenantPooled* calls fail with ErrPoolNotConfigured.
// ---------------------------------------------------------------------------

// VolumePoolSubsystem bundles the Manager, StateStore, and PerTenantRegistry
// that back dedicated-isolation tenant storage. (Aliases
// pkg/storage/pool.VolumePoolSubsystem.) Close must be called at shutdown.
type VolumePoolSubsystem = pool.VolumePoolSubsystem

// BuildVolumePoolSubsystem constructs the volume-pool Manager, StateStore, and
// PerTenantRegistry from the data directory and declared pools. It returns
// (nil, nil) when no pools are configured. Pass clustered=true to construct the
// replicated round-robin cursor for clustered deployments. (Aliases
// pkg/storage/pool.BuildVolumePoolSubsystem.)
var BuildVolumePoolSubsystem = pool.BuildVolumePoolSubsystem
