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

import "github.com/jeremyhahn/go-qrdb/pkg/statemachine"

// ---------------------------------------------------------------------------
// State machine backend interface
// ---------------------------------------------------------------------------

// SMBackend is the storage interface used by state machine implementations.
type SMBackend = statemachine.Backend

// ---------------------------------------------------------------------------
// State machine implementations
// ---------------------------------------------------------------------------

// FastStateMachine is the high-performance sharded state machine with
// write-through caching, supporting PrefixQuery lookups required by Scan.
type FastStateMachine = statemachine.FastStateMachine

// ConcurrentBackend is a sharded in-memory state machine backend.
type ConcurrentBackend = statemachine.ConcurrentBackend

// StandardBackend is a simple single-lock in-memory state machine backend.
type StandardBackend = statemachine.StandardBackend

// DiskBackend is a disk-backed state machine backend.
type DiskBackend = statemachine.DiskBackend

// FastBackend is a high-performance sharded state machine backend.
type FastBackend = statemachine.FastBackend

// KVStateMachine is the generic KV state machine that delegates to a Backend.
type KVStateMachine = statemachine.KVStateMachine

// ---------------------------------------------------------------------------
// State machine options
// ---------------------------------------------------------------------------

// SMOption configures a FastStateMachine at construction time.
type SMOption = statemachine.Option

// WithWatchManager configures a WatchManager for the state machine.
var WithWatchManager = statemachine.WithWatchManager

// WithSMLogger configures a logger for the state machine.
var WithSMLogger = statemachine.WithLogger

// WithIndexManager configures an IndexManager for the state machine.
var WithIndexManager = statemachine.WithIndexManager

// WithLeaseManager configures a LeaseManager for the state machine.
var WithLeaseManager = statemachine.WithLeaseManager

// WithTimeSeriesStore configures a TimeSeriesStore for the state machine.
var WithTimeSeriesStore = statemachine.WithTimeSeriesStore

// WithMVCCStore configures an MVCCStore for the state machine.
var WithMVCCStore = statemachine.WithMVCCStore

// ---------------------------------------------------------------------------
// State machine type interfaces
// ---------------------------------------------------------------------------

// WatchManager is the interface for event notification.
type WatchManager = statemachine.WatchManager

// PrefixQuery is a lookup query that scans by key prefix.
type PrefixQuery = statemachine.PrefixQuery

// LeaseManager is the interface for distributed lease operations.
type LeaseManager = statemachine.LeaseManager

// IndexManager is the interface for secondary index operations.
type IndexManager = statemachine.IndexManager

// TimeSeriesStore is the interface for time-series storage.
type TimeSeriesStore = statemachine.TimeSeriesStore

// MVCCStore is the interface for MVCC operations.
type MVCCStore = statemachine.MVCCStore

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// SMFactory creates state machine CreateFunc instances.
type SMFactory = statemachine.Factory

// SMPersistenceType identifies the persistence mechanism.
type SMPersistenceType = statemachine.PersistenceType

// SMPersistenceType constants.
const (
	SMPersistenceMemory = statemachine.PersistenceMemory
	SMPersistenceDisk   = statemachine.PersistenceDisk
	SMPersistenceHybrid = statemachine.PersistenceHybrid
)

// SMKind identifies the state machine kind.
type SMKind = statemachine.SMKind

// SMKind constants.
const (
	SMKindRegular    = statemachine.SMKindRegular
	SMKindDisk       = statemachine.SMKindDisk
	SMKindConcurrent = statemachine.SMKindConcurrent
)

// SMCapability identifies a state machine capability.
type SMCapability = statemachine.Capability

// SMCapability constants.
const (
	SMCapKV          = statemachine.CapabilityKV
	SMCapMVCC        = statemachine.CapabilityMVCC
	SMCapTimeSeries  = statemachine.CapabilityTimeSeries
	SMCapVector      = statemachine.CapabilityVector
	SMCapIndex       = statemachine.CapabilityIndex
	SMCapLease       = statemachine.CapabilityLease
	SMCapWatch       = statemachine.CapabilityWatch
	SMCapTransaction = statemachine.CapabilityTransaction
	SMCapBatch       = statemachine.CapabilityBatch
)

// SMDescriptor describes a registered state machine.
type SMDescriptor = statemachine.Descriptor

// SMRegistry manages state machine factory registrations.
type SMRegistry = statemachine.Registry

// NewSMRegistry creates a new state machine Registry.
var NewSMRegistry = statemachine.NewRegistry

// ---------------------------------------------------------------------------
// State machine error types
// ---------------------------------------------------------------------------

// SMInvalidOpcodeError is returned when an invalid opcode is received.
type SMInvalidOpcodeError = statemachine.InvalidOpcodeError

// SMIncompleteDataError is returned when command data is incomplete.
type SMIncompleteDataError = statemachine.IncompleteDataError

// SMSnapshotError is returned when a snapshot operation fails.
type SMSnapshotError = statemachine.SnapshotError

// SMStorageError is returned when a storage operation fails.
type SMStorageError = statemachine.StorageError

// SMServiceNotConfiguredError is returned when a required service is not configured.
type SMServiceNotConfiguredError = statemachine.ServiceNotConfiguredError

// SMUnknownSMTypeError is returned when an unknown state machine type is requested.
type SMUnknownSMTypeError = statemachine.UnknownSMTypeError

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewFastStateMachine creates a new FastStateMachine.
var NewFastStateMachine = statemachine.NewFastStateMachine

// NewConcurrentBackend creates a new ConcurrentBackend.
var NewConcurrentBackend = statemachine.NewConcurrentBackend

// NewStandardBackend creates a new StandardBackend.
var NewStandardBackend = statemachine.NewStandardBackend

// NewDiskBackend creates a new DiskBackend.
var NewDiskBackend = statemachine.NewDiskBackend

// NewFastBackend creates a new FastBackend.
var NewFastBackend = statemachine.NewFastBackend

// NewKVStateMachine creates a new KVStateMachine.
var NewKVStateMachine = statemachine.NewKVStateMachine
