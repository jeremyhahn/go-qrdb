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
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Encryption migration (enable-encryption)
//
// These re-exports let an embedding consumer drive Node.EnableEncryption —
// migrating a previously-unencrypted (none-strategy) barrier to a keyed
// strategy in place — without importing go-qrdb's internal pkg/seal or
// pkg/service packages. They mirror the seal.MigrationOptions and
// service.BarrierScope types the node entrypoint accepts.
// ---------------------------------------------------------------------------

// MigrationOptions describes a request to enable encryption on a none-strategy
// barrier by migrating its at-rest data to a keyed strategy. (Aliases
// pkg/seal.MigrationOptions.)
type MigrationOptions = seal.MigrationOptions

// MigrationMechanism selects the physical re-encryption approach for a
// migration. (Aliases pkg/seal.MigrationMechanism.)
type MigrationMechanism = seal.MigrationMechanism

// Migration mechanism constants.
const (
	// MechanismSwap copies the plaintext store into a keyed staging store and
	// atomically swaps it in — crash-safe at ~2x peak disk.
	MechanismSwap = seal.MechanismSwap
	// MechanismInPlace re-encrypts each store file in place with a per-file
	// resume journal — lower peak disk, more delicate crash-safety.
	MechanismInPlace = seal.MechanismInPlace
)

// MigrationAvailability selects how the node serves writes while a migration
// runs. (Aliases pkg/seal.MigrationAvailability.)
type MigrationAvailability = seal.MigrationAvailability

// Migration availability constants.
const (
	// AvailabilityQuiesced blocks writes for the duration of the migration.
	AvailabilityQuiesced = seal.AvailabilityQuiesced
	// AvailabilityOnline keeps serving writes and replays the delta before commit.
	AvailabilityOnline = seal.AvailabilityOnline
)

// BarrierScope names the barrier an encryption migration targets. (Aliases
// pkg/service.BarrierScope.)
type BarrierScope = service.BarrierScope

// BarrierScopeKind identifies which barrier an encryption migration targets.
// (Aliases pkg/service.BarrierScopeKind.)
type BarrierScopeKind = service.BarrierScopeKind

// Barrier scope constants.
const (
	// ScopeSystem targets the node/system barrier (admin authority).
	ScopeSystem = service.ScopeSystem
	// ScopeTenant targets a single tenant's dedicated barrier.
	ScopeTenant = service.ScopeTenant
)

// EncryptionMigrator enables at-rest encryption on a barrier after the fact
// (none/system-managed → keyed) for a scope. The embedded *node.Node satisfies
// it, so a composing service (e.g. go-quicbaas) can drive enable-encryption
// in-process by delegating to the node rather than the embedded BarrierClient
// transport (which does not implement it). (Aliases pkg/service.EncryptionMigrator.)
type EncryptionMigrator = service.EncryptionMigrator
