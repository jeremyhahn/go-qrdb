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

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func reattachTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newReattachFixture builds a TenantService backed by a pooled, system-managed
// barrier registry whose system barrier is initialized + unsealed, suitable for
// exercising reattach helpers and quota tests in this package.
func newReattachFixture(t *testing.T) (*tenant.TenantService, *seal.BarrierRegistry) {
	t.Helper()

	dir := t.TempDir()
	c := local.New()
	if err := c.Configure(map[string]string{"path": dir}); err != nil {
		t.Fatalf("configure local backend: %v", err)
	}
	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })
	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	mgr := objpool.NewManager()
	if err := mgr.Register("default", []common.Storage{c}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	sysStrat, err := seal.NewSoftwareStrategy(context.Background(), []byte("reattach-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(reattachTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}
