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

package sealtest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// heldSwapBackend is a memory backend whose first guarded swap after it is
// armed reports that it has been entered and then waits for the release.
type heldSwapBackend struct {
	*seal.MemoryBackend
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

// CompareAndSwap holds the armed swap until the release, then performs it.
func (b *heldSwapBackend) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if b.armed.CompareAndSwap(true, false) {
		close(b.entered)
		<-b.release
	}
	return b.MemoryBackend.CompareAndSwap(ctx, key, expected, next)
}

// PassphraseChangeInFlight returns an unsealed barrier sealed under
// passphrase on which a passphrase change from passphrase to next is in
// flight: it has taken the root-key rewrite and is held inside its guarded
// write, so every further rewrite of the root key, such as a second
// passphrase change, is refused with seal.ErrRootKeyWriteInProgress for as
// long as it is held. The returned function lets the held change finish
// and fails tb if it did not succeed; it is idempotent and also runs at
// cleanup, which then closes the barrier. The overlap is real rather than injected, and needs no sleep.
func PassphraseChangeInFlight(tb testing.TB, passphrase, next string) (*seal.StorageBarrier, func()) {
	tb.Helper()
	ctx := tb.Context()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(passphrase))
	if err != nil {
		tb.Fatalf("NewSoftwareStrategy: %v", err)
	}
	backend := &heldSwapBackend{
		MemoryBackend: seal.NewMemoryBackend(),
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		backend, seal.BarrierConfig{RootKeyPath: "sealtest-root-key", DeploymentID: 1}, strategy)
	if err != nil {
		tb.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(ctx, []byte(passphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		tb.Fatalf("barrier.Initialize: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(ctx, creds); err != nil {
			tb.Fatalf("barrier.Unseal: %v", err)
		}
	}

	backend.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		// The barrier, not the test, owns the change's context: tb's is
		// cancelled before cleanup runs the release.
		done <- barrier.ChangePassword(context.Background(),
			seal.MustCredentials(ctx, []byte(passphrase)), seal.MustCredentials(ctx, []byte(next)))
	}()
	select {
	case <-backend.entered:
	case err := <-done:
		tb.Fatalf("the held passphrase change returned before its guarded write: %v", err)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			close(backend.release)
			if err := <-done; err != nil {
				tb.Errorf("the held passphrase change: %v", err)
			}
		})
	}
	tb.Cleanup(func() {
		release()
		_ = barrier.Close()
	})
	return barrier, release
}
