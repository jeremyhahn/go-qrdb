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

package store

import (
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Option configures the underlying PebbleDB database for state machine factories.
type Option func(*pebble.Options)

// WithCacheSize sets the PebbleDB block cache size in bytes.
func WithCacheSize(size int64) Option {
	return func(o *pebble.Options) {
		o.Cache = pebble.NewCache(size)
	}
}

// WithWALDir sets a separate directory for the PebbleDB WAL.
func WithWALDir(dir string) Option {
	return func(o *pebble.Options) {
		o.WALDir = dir
	}
}

// WithMaxOpenFiles sets the maximum number of open files for PebbleDB.
func WithMaxOpenFiles(n int) Option {
	return func(o *pebble.Options) {
		o.MaxOpenFiles = n
	}
}

// WithEncryptor applies enc to the Pebble options, wrapping their FS with
// barrier-driven AES-256-GCM encryption through pkg/storage/pebble's
// adapter. A nil enc or an enc whose barrier is nil is a no-op, so
// plaintext deployments and tests are unaffected.
func WithEncryptor(enc *atrest.Encryptor) Option {
	return func(o *pebble.Options) {
		if enc.Enabled() {
			o.FS = pebblestore.WrapVFS(o.FS, enc.Wrap)
		}
	}
}

// ---------------------------------------------------------------------------
// MVCCOption -- configures an MVCCStateMachine
// ---------------------------------------------------------------------------

// MVCCOption configures an MVCCStateMachine created by NewMVCCCreateFuncWith.
type MVCCOption func(*MVCCStateMachine)

// WithLessor attaches a lease.Lessor to the MVCC state machine. The lessor
// manages lease lifecycle (grant/revoke/keep-alive) and key-lease bindings.
func WithLessor(lr *lease.Lessor) MVCCOption {
	return func(s *MVCCStateMachine) {
		s.SetLessor(lr)
	}
}

// WithLogs attaches a log registry (vlog.Chain) of its own to every MVCC
// state machine the create function builds. It is the one way a create
// function attaches logs: the daemon, the per-tenant data plane and the SDK's
// clustered node all compose it.
//
// The chain is built per machine, never shared: Chain.Restore replaces the
// whole head table with what one state machine's storage holds, so a chain
// shared by two Raft groups on a node would number one group's events after
// the other's and let either group's open or recovery overwrite the other's
// heads.
//
// Every state machine the registry builds for a given sm_type MUST receive
// this attachment (it mirrors WithLessor): a chain conditioned on per-node
// configuration would let one replica apply protocol.OpAuditAppend or
// protocol.OpVerifiableLog while a sibling replica of the same Raft group
// refuses it for want of a registry, which is state divergence, not a
// degraded feature. The featuregate.AuditAppend and featuregate.VerifiableLog
// gates -- not this attachment -- are what control whether the opcodes are
// ever proposed.
func WithLogs() MVCCOption {
	return func(s *MVCCStateMachine) {
		s.SetLogs(vlog.NewChain())
	}
}

// WithWatchHub attaches a watch.Hub to the MVCC state machine. When set,
// the state machine emits watch notifications after each committed batch.
// The hub instance must be shared with the WatchService to deliver events.
func WithWatchHub(hub *watch.Hub) MVCCOption {
	return func(s *MVCCStateMachine) {
		s.SetWatchHub(hub)
	}
}

// WithTimeSeriesEnabled instructs the MVCC state machine to create a
// time-series storage engine when the database is opened. The storage
// is backed by the same PebbleDB instance as the MVCC store.
func WithTimeSeriesEnabled() MVCCOption {
	return func(s *MVCCStateMachine) {
		s.enableTimeSeries = true
	}
}

// WithNoSync disables fsync on PebbleDB batch commits. When set, the state
// machine's Update does not wait for OS-level write synchronisation, trading
// durability for performance. This is appropriate only for testing environments
// where Docker overlay2 fsync latency would otherwise cause Raft proposal
// timeouts. Never use in production.
func WithNoSync() MVCCOption {
	return func(s *MVCCStateMachine) {
		s.noSync = true
	}
}

// ---------------------------------------------------------------------------
// Factory functions
// ---------------------------------------------------------------------------

// NewMVCCCreateFunc returns a sm.CreateFunc that creates MVCCStateMachine
// instances. The MVCC state machine provides versioned key-value storage
// with revision tracking, compare-and-swap transactions, and compaction.
// Options configure the underlying PebbleDB database.
func NewMVCCCreateFunc(opts ...Option) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
		pOpts := &pebble.Options{}
		for _, opt := range opts {
			opt(pOpts)
		}
		return NewMVCCStateMachine(pOpts)
	})
}

// NewMVCCCreateFuncWith returns a sm.CreateFunc that creates
// MVCCStateMachine instances with subsystem configuration. pebbleOpts
// configure the underlying PebbleDB database. mvccOpts configure the
// MVCCStateMachine subsystems (lessor, watch hub, time-series).
func NewMVCCCreateFuncWith(pebbleOpts []Option, mvccOpts ...MVCCOption) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
		pOpts := &pebble.Options{}
		for _, opt := range pebbleOpts {
			opt(pOpts)
		}
		s := NewMVCCStateMachine(pOpts)
		for _, opt := range mvccOpts {
			opt(s)
		}
		return s
	})
}

// PebbleConfigurator mutates a fully-built *PebbleStateMachine after the
// pebble.Options have been applied. It exposes the same kind of
// dependency-injection seam that NewMVCCCreateFuncWith does for the
// MVCC state machine, so callers can wire shared subsystems (lessor,
// watch hub, time-series) into the pebble state machine factory.
type PebbleConfigurator func(*PebbleStateMachine)

// NewPebbleCreateFuncWith returns a sm.CreateFunc that creates
// PebbleStateMachine instances with both pebble-level options and
// post-construction configurators applied. The pebble Options are
// useful for wiring store.WithEncryptor so that the data-plane
// Pebble instance is opened on top of the seal/encfs vfs.
func NewPebbleCreateFuncWith(pebbleOpts []Option, configurators ...PebbleConfigurator) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
		pOpts := &pebble.Options{}
		for _, opt := range pebbleOpts {
			opt(pOpts)
		}
		s := NewPebbleStateMachine(pOpts)
		for _, cfg := range configurators {
			cfg(s)
		}
		return s
	})
}
