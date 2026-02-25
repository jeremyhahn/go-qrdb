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
)

// Option configures a PebbleStore created by NewPebbleCreateFunc.
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

// NewPebbleCreateFunc returns a sm.CreateFunc that creates PebbleStore instances.
// Options configure the underlying PebbleDB database.
func NewPebbleCreateFunc(opts ...Option) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
		pOpts := &pebble.Options{}
		for _, opt := range opts {
			opt(pOpts)
		}
		return NewPebbleStore(pOpts)
	})
}

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
