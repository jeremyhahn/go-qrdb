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
	"testing"
)

func TestWriteFormatSentinel_RejectsNilStore(t *testing.T) {
	if err := WriteFormatSentinel(context.Background(), nil); err == nil {
		t.Error("expected error for nil store")
	}
}

func TestReadFormatSentinel_RejectsNilStore(t *testing.T) {
	if err := ReadFormatSentinel(context.Background(), nil); err == nil {
		t.Error("expected error for nil store")
	}
}

func TestSentinel_RoundTripCurrentVersion(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	if err := WriteFormatSentinel(ctx, store); err != nil {
		t.Fatalf("WriteFormatSentinel: %v", err)
	}
	if err := ReadFormatSentinel(ctx, store); err != nil {
		t.Errorf("ReadFormatSentinel after write: %v", err)
	}
}

func TestReadFormatSentinel_AbsentReturnsLegacy(t *testing.T) {
	store := NewMemoryBackend()
	err := ReadFormatSentinel(context.Background(), store)
	if !errors.Is(err, ErrLegacyDataDir) {
		t.Errorf("err = %v, want ErrLegacyDataDir", err)
	}
}

func TestReadFormatSentinel_FutureVersionReturnsTypedError(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], CurrentFormatVersion+1)
	if err := store.Put(ctx, SentinelKey, buf[:]); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := ReadFormatSentinel(ctx, store)
	var unsupported *ErrUnsupportedFormatVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *ErrUnsupportedFormatVersion", err)
	}
	if unsupported.OnDisk != CurrentFormatVersion+1 {
		t.Errorf("OnDisk = %d, want %d", unsupported.OnDisk, CurrentFormatVersion+1)
	}
	if unsupported.Supported != CurrentFormatVersion {
		t.Errorf("Supported = %d, want %d", unsupported.Supported, CurrentFormatVersion)
	}
	if unsupported.Error() == "" {
		t.Error("Error() returned empty string")
	}
}

func TestReadFormatSentinel_MalformedLengthRejected(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	if err := store.Put(ctx, SentinelKey, []byte{0x01}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := ReadFormatSentinel(ctx, store)
	if err == nil || errors.Is(err, ErrLegacyDataDir) {
		t.Errorf("err = %v, want non-legacy length error", err)
	}
}

func TestEnsureFormatSentinel_FirstBootEmptyWritesSentinel(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	if err := EnsureFormatSentinel(ctx, store, true); err != nil {
		t.Fatalf("EnsureFormatSentinel first boot: %v", err)
	}
	if err := ReadFormatSentinel(ctx, store); err != nil {
		t.Errorf("ReadFormatSentinel after EnsureFormatSentinel: %v", err)
	}
}

func TestEnsureFormatSentinel_LegacyNonEmptyRefused(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	// Pre-existing keys but no sentinel: simulates a pre-refactor data dir.
	if err := store.Put(ctx, "some-pre-existing-key", []byte("legacy")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := EnsureFormatSentinel(ctx, store, false)
	if !errors.Is(err, ErrLegacyDataDir) {
		t.Errorf("err = %v, want ErrLegacyDataDir", err)
	}
}

func TestEnsureFormatSentinel_PreviouslyInitializedSucceeds(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	if err := WriteFormatSentinel(ctx, store); err != nil {
		t.Fatalf("WriteFormatSentinel: %v", err)
	}
	if err := EnsureFormatSentinel(ctx, store, false); err != nil {
		t.Errorf("EnsureFormatSentinel on initialized store: %v", err)
	}
}

func TestEnsureFormatSentinel_FutureVersionPropagates(t *testing.T) {
	store := NewMemoryBackend()
	ctx := context.Background()
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], CurrentFormatVersion+1)
	if err := store.Put(ctx, SentinelKey, buf[:]); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := EnsureFormatSentinel(ctx, store, false)
	var unsupported *ErrUnsupportedFormatVersion
	if !errors.As(err, &unsupported) {
		t.Errorf("err = %v, want *ErrUnsupportedFormatVersion", err)
	}
}

func TestErrLegacyDataDir_StaticMessage(t *testing.T) {
	if ErrLegacyDataDir.Error() == "" {
		t.Error("ErrLegacyDataDir has empty message")
	}
}
