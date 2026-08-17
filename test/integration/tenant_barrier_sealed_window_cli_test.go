//go:build integration

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

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTenantBarrier_SealedWindowLifecycle_PerProtocol proves the complete
// self-managed tenant barrier lifecycle — and, with it, the sealed-window
// authorization parity of every transport — end to end through the CLI:
//
//  1. PRE-INIT WINDOW: a self-managed dedicated-isolation tenant starts with
//     its barrier registered SEALED and uninitialized, so its IAM is
//     unreachable and NO role able to satisfy any tenant_barrier:* RBAC
//     check can exist. The tenant's own mTLS identity (CN=admin,
//     OU=<tenant>) must nevertheless read `tenant barrier-status`
//     (sealed-window fallback) and drive `tenant barrier-init`
//     (trust-on-first-init).
//  2. RBAC PATH: once initialized (unsealed) and a tenant role/user exist,
//     the identical commands authorize through plain RBAC.
//  3. RE-SEALED WINDOW: after `tenant barrier-seal`, the tenant's IAM is
//     unreachable again; `barrier-status` and `barrier-unseal` must remain
//     reachable via the sealed-window fallback, and unsealing restores the
//     RBAC path.
//
// Every transport (REST, gRPC, QUIC, MCP, Unix) drives the identical flow —
// the routes/methods/tools are IdentityGated with the RBAC-first fallback
// rules centralized in pkg/auth/middleware (AuthorizeTenantBarrierInit /
// AuthorizeTenantBarrierSealedOp), and this test is the cross-transport pin
// that keeps them from drifting. It also exercises the tenant sealed-window
// admin:health liveness exception: the gRPC/QUIC/Unix SDK transports
// perform an authenticated Health RPC at Connect time, which must succeed
// for a sealed tenant's identity or the unseal flow could never start on
// those transports.
func TestTenantBarrier_SealedWindowLifecycle_PerProtocol(t *testing.T) {
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testSealedWindowLifecycleOverProtocol(t, node, proto)
		})
	}
}

// testSealedWindowLifecycleOverProtocol drives the full lifecycle for one
// transport against a fresh tenant.
func testSealedWindowLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	tenantID := fmt.Sprintf("swl-%s-%d", proto.name, time.Now().UnixNano())

	// The system admin creates the SELF-MANAGED (autoUnseal=false)
	// dedicated-isolation tenant: its barrier registers SEALED and
	// uninitialized. IsolationRaft (RegisterVolumeTenant) registers the
	// full system strategy set, so the none strategy below is available;
	// see the RF=3 cluster enable-encryption test for the same flow.
	createTenant(t, node, proto, tenantID, "Sealed Window Tenant", "raft", false)

	// The tenant's own mTLS identity. NO user or role exists for it yet —
	// the whole point of the bootstrap windows under test.
	certPath, keyPath := node.ca.generateClientCertForTenant(t, tenantID)
	tenantCLI := func(args ...string) (string, error) {
		full := append(tenantCLIArgsForProto(node, proto, certPath, keyPath), args...)
		return runCLI(t, node.cli, full...)
	}

	// --- 1. Pre-init sealed window -------------------------------------
	out, err := tenantCLI("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] pre-init barrier-status by the tenant's own identity must succeed via the sealed-window fallback: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Initialized: false") {
		t.Fatalf("[%s] pre-init barrier-status should report Initialized: false, got: %s", proto.name, out)
	}

	if out, err = tenantCLI("tenant", "barrier-init", "--id", tenantID, "--strategy", "none"); err != nil {
		t.Fatalf("[%s] barrier-init by the tenant's own identity must succeed via trust-on-first-init: %v\noutput: %s", proto.name, err, out)
	}

	// The trust-on-first-init window has CLOSED: without any role, the same
	// identity must now be refused by RBAC (the barrier is unsealed, so the
	// sealed-window fallback is unavailable and re-init demands the grant).
	if out, err = tenantCLI("tenant", "barrier-init", "--id", tenantID, "--strategy", "none"); err == nil {
		t.Fatalf("[%s] re-init after initialization without an RBAC grant must be refused, got success: %s", proto.name, out)
	}

	// --- 2. RBAC path ---------------------------------------------------
	// Provision the tenant role/user (system-admin plumbing, not the
	// feature under test). admin:health is required for the gRPC/QUIC/Unix
	// SDK Connect-time Health RPC once the barrier is unsealed and RBAC
	// resolves roles again.
	provisionTenantIdentity(t, node, tenantID, "barrier-admin-"+proto.name, []string{
		"tenant_barrier:status", "tenant_barrier:init", "tenant_barrier:unseal", "tenant_barrier:seal",
		"admin:health",
	})

	out, err = tenantCLI("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] post-init barrier-status via RBAC must succeed: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Sealed:      false") || !strings.Contains(out, "Initialized: true") {
		t.Fatalf("[%s] post-init barrier-status should report unsealed+initialized, got: %s", proto.name, out)
	}

	// --- 3. Re-sealed window --------------------------------------------
	if out, err = tenantCLI("tenant", "barrier-seal", "--id", tenantID); err != nil {
		t.Fatalf("[%s] barrier-seal via RBAC must succeed: %v\noutput: %s", proto.name, err, out)
	}

	// IAM is unreachable again: status must fall back to the sealed-window
	// rule and report the sealed state.
	out, err = tenantCLI("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] sealed-window barrier-status must succeed via the fallback: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Sealed:      true") {
		t.Fatalf("[%s] sealed-window barrier-status should report Sealed: true, got: %s", proto.name, out)
	}

	// Unseal through the fallback. The none strategy ignores the key value
	// but the service requires non-empty key material; "bm9uZQ==" is
	// base64("none").
	if out, err = tenantCLI("tenant", "barrier-unseal", "--id", tenantID, "--key", "bm9uZQ=="); err != nil {
		t.Fatalf("[%s] sealed-window barrier-unseal must succeed via the fallback: %v\noutput: %s", proto.name, err, out)
	}

	// Back on the RBAC path: the barrier reports unsealed again.
	out, err = tenantCLI("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] post-unseal barrier-status via RBAC must succeed: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Sealed:      false") {
		t.Fatalf("[%s] post-unseal barrier-status should report Sealed: false, got: %s", proto.name, out)
	}
}
