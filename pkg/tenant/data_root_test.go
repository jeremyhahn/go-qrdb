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

package tenant

import (
	"context"
	"errors"
	"testing"
)

// TestDataRootsOverlap covers the component-aware containment helper directly,
// including the /data/foo vs /data/foo2 guard that a naive strings.HasPrefix
// would get wrong. dataRootsOverlap now backs only the deleted-tenant residue
// guard (no replicated record names a filesystem path any more), but the
// component-aware containment math it relies on is unchanged.
func TestDataRootsOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "/data/foo", "/data/foo", true},
		{"equal trailing slash normalized", "/data/foo/", "/data/foo", true},
		{"a parent of b", "/data/foo", "/data/foo/bar", true},
		{"b parent of a", "/data/foo/bar", "/data/foo", true},
		{"deep child", "/data", "/data/a/b/c", true},
		{"sibling foo vs foo2", "/data/foo", "/data/foo2", false},
		{"sibling foo2 vs foo", "/data/foo2", "/data/foo", false},
		{"unrelated", "/data/foo", "/other/bar", false},
		{"prefix-string but not path", "/data/foobar", "/data/foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dataRootsOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("dataRootsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestCreateTenant_WithDataRoot_IsRefused, TestCreateTenant_PersistsNoDataRoot
// and TestGetTenant_RefusesAStoredDataRoot covered the "interim, until named
// volume pools land" fix (go-qrdb, commit 58e8daed7): CreateTenant's
// WithDataRoot refused with *DataRootRetiredError, and a stored record
// carrying a path refused with *PersistedDataRootError. Both types are gone:
// CreateTenant no longer accepts an explicit disk path at all (there is no
// WithDataRoot option to call), and a persisted record can no longer carry
// one -- volume.Catalog's Reserve validates the Assignment's path shape
// before it is ever written (docs/volume-pools D9), closing off the
// malformed-record case the second test defended against at a lower layer,
// covered by pkg/volume's own tests. See the identical notes on
// pkg/seal/destroy_tenant_test.go and pkg/tenant's other D9-era deletions
// this same migration made.

// TestCreateTenant_WithVolumePool_RejectedForPrefixIsolation verifies that
// WithVolumePool is rejected for the default (prefix) isolation type, which
// never draws from a volume pool.
func TestCreateTenant_WithVolumePool_RejectedForPrefixIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "ptenant", "Prefix Tenant", IsolationPrefix, WithVolumePool("default"))
	var dite *DataRootIsolationTypeError
	if !errors.As(err, &dite) {
		t.Fatalf("CreateTenant(prefix, WithVolumePool) = %v, want *DataRootIsolationTypeError", err)
	}
}

// TestCreateTenant_WithVolumePool_RejectedForBarrierIsolation verifies that
// WithVolumePool is rejected for IsolationBarrier, which draws its per-tenant
// Pebble from the generic pooled-tenant primitives, not a named volume pool.
func TestCreateTenant_WithVolumePool_RejectedForBarrierIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "btenant", "Barrier Tenant", IsolationBarrier, WithVolumePool("default"))
	var dite *DataRootIsolationTypeError
	if !errors.As(err, &dite) {
		t.Fatalf("CreateTenant(barrier, WithVolumePool) = %v, want *DataRootIsolationTypeError", err)
	}
}
