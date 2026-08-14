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
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// WithEncryptingFS wraps the Pebble vfs.FS with seal/encfs so that
// every file the database writes — sstables, WAL, manifest,
// snapshots — is transparently encrypted using the supplied barrier
// as the key-encryption-key. The barrier MUST already be unsealed
// when the state machine factory is invoked; if it is sealed, file
// open and creation operations return errors wrapping
// seal.ErrSealed.
//
// If barrier is nil this is a no-op so existing tests and
// non-encrypted deployments continue to work unchanged.
//
// Deprecated: prefer WithEncryptor(atrest.New(barrier)) which routes
// through the single atrest.Encryptor authority. WithEncryptingFS is
// kept for backward compatibility.
func WithEncryptingFS(barrier *seal.StorageBarrier) Option {
	return WithEncryptor(atrest.New(barrier))
}

// WithEncryptor applies enc to the Pebble options, wrapping the vfs.FS
// with barrier-driven AES-256-GCM encryption. A nil enc or an enc whose
// barrier is nil is a no-op, so plaintext deployments and tests are
// unaffected.
func WithEncryptor(enc *atrest.Encryptor) Option {
	return func(o *pebble.Options) {
		enc.Apply(o)
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
// useful for wiring store.WithEncryptingFS so that the data-plane
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
