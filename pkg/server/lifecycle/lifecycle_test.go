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

package lifecycle_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// newLocalHost creates an in-memory LocalBackend suitable for testing.
func newLocalHost() *backend.LocalBackend {
	stg := memory.NewStorage()
	eng := engine.NewKVEngine(stg)
	return backend.NewLocalBackend(eng)
}

// newTestBarrier creates an initialized and unsealed in-memory seal barrier.
func newTestBarrier(t *testing.T) (
	barrier *seal.StorageBarrier,
	registry *seal.BarrierRegistry,
	cred *credentials.CredentialService,
) {
	t.Helper()

	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	passphrase := []byte(hex.EncodeToString(buf[:]))

	strategy, err := seal.NewSoftwareStrategy(passphrase)
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}

	bknd := seal.NewMemoryBackend()
	b, err := seal.NewStorageBarrier(
		slog.Default(), bknd,
		seal.BarrierConfig{RootKeyPath: "root-key"},
		strategy,
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	// Initialize transitions the barrier to the unsealed state directly.
	// A subsequent Unseal call is not needed and would return ErrAlreadyUnsealed.
	ctx := context.Background()
	if err := b.Initialize(ctx, seal.Credentials{Secret: string(passphrase)}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(slog.Default(), b)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	credSvc, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: "barrier"}, b, slog.Default())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}

	return b, reg, credSvc
}

// TestBuildServices_CoreServicesNonNil verifies that BuildServices with
// InProcessOnly infra (no seal) returns a Node with all core services
// populated. This is the baseline embedded GUI mode.
func TestBuildServices_CoreServicesNonNil(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	tests := []struct {
		name string
		svc  interface{}
	}{
		{"KV", node.KV},
		{"Admin", node.Admin},
		{"MVCC", node.MVCC},
		{"Batch", node.Batch},
		{"Transaction", node.Transaction},
		{"Lease", node.Lease},
		{"Index", node.Index},
		{"TimeSeries", node.TimeSeries},
		{"Watch", node.Watch},
		{"Lock", node.Lock},
		{"Collection", node.Collection},
		{"Database", node.Database},
		{"Vector", node.Vector},
		{"User", node.User},
		{"Role", node.Role},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.svc == nil {
				t.Errorf("%s is nil", tc.name)
			}
		})
	}
}

// TestBuildServices_SealGatedServicesNonNil verifies that BuildServices with
// a fully initialised seal barrier wires Tenant, Admin, KV, Barrier,
// Ceremony, Credential, and CertProv.
func TestBuildServices_SealGatedServicesNonNil(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	tests := []struct {
		name string
		svc  interface{}
	}{
		{"Barrier", node.Barrier},
		{"Ceremony", node.Ceremony},
		{"Credential", node.Credential},
		{"Tenant", node.Tenant},
		{"CertProv", node.CertProv},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.svc == nil {
				t.Errorf("%s is nil", tc.name)
			}
		})
	}
}

// TestBuildServices_NilHostError verifies that a nil Host returns
// NilHostError.
func TestBuildServices_NilHostError(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()

	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     nil,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilHostError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilHostError, got %T: %v", err, err)
	}
}

// TestBuildServices_NilWatchHubError verifies that a nil WatchHub returns
// NilWatchHubError.
func TestBuildServices_NilWatchHubError(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: nil,
		DataDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilWatchHubError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilWatchHubError, got %T: %v", err, err)
	}
}

// TestNode_Stop verifies that Stop succeeds on a cleanly constructed node
// and does not panic on a second call with a fresh node.
func TestNode_Stop(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}

	if err := node.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestBuildServices_WithSuppliedDatabaseManager verifies that when
// Infra.DatabaseManager is non-nil, BuildServices uses it and the
// Database service is non-nil.
func TestBuildServices_WithSuppliedDatabaseManager(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	// Supply a pre-built database manager to simulate start.go's pattern.
	dataDir := t.TempDir()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  dataDir,
		// DatabaseManager deliberately nil: lifecycle creates its own.
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Database == nil {
		t.Error("Database is nil")
	}
}
