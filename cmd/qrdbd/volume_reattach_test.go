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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
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

	reg, _, allocator := sealtest.PooledRegistry(t, system)

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
			AllowedVolumePools:        []string{"default"},
		}),
		tenant.WithVolumeAllocator(allocator),
	)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}
