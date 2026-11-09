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

package store

import (
	"errors"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// barrierSealed reports whether err is an at-rest barrier refusing to encrypt
// because it is sealed: qrdb's StorageBarrier, or the go-quicraft
// EpochBarrier under it, which a closing quicraft Host seals on its own; or a
// storage package's refusal of the same seal, which cannot carry pkg/seal's
// sentinels: a sealing barrier's closed quiesce gate, or Pebble's sealed
// at-rest barrier.
func barrierSealed(err error) bool {
	return errors.Is(err, seal.ErrSealed) || errors.Is(err, seal.ErrBarrierSealed) ||
		errors.Is(err, quiesce.ErrSealing) || errors.Is(err, pebbleStorage.ErrBarrierSealed)
}

// openPebble opens the state machine database in dir with a SealWatch
// attached, so that a barrier sealed under its encrypting filesystem ends the
// state machine's flushes with a typed error rather than a wait that never
// returns, and Pebble's retries log once rather than once per attempt. opts is
// cloned, so a state machine reopened over the same options carries one watch.
func openPebble(dir string, opts *pebble.Options) (*pebble.DB, *pebbleStorage.SealWatch, error) {
	return pebbleStorage.Open(dir, opts, barrierSealed)
}
