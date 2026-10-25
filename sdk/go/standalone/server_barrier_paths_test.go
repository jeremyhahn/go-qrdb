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

package standalone

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestNewServer_WrongPassphraseOnRebootIsRefused proves a data directory
// sealed under one passphrase does not open under another: the second boot
// finds the barrier initialised, its unseal fails, and NewServer reports it.
func TestNewServer_WrongPassphraseOnRebootIsRefused(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	first, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "the-passphrase-that-sealed-it"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, err = NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "a-different-passphrase-entirely"),
		},
	})
	var unsealErr *BarrierUnsealError
	if !errors.As(err, &unsealErr) {
		t.Fatalf("NewServer with the wrong passphrase: got %v, want *BarrierUnsealError", err)
	}
}

// TestNewServer_RebootWithTheSamePassphraseKeepsTheData proves the second
// boot unseals rather than re-initialises: what the first wrote is readable.
func TestNewServer_RebootWithTheSamePassphraseKeepsTheData(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	cfg := func() *ServerConfig {
		return &ServerConfig{
			Backend: database.BackendPebble,
			DataDir: dataDir,
			Barrier: &BarrierConfig{
				Strategy:   seal.StrategyPassphrase,
				AutoUnseal: true,
				Passphrase: barrierPassphrase(t, "the-same-passphrase-both-times"),
			},
		}
	}

	first, err := NewServer(ctx, cfg())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := first.Client().KV().Put(ctx, "k", []byte("v")); err != nil {
		_ = first.Shutdown(ctx)
		t.Fatalf("Put: %v", err)
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	second, err := NewServer(ctx, cfg())
	if err != nil {
		t.Fatalf("NewServer on reboot: %v", err)
	}
	defer func() { _ = second.Shutdown(ctx) }()
	got, err := second.Client().KV().Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get after reboot: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get after reboot = %q, want %q", got, "v")
	}
}

// TestNewServer_UnsetStrategyIsAPassphraseBarrier proves a barrier whose
// strategy is left empty but which carries a passphrase seals with it.
func TestNewServer_UnsetStrategyIsAPassphraseBarrier(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "a-passphrase-with-no-strategy"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("barrier sealed after AutoUnseal")
	}
	if status.Strategy == string(seal.StrategyNone) {
		t.Fatal("an unset strategy with a passphrase produced a NONE barrier")
	}
}

// TestRunAutoUnseal_RefusesShamir proves the auto-unseal step never opens a
// Shamir barrier, which only a share-holder quorum may unseal.
func TestRunAutoUnseal_RefusesShamir(t *testing.T) {
	err := (&Server{}).runAutoUnseal(context.Background(), nil, seal.StrategyShamir, &BarrierConfig{}, seal.Credentials{})
	var unsealErr *BarrierUnsealError
	if !errors.As(err, &unsealErr) {
		t.Fatalf("runAutoUnseal(shamir): got %v, want *BarrierUnsealError", err)
	}
	if !errors.Is(err, ErrShamirAutoUnseal) {
		t.Fatalf("runAutoUnseal(shamir): got %v, want ErrShamirAutoUnseal", err)
	}
}
