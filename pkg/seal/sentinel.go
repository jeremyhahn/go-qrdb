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
	"encoding/binary"
	"errors"
	"fmt"
)

// CurrentFormatVersion is the on-disk format version this build
// produces. The daemon refuses to start on data dirs that carry a
// lower (or absent) format-version sentinel, because the encfs
// at-rest encryption introduced in version 1 is not compatible with
// pre-encfs plaintext storage.
const CurrentFormatVersion uint32 = 1

// SentinelKey is the key under which the format-version sentinel is
// stored in the seal sub-store. The sentinel lives in the seal
// sub-store rather than the data plane because the seal sub-store is
// always plaintext (it holds the wrapped barrier root key and must
// be readable before the barrier is unsealed).
const SentinelKey = "__sys/barrier_format_version"

// ErrLegacyDataDir is returned when the daemon detects an existing
// seal sub-store that lacks a format-version sentinel, indicating
// the data dir was created by a pre-encfs build that wrote
// plaintext sstables. There is no in-place upgrade; operators must
// migrate data through application-level export/import.
var ErrLegacyDataDir = errors.New("qrdb/seal: data dir was created with a pre-barrier-refactor version; in-place upgrade is not supported")

// UnsupportedFormatVersionError is returned when the sentinel is
// present but carries a version number this build does not know how
// to read. This typically means the operator deployed an older
// binary against a data dir produced by a newer one.
type UnsupportedFormatVersionError struct {
	OnDisk    uint32
	Supported uint32
}

func (e *UnsupportedFormatVersionError) Error() string {
	return fmt.Sprintf("qrdb/seal: on-disk format version %d is newer than supported version %d", e.OnDisk, e.Supported)
}

// WriteFormatSentinel writes the current format-version sentinel to
// the supplied storage backend. Intended to be called once during
// the first boot of a fresh data dir, immediately after the seal
// barrier has been constructed.
func WriteFormatSentinel(ctx context.Context, store StorageBackend) error {
	if store == nil {
		return errors.New("qrdb/seal: storage backend is nil")
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], CurrentFormatVersion)
	return store.Put(ctx, SentinelKey, buf[:])
}

// ReadFormatSentinel reads and validates the format-version sentinel
// from the supplied storage backend. The returned error is one of:
//
//   - nil if the sentinel is present and matches CurrentFormatVersion;
//   - ErrLegacyDataDir if the sentinel is absent;
//   - *UnsupportedFormatVersionError if the sentinel is present but
//     carries a version greater than CurrentFormatVersion.
//
// A version equal to CurrentFormatVersion succeeds. A future build
// that introduces a higher version will need to also migrate the
// "supports reading version N" check.
func ReadFormatSentinel(ctx context.Context, store StorageBackend) error {
	if store == nil {
		return errors.New("qrdb/seal: storage backend is nil")
	}
	value, err := store.Get(ctx, SentinelKey)
	if err != nil {
		// The seal storage backend returns nil, ErrSecretNotFound when a
		// key is absent; anything else (I/O failure, corruption) bubbles
		// up so the operator sees the real fault.
		if errors.Is(err, ErrSecretNotFound) {
			return ErrLegacyDataDir
		}
		return err
	}
	if value == nil {
		return ErrLegacyDataDir
	}
	if len(value) != 4 {
		return &SentinelLengthError{Length: len(value)}
	}
	onDisk := binary.LittleEndian.Uint32(value)
	if onDisk > CurrentFormatVersion {
		return &UnsupportedFormatVersionError{OnDisk: onDisk, Supported: CurrentFormatVersion}
	}
	return nil
}

// EnsureFormatSentinel reads the sentinel, writing the current
// version if the seal sub-store is genuinely empty (sealStoreEmpty
// is true). On a non-empty store with no sentinel it returns
// ErrLegacyDataDir to refuse the start. This is the helper the
// daemon's boot sequence calls.
func EnsureFormatSentinel(ctx context.Context, store StorageBackend, sealStoreEmpty bool) error {
	err := ReadFormatSentinel(ctx, store)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrLegacyDataDir) {
		return err
	}
	if !sealStoreEmpty {
		return ErrLegacyDataDir
	}
	return WriteFormatSentinel(ctx, store)
}

// SentinelLengthError is returned when the format-version sentinel value has
// an unexpected byte length (it must be exactly 4 bytes).
type SentinelLengthError struct {
	Length int
}

func (e *SentinelLengthError) Error() string {
	return fmt.Sprintf("qrdb/seal: sentinel has unexpected length %d, want 4", e.Length)
}
