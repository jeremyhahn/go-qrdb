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

// volume_pool_cli_test.go drives the `qrdb volume-pool` command group
// (docs/volume-pools/cli.md) through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) sharing the
// multi_protocol_helpers_test.go harness. Pool management needs no
// configured storage.volume_pools on the node (a pool's catalog record is
// pure metadata; only a tenant actually placed on it needs the local path),
// so the plain startMultiProtocolNode harness is enough.
package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// cliVolumePoolJSON decodes the subset of VolumePool's JSON fields
// (sdk/go/transport/volume_pool_types.go) this file needs.
type cliVolumePoolJSON struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	Description   string  `json:"description"`
	CapacityBytes int64   `json:"capacityBytes"`
	Overcommit    float64 `json:"overcommit"`
	TenantCount   int     `json:"tenantCount"`
}

// cliVolumePoolDetailJSON decodes GetPool's response: a VolumePool's fields
// (promoted by the embedding, same as VolumePoolDetail's own JSON shape)
// plus the per-node report rows.
type cliVolumePoolDetailJSON struct {
	cliVolumePoolJSON
	NodeReports []struct {
		NodeID string `json:"nodeId"`
		State  string `json:"state"`
	} `json:"nodeReports"`
}

// cliListVolumePoolTenantsJSON decodes ListPoolTenants' response.
type cliListVolumePoolTenantsJSON struct {
	Tenants []struct {
		TenantID string `json:"tenantId"`
	} `json:"tenants"`
}

// TestVolumePool_CLI_CRUD_PerProtocol drives create/list/get/update/tenants/
// delete through every transport, each against its own uniquely named pool,
// proving the full management surface works identically regardless of
// transport (docs/volume-pools/api.md: Tier 1, every capability on all six
// transports).
func TestVolumePool_CLI_CRUD_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVolumePoolCRUDOverProtocol(t, node, proto)
		})
	}
}

// testVolumePoolCRUDOverProtocol drives the full lifecycle for one
// transport against a fresh, uniquely named pool.
func testVolumePoolCRUDOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	poolName := fmt.Sprintf("vp-%s-%d", proto.name, time.Now().UnixNano())

	// --- create -----------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "volume-pool", "create", poolName,
		"--description", "integration test pool",
		"--capacity", "2GiB",
		"--overcommit", "1.5",
	)
	if err != nil {
		t.Fatalf("[%s] volume-pool create: %v\noutput: %s", proto.name, err, out)
	}
	var created cliVolumePoolJSON
	if decErr := json.Unmarshal([]byte(out), &created); decErr != nil {
		t.Fatalf("[%s] decode create response: %v\noutput: %s", proto.name, decErr, out)
	}
	if created.Name != poolName {
		t.Fatalf("[%s] create Name = %q, want %q", proto.name, created.Name, poolName)
	}
	if created.Provider != "dir" {
		t.Fatalf("[%s] create Provider = %q, want %q", proto.name, created.Provider, "dir")
	}
	const wantCapacity = 2 << 30
	if created.CapacityBytes != wantCapacity {
		t.Fatalf("[%s] create CapacityBytes = %d, want %d", proto.name, created.CapacityBytes, wantCapacity)
	}
	if created.Overcommit != 1.5 {
		t.Fatalf("[%s] create Overcommit = %v, want 1.5", proto.name, created.Overcommit)
	}

	// A second create of the same name is refused: the catalog's record is
	// unique per name.
	if out, err := runCLIProto(t, node, proto, "volume-pool", "create", poolName); err == nil {
		t.Fatalf("[%s] duplicate volume-pool create must be refused, got success: %s", proto.name, out)
	}

	// --- list ---------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "list")
	if err != nil {
		t.Fatalf("[%s] volume-pool list: %v\noutput: %s", proto.name, err, out)
	}
	var pools []cliVolumePoolJSON
	if decErr := json.Unmarshal([]byte(out), &pools); decErr != nil {
		t.Fatalf("[%s] decode list response: %v\noutput: %s", proto.name, decErr, out)
	}
	found := false
	for _, p := range pools {
		if p.Name == poolName {
			found = true
		}
	}
	if !found {
		t.Fatalf("[%s] volume-pool list did not include %q: %s", proto.name, poolName, out)
	}

	// --- get ------------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get", poolName)
	if err != nil {
		t.Fatalf("[%s] volume-pool get: %v\noutput: %s", proto.name, err, out)
	}
	var detail cliVolumePoolDetailJSON
	if decErr := json.Unmarshal([]byte(out), &detail); decErr != nil {
		t.Fatalf("[%s] decode get response: %v\noutput: %s", proto.name, decErr, out)
	}
	if detail.Name != poolName {
		t.Fatalf("[%s] get Name = %q, want %q", proto.name, detail.Name, poolName)
	}

	// --- update ---------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "update", poolName,
		"--description", "updated by integration test",
		"--capacity", "4GiB",
		"--overcommit", "2",
	)
	if err != nil {
		t.Fatalf("[%s] volume-pool update: %v\noutput: %s", proto.name, err, out)
	}
	var updated cliVolumePoolJSON
	if decErr := json.Unmarshal([]byte(out), &updated); decErr != nil {
		t.Fatalf("[%s] decode update response: %v\noutput: %s", proto.name, decErr, out)
	}
	if updated.Description != "updated by integration test" {
		t.Fatalf("[%s] update Description = %q, want %q", proto.name, updated.Description, "updated by integration test")
	}
	const wantUpdatedCapacity = 4 << 30
	if updated.CapacityBytes != wantUpdatedCapacity {
		t.Fatalf("[%s] update CapacityBytes = %d, want %d", proto.name, updated.CapacityBytes, wantUpdatedCapacity)
	}
	if updated.Overcommit != 2 {
		t.Fatalf("[%s] update Overcommit = %v, want 2", proto.name, updated.Overcommit)
	}

	// --- tenants (empty: nothing is assigned to this pool) ---------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "tenants", poolName)
	if err != nil {
		t.Fatalf("[%s] volume-pool tenants: %v\noutput: %s", proto.name, err, out)
	}
	var tenantsResp cliListVolumePoolTenantsJSON
	if decErr := json.Unmarshal([]byte(out), &tenantsResp); decErr != nil {
		t.Fatalf("[%s] decode tenants response: %v\noutput: %s", proto.name, decErr, out)
	}
	if len(tenantsResp.Tenants) != 0 {
		t.Fatalf("[%s] volume-pool tenants on an unassigned pool should be empty, got: %s", proto.name, out)
	}

	// --- delete -----------------------------------------------------------------
	if out, err := runCLIProto(t, node, proto, "volume-pool", "delete", poolName); err != nil {
		t.Fatalf("[%s] volume-pool delete: %v\noutput: %s", proto.name, err, out)
	}

	// A deleted pool is gone: get now fails.
	if out, err := runCLIProto(t, node, proto, "volume-pool", "get", poolName); err == nil {
		t.Fatalf("[%s] volume-pool get after delete must fail, got success: %s", proto.name, out)
	}
}
