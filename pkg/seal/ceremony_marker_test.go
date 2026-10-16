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
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// errMarkerRead is the read failure markerReadFailBackend injects.
var errMarkerRead = errors.New("seal test: injected read failure")

// markerReadFailBackend fails every Get of a key ending in suffix.
type markerReadFailBackend struct {
	StorageBackend
	suffix string
}

func (b *markerReadFailBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasSuffix(key, b.suffix) {
		return nil, errMarkerRead
	}
	return b.StorageBackend.Get(ctx, key)
}

func markerTestBarrier(t *testing.T, base StorageBackend) *StorageBarrier {
	t.Helper()
	sw, err := NewSoftwareStrategy(context.Background(), []byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), base,
		BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []StrategyID{StrategyPassphrase}}, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// TestCeremonyOperationalMarker: the marker is absent until recorded, is
// readable while the barrier is sealed, and survives into a second barrier
// over the same store -- a restart.
func TestCeremonyOperationalMarker(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	b := markerTestBarrier(t, base)
	if !b.IsSealed() {
		t.Fatal("a fresh barrier must be sealed")
	}
	if got, err := b.CeremonyOperational(ctx); err != nil || got {
		t.Fatalf("CeremonyOperational before marking = (%v, %v), want (false, nil)", got, err)
	}
	if err := b.MarkCeremonyOperational(ctx); err != nil {
		t.Fatalf("MarkCeremonyOperational on a sealed barrier: %v", err)
	}
	if got, err := b.CeremonyOperational(ctx); err != nil || !got {
		t.Fatalf("CeremonyOperational after marking = (%v, %v), want (true, nil)", got, err)
	}
	restarted := markerTestBarrier(t, base)
	if got, err := restarted.CeremonyOperational(ctx); err != nil || !got {
		t.Fatalf("CeremonyOperational after a restart = (%v, %v), want (true, nil)", got, err)
	}
}

// TestCeremonyOperationalMarker_ReadFailure: a read failure is returned, not
// reported as "not operational".
func TestCeremonyOperationalMarker_ReadFailure(t *testing.T) {
	b := markerTestBarrier(t, &markerReadFailBackend{StorageBackend: NewMemoryBackend(), suffix: ceremonyOperationalSuffix})
	if _, err := b.CeremonyOperational(context.Background()); !errors.Is(err, errMarkerRead) {
		t.Fatalf("CeremonyOperational = %v, want the read failure", err)
	}
}

// TestInitialized reads the persisted state on a sealed barrier after a
// restart, and returns a metadata read failure rather than reporting
// "never initialized".
func TestInitialized(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	b := markerTestBarrier(t, base)
	if got, err := b.Initialized(ctx); err != nil || got {
		t.Fatalf("Initialized before init = (%v, %v), want (false, nil)", got, err)
	}
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got, err := b.Initialized(ctx); err != nil || !got {
		t.Fatalf("Initialized after init = (%v, %v), want (true, nil)", got, err)
	}
	restarted := markerTestBarrier(t, base)
	if !restarted.IsSealed() {
		t.Fatal("a restarted barrier must be sealed")
	}
	if got, err := restarted.Initialized(ctx); err != nil || !got {
		t.Fatalf("Initialized after a restart = (%v, %v), want (true, nil)", got, err)
	}

	failing := markerTestBarrier(t, &markerReadFailBackend{StorageBackend: NewMemoryBackend(), suffix: b.metadataPath()})
	if _, err := failing.Initialized(ctx); !errors.Is(err, errMarkerRead) {
		t.Fatalf("Initialized with an unreadable metadata record = %v, want the read failure", err)
	}
}

// TestInitializedCached answers from memory alone: unknown until the barrier
// has read its metadata or been initialized, then known whether it is sealed
// or not, and a failed read leaves it unknown rather than "never initialized".
func TestInitializedCached(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	b := markerTestBarrier(t, base)
	if got, known := b.InitializedCached(); got || known {
		t.Fatalf("InitializedCached before any read = (%v, %v), want (false, false)", got, known)
	}
	if _, err := b.Initialized(ctx); err != nil {
		t.Fatalf("Initialized: %v", err)
	}
	if got, known := b.InitializedCached(); got || !known {
		t.Fatalf("InitializedCached after reading a pre-init barrier = (%v, %v), want (false, true)", got, known)
	}
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got, known := b.InitializedCached(); !got || !known {
		t.Fatalf("InitializedCached after Initialize = (%v, %v), want (true, true)", got, known)
	}

	restarted := markerTestBarrier(t, base)
	if got, known := restarted.InitializedCached(); got || known {
		t.Fatalf("InitializedCached after a restart = (%v, %v), want (false, false)", got, known)
	}
	if _, err := restarted.Initialized(ctx); err != nil {
		t.Fatalf("Initialized after a restart: %v", err)
	}
	if !restarted.IsSealed() {
		t.Fatal("a restarted barrier must be sealed")
	}
	if got, known := restarted.InitializedCached(); !got || !known {
		t.Fatalf("InitializedCached on a sealed, initialized barrier = (%v, %v), want (true, true)", got, known)
	}

	failing := markerTestBarrier(t, &markerReadFailBackend{StorageBackend: NewMemoryBackend(), suffix: b.metadataPath()})
	if _, err := failing.Initialized(ctx); !errors.Is(err, errMarkerRead) {
		t.Fatalf("Initialized with an unreadable metadata record = %v, want the read failure", err)
	}
	if got, known := failing.InitializedCached(); got || known {
		t.Fatalf("InitializedCached after a failed read = (%v, %v), want (false, false)", got, known)
	}
}
