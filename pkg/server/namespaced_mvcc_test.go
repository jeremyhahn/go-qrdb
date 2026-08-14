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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// compactRecordingMVCC is an inner MVCCKVServicer that records whether its
// store-global Compact was reached and at which revision. It embeds the
// interface so only Compact (and CurrentRevision) need real bodies for these
// tests; any other method call would panic, proving the guard short-circuits
// before touching the shared store.
type compactRecordingMVCC struct {
	service.MVCCKVServicer
	compacted     bool
	compactedRev  int64
	compactCalls  int
	compactReturn error
}

func (m *compactRecordingMVCC) Compact(_ context.Context, revision int64) error {
	m.compacted = true
	m.compactedRev = revision
	m.compactCalls++
	return m.compactReturn
}

func (m *compactRecordingMVCC) CurrentRevision(_ context.Context) (int64, error) {
	return 0, nil
}

// TestNamespacedMVCC_Compact_NonSystemTenantRejected proves that a non-system
// tenant's Compact is rejected at the namespaced data-plane view with a typed
// *service.CompactSystemOnlyError and NEVER reaches the shared inner store.
// MVCC revisions are a store-global counter, so forwarding a tenant Compact
// would discard every other tenant's historical revisions.
func TestNamespacedMVCC_Compact_NonSystemTenantRejected(t *testing.T) {
	for _, tenantID := range []string{"tenantA", "acme", "another-tenant"} {
		t.Run(tenantID, func(t *testing.T) {
			inner := &compactRecordingMVCC{}
			w := newNamespacedMVCCService(inner, tenantID+"/db/")
			ctx := tenant.WithTenantID(context.Background(), tenantID)

			err := w.Compact(ctx, 42)
			if err == nil {
				t.Fatal("Compact() returned nil error for a non-system tenant; want *service.CompactSystemOnlyError")
			}
			var cso *service.CompactSystemOnlyError
			if !errors.As(err, &cso) {
				t.Fatalf("Compact() error type = %T (%v), want *service.CompactSystemOnlyError", err, err)
			}
			if cso.TenantID != tenantID {
				t.Fatalf("CompactSystemOnlyError.TenantID = %q, want %q", cso.TenantID, tenantID)
			}
			if inner.compacted || inner.compactCalls != 0 {
				t.Fatalf("CROSS-TENANT COMPACT LEAK: inner store Compact reached (calls=%d, rev=%d) for tenant %q",
					inner.compactCalls, inner.compactedRev, tenantID)
			}
		})
	}
}

// TestNamespacedMVCC_Compact_SystemTenantAllowed proves the legitimate admin
// path is preserved: a system-tenant caller's Compact is forwarded unchanged
// to the shared inner store at the requested revision.
func TestNamespacedMVCC_Compact_SystemTenantAllowed(t *testing.T) {
	inner := &compactRecordingMVCC{}
	w := newNamespacedMVCCService(inner, tenant.SystemTenantID+"/system/")
	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	if err := w.Compact(ctx, 7); err != nil {
		t.Fatalf("Compact() as system tenant returned error: %v", err)
	}
	if !inner.compacted {
		t.Fatal("Compact() as system tenant did not reach the inner store")
	}
	if inner.compactedRev != 7 {
		t.Fatalf("inner Compact revision = %d, want 7", inner.compactedRev)
	}
	if inner.compactCalls != 1 {
		t.Fatalf("inner Compact calls = %d, want 1", inner.compactCalls)
	}
}

// TestNamespacedMVCC_Compact_UnboundContextTreatedAsSystem proves an unbound
// context (no tenant binding) resolves to the system tenant and is allowed,
// matching tenant.FromContext's documented daemon-internal fallback so
// in-process maintenance compaction is not broken.
func TestNamespacedMVCC_Compact_UnboundContextTreatedAsSystem(t *testing.T) {
	inner := &compactRecordingMVCC{}
	w := newNamespacedMVCCService(inner, tenant.SystemTenantID+"/system/")

	if err := w.Compact(context.Background(), 3); err != nil {
		t.Fatalf("Compact() with unbound context returned error: %v", err)
	}
	if !inner.compacted || inner.compactedRev != 3 {
		t.Fatalf("inner Compact not reached as expected (compacted=%v rev=%d)", inner.compacted, inner.compactedRev)
	}
}

// TestNamespacedMVCC_Compact_SystemErrorPropagates proves a genuine inner
// Compact failure surfaces unchanged for the system tenant (the guard adds no
// error masking on the allowed path).
func TestNamespacedMVCC_Compact_SystemErrorPropagates(t *testing.T) {
	sentinel := errors.New("inner compact failed")
	inner := &compactRecordingMVCC{compactReturn: sentinel}
	w := newNamespacedMVCCService(inner, tenant.SystemTenantID+"/system/")
	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	if err := w.Compact(ctx, 1); !errors.Is(err, sentinel) {
		t.Fatalf("Compact() error = %v, want inner sentinel", err)
	}
}
