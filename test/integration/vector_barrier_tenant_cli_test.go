// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// vectorBarrierPermissions lets a tenant identity run the vector verbs and drive its own barrier.
var vectorBarrierPermissions = []string{
	"tenant_barrier:status", "tenant_barrier:seal", "tenant_barrier:unseal",
	"databases:read", "databases:list",
	"vector:namespace_create", "vector:namespace_delete", "vector:namespace_list", "vector:namespace_info",
	"vector:upsert", "vector:get", "vector:delete", "vector:search_similar",
}

// TestVector_CLI_BarrierTenant_PerProtocol runs a barrier tenant's vectors over every transport in raft mode.
func TestVector_CLI_BarrierTenant_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVectorBarrierTenantOverProtocol(t, node, proto)
		})
	}
}

// TestVector_CLI_BarrierTenant_Standalone_PerProtocol runs a barrier tenant's vectors over every transport in standalone mode.
func TestVector_CLI_BarrierTenant_Standalone_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startStandaloneModeNodeWithConfig(t, "pebble", standaloneBarrierTenantYAML(t.TempDir()))
	createVolumePoolCLI(t, node.cli, node.cliArgsFor(multiProtocols[0]), "default")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVectorBarrierTenantOverProtocol(t, node, proto)
		})
	}
}

// testVectorBarrierTenantOverProtocol checks a barrier tenant's vectors are served unsealed, refused sealed, and intact after the unseal.
func testVectorBarrierTenantOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	tenantID := fmt.Sprintf("vecb-%s-%d", proto.name, time.Now().UnixNano())

	adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Vector Barrier Tenant", "barrier", false, tenantID+"-admin")
	adminConn := tenantCLIArgsForProto(node, proto, adminCert, adminKey)
	if out, err := runCLI(t, node.cli, append(adminConn, "tenant", "barrier-init", "--id", tenantID, "--strategy", "none")...); err != nil {
		t.Fatalf("[%s] barrier-init: %v\noutput: %s", proto.name, err, out)
	}

	certPath, keyPath := node.ca.generateClientCertForTenant(t, tenantID)
	provisionTenantIdentityFor(t, node, tenantID, "vector-barrier-"+proto.name, vectorBarrierPermissions, certPath)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	vec := func(args ...string) (string, error) {
		return runQRDB(t, node.cli, conn, append([]string{"vector"}, args...)...)
	}
	ns := "docs-" + proto.name

	if out, err := vec("namespace", "create", "--namespace", ns, "--dimension", "2", "--distance-metric", "cosine"); err != nil {
		t.Fatalf("[%s] vector namespace create: %v\noutput: %s", proto.name, err, out)
	}
	for id, v := range map[string]string{"1": "1,0", "2": "0,1"} {
		if out, err := vec("upsert", "--namespace", ns, "--id", id, "--vector", v); err != nil {
			t.Fatalf("[%s] vector upsert id=%s: %v\noutput: %s", proto.name, id, err, out)
		}
	}
	if out, err := vec("get", "--namespace", ns, "--id", "1"); err != nil || !strings.Contains(out, "Found:     true") {
		t.Fatalf("[%s] vector get id=1 = %v\noutput: %s", proto.name, err, out)
	}
	if out, err := vec("namespace", "list"); err != nil || !strings.Contains(out, ns) {
		t.Fatalf("[%s] vector namespace list = %v, want %s\noutput: %s", proto.name, err, ns, out)
	}

	// Sealed: every vector verb is refused as sealed.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-seal", "--id", tenantID)...); err != nil {
		t.Fatalf("[%s] barrier-seal: %v\noutput: %s", proto.name, err, out)
	}
	for _, args := range [][]string{
		{"namespace", "create", "--namespace", ns + "-sealed", "--dimension", "2", "--distance-metric", "cosine"},
		{"upsert", "--namespace", ns, "--id", "3", "--vector", "1,1"},
		{"get", "--namespace", ns, "--id", "1"},
		{"search", "--namespace", ns, "--vector", "1,0", "--k", "1"},
		{"namespace", "list"},
		{"delete", "--namespace", ns, "--id", "2"},
	} {
		out, err := vec(args...)
		if err == nil {
			t.Fatalf("[%s] vector %s served on a sealed barrier: %s", proto.name, args[0], out)
		}
		if !strings.Contains(strings.ToLower(out), "sealed") {
			t.Fatalf("[%s] vector %s on a sealed barrier: want a sealed refusal, got: %s", proto.name, args[0], out)
		}
	}

	// Unsealed again: the vectors are where they were.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-unseal", "--id", tenantID, "--key", "bm9uZQ==")...); err != nil {
		t.Fatalf("[%s] barrier-unseal: %v\noutput: %s", proto.name, err, out)
	}
	out, err := runQRDB(t, node.cli, conn, "--output", "json", "vector", "search", "--namespace", ns, "--vector", "1,0", "--k", "2")
	if err != nil || !strings.Contains(out, `"id": 1`) || !strings.Contains(out, `"id": 2`) {
		t.Fatalf("[%s] vector search after the unseal = %v, want ids 1 and 2\noutput: %s", proto.name, err, out)
	}
	if out, err := vec("namespace", "delete", "--namespace", ns); err != nil {
		t.Fatalf("[%s] vector namespace delete: %v\noutput: %s", proto.name, err, out)
	}
}
