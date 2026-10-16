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

package seal

import (
	"context"
	"time"
)

// ceremonyOperationalSuffix names, under the barrier's root-key path, the
// record that the initialization ceremony this barrier serves has reached
// operational.
const ceremonyOperationalSuffix = ".ceremony-operational"

func (b *StorageBarrier) ceremonyOperationalPath() string {
	return b.config.RootKeyPath + ceremonyOperationalSuffix
}

// MarkCeremonyOperational records, in the barrier's plaintext base store, that
// the initialization ceremony has reached operational. Like the root-key blob
// and the barrier metadata, the record is readable while the barrier is
// sealed, which is the point: the boot-time sealed-window listener reads it
// (CeremonyOperational) to close the anonymous officer-enrollment exemption
// after a restart, when the ceremony's in-memory state is gone. The record
// holds only the time it was written. Writing it again is harmless.
func (b *StorageBarrier) MarkCeremonyOperational(ctx context.Context) error {
	return b.base.Put(ctx, b.ceremonyOperationalPath(), []byte(time.Now().UTC().Format(time.RFC3339Nano)))
}

// CeremonyOperational reports whether MarkCeremonyOperational has been
// recorded for this barrier. An absent record is false with no error; any
// other read failure is returned, and a caller gating on the answer must
// treat it as operational.
func (b *StorageBarrier) CeremonyOperational(ctx context.Context) (bool, error) {
	blob, err := b.base.Get(ctx, b.ceremonyOperationalPath())
	if err != nil {
		if isSecretNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return len(blob) > 0, nil
}

// Initialized reports whether the barrier has been initialized, reading the
// persisted metadata when this process has not initialized it itself, so it
// answers correctly on a sealed barrier after a restart. Unlike Status, which
// reports a metadata read failure as "never initialized", it returns the
// error, so a caller gating on the answer can fail closed. A successful read
// is remembered for InitializedCached.
func (b *StorageBarrier) Initialized(ctx context.Context) (bool, error) {
	if v := b.initializedAt.Load(); v != nil {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			return true, nil
		}
	}
	t, err := b.loadInitializedAt(ctx)
	if err != nil {
		return false, err
	}
	if t.IsZero() {
		b.uninitializedRead.Store(true)
		return false, nil
	}
	b.initializedAt.Store(t)
	return true, nil
}

// InitializedCached reports whether the barrier is initialized from memory
// alone, never reading the store, and whether it knows: known once this
// process has initialized the barrier, or has read its metadata through
// Initialized or Status. It is the answer a sealed node gives on its sealed
// surface (service.BarrierService.BarrierStatus), so a composer that serves
// that surface reads Initialized once at boot
// (server.NewSealedWindowListenerProbe does).
func (b *StorageBarrier) InitializedCached() (initialized, known bool) {
	if v := b.initializedAt.Load(); v != nil {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			return true, true
		}
	}
	return false, b.uninitializedRead.Load()
}
