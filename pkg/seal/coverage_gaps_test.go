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

package seal

import (
	"context"
	"testing"
)

// newNoPoolRegistry builds a BarrierRegistry with NO volume pool configured, so
// the pool-resolution helpers take their fail-closed branches.
func newNoPoolRegistry(t *testing.T) *BarrierRegistry {
	t.Helper()
	sys, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "sys/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	reg, err := NewBarrierRegistry(testLogger(), sys)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// TestSystemBarrierStrategy_Close verifies the no-op Close returns nil even with
// a nil backing barrier (the strategy holds no resources of its own).
func TestSystemBarrierStrategy_Close(t *testing.T) {
	t.Parallel()
	if err := NewSystemBarrierStrategy(nil).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLocalTenantDataRoot_UnknownTenant verifies the not-found path when neither
// the in-memory map nor the persistent store has a record for the tenant.
func TestLocalTenantDataRoot_UnknownTenant(t *testing.T) {
	t.Parallel()
	reg := newNoPoolRegistry(t)
	if _, err := reg.LocalTenantDataRoot(context.Background(), "ghost-tenant"); err == nil {
		t.Fatal("LocalTenantDataRoot: expected error for unknown tenant")
	}
}
