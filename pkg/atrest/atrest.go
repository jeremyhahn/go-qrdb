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

// Package atrest is the single authority for turning any store into an
// at-rest-encrypted store using the node seal barrier. It owns Layer-1
// at-rest filesystem encryption only — no tenant or data-plane knowledge.
//
// All qrdb stores (MVCC state machine, vector storage, standalone time-series,
// the file backend) obtain their encrypting filesys.FS from one Encryptor
// instance, eliminating the duplicated per-store encfs wiring that previously
// lived in pkg/store, pkg/storage/vector, and pkg/timeseries. A Pebble store
// reaches it through pkg/storage/pebble's WrapVFS; atrest itself imports no
// Pebble package.
//
// Usage:
//
//	enc := atrest.New(sealBarrier)          // nil barrier → plaintext (no-op)
//	store.WithEncryptor(enc)(pOpts)         // MVCC / pebble SM factory
//	vectorstorage.WithEncryptor(enc)(pOpts) // vector storage
//	timeseries.WithEncryptor(enc)(pOpts)    // standalone time-series storage
package atrest

import (
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// Encryptor wraps any filesys.FS with barrier-driven AES-256-GCM encryption.
// A nil Encryptor or an Encryptor whose barrier is nil is a deliberate no-op,
// preserving plaintext operation for tests and non-encrypted deployments.
type Encryptor struct {
	barrier *seal.StorageBarrier
	baseFS  filesys.FS
}

// New returns an Encryptor that uses barrier as the key-encryption-key. The
// base FS defaults to filesys.Default. A nil barrier produces a valid, no-op
// Encryptor so callers do not need to nil-check before passing it around.
func New(barrier *seal.StorageBarrier) *Encryptor {
	return &Encryptor{
		barrier: barrier,
		baseFS:  filesys.Default,
	}
}

// Enabled reports whether the Encryptor has a non-nil barrier and will
// actually wrap the FS. When false, Wrap returns its argument and FS returns
// the base FS.
func (e *Encryptor) Enabled() bool {
	return e != nil && e.barrier != nil
}

// FS returns the encrypting filesys.FS over the base FS. When the Encryptor
// is disabled (nil receiver or nil barrier), the base FS is returned so
// callers that need a concrete filesys.FS always get a usable value.
func (e *Encryptor) FS() filesys.FS {
	if e == nil {
		return filesys.Default
	}
	return e.Wrap(e.baseFS)
}

// Wrap returns base wrapped in the barrier-driven encrypting FS, or base
// itself when the Encryptor is disabled. It is how every store — the file
// backend directly, a Pebble store through pkg/storage/pebble's WrapVFS —
// writes through the same encryption.
func (e *Encryptor) Wrap(base filesys.FS) filesys.FS {
	if !e.Enabled() {
		return base
	}
	return encfs.New(base, e.barrier)
}
