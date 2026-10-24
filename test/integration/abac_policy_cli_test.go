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

//go:build integration

// abac_policy_cli_test.go drives the whole `qrdb abac policy` command tree
// (create, get, list, update, delete) through the real client CLI binary
// (cmd/qrdb/abac_policy.go), against every transport (REST, gRPC, QUIC, MCP,
// Unix), using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). Every document is stored in the
// caller's tenant by the daemon's PolicyService.
package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// abacPolicyCLIJSON mirrors the fields of abac.Policy the test reads.
type abacPolicyCLIJSON struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name     string    `json:"name"`
		Tenant   string    `json:"tenant"`
		Priority int       `json:"priority"`
		Created  time.Time `json:"created"`
		Updated  time.Time `json:"updated"`
	} `json:"metadata"`
	Spec struct {
		Enabled *bool `json:"enabled"`
		Rules   []struct {
			Name string `json:"name"`
			When []struct {
				Expression string `json:"expression"`
			} `json:"when"`
		} `json:"rules"`
	} `json:"spec"`
}

// abacPolicyCLIDocument is a policy document with no metadata.tenant: the
// daemon stores it in the caller's tenant.
func abacPolicyCLIDocument(name, enabled string) string {
	return `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: ` + name + `
  priority: 10
spec:
  enabled: ` + enabled + `
  subjects:
    roles: [auditor]
  rules:
    - name: deny-secrets
      effect: deny
      actions: [read]
      resources: ["/secrets/**"]
      when:
        - expression: 'subject.department == "finance"'
`
}

// TestABACPolicy_CLI_PerProtocol drives the stored-policy lifecycle --
// create, get, update (Created survives, Enabled flips), list, delete --
// and its refusals (a duplicate name, a missing policy, naming another
// tenant) through the CLI binary against every transport sharing one
// multiProtoNode.
func TestABACPolicy_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testABACPolicyLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testABACPolicyLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	name := "cli-" + proto.name
	dir := t.TempDir()
	enabledDoc := filepath.Join(dir, "enabled.yaml")
	disabledDoc := filepath.Join(dir, "disabled.yaml")
	if err := os.WriteFile(enabledDoc, []byte(abacPolicyCLIDocument(name, "true")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disabledDoc, []byte(abacPolicyCLIDocument(name, "false")), 0o600); err != nil {
		t.Fatal(err)
	}
	decode := func(op, out string) abacPolicyCLIJSON {
		t.Helper()
		var p abacPolicyCLIJSON
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("[%s] decode abac policy %s JSON: %v\noutput: %s", proto.name, op, err, out)
		}
		return p
	}

	// --- create --------------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "abac", "policy", "create", "--file", enabledDoc)
	if err != nil {
		t.Fatalf("[%s] abac policy create: %v\noutput: %s", proto.name, err, out)
	}
	created := decode("create", out)
	if created.Metadata.Name != name || created.Metadata.Tenant == "" || created.Metadata.Created.IsZero() {
		t.Fatalf("[%s] created = %+v, want the name, the caller's tenant and a creation time", proto.name, created.Metadata)
	}

	// --- get -----------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "abac", "policy", "get", name)
	if err != nil {
		t.Fatalf("[%s] abac policy get: %v\noutput: %s", proto.name, err, out)
	}
	got := decode("get", out)
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].When) != 1 || got.Metadata.Priority != 10 {
		t.Fatalf("[%s] get = %+v, want the stored document", proto.name, got)
	}
	out, err = runCLIProto(t, node, proto, "--output", "yaml", "abac", "policy", "get", name)
	if err != nil || !strings.Contains(out, "name: "+name) {
		t.Fatalf("[%s] abac policy get (yaml): %v\noutput: %s", proto.name, err, out)
	}
	out, err = runCLIProto(t, node, proto, "abac", "policy", "get", name)
	if err != nil || !strings.Contains(out, "ENABLED") || !strings.Contains(out, name) {
		t.Fatalf("[%s] abac policy get (table): %v\noutput: %s", proto.name, err, out)
	}

	// --- update, --tenant naming the caller's own tenant ---------------------
	tenant := created.Metadata.Tenant
	out, err = runCLIProto(t, node, proto, "--output", "json", "abac", "policy", "update", "--tenant", tenant, "-f", disabledDoc)
	if err != nil {
		t.Fatalf("[%s] abac policy update: %v\noutput: %s", proto.name, err, out)
	}
	updated := decode("update", out)
	if updated.Spec.Enabled == nil || *updated.Spec.Enabled || !updated.Metadata.Created.Equal(created.Metadata.Created) {
		t.Fatalf("[%s] updated = %+v, want enabled false and the original creation time", proto.name, updated)
	}

	// --- list ----------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "abac", "policy", "list", "--tenant", tenant)
	if err != nil {
		t.Fatalf("[%s] abac policy list: %v\noutput: %s", proto.name, err, out)
	}
	var listed []abacPolicyCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode abac policy list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	found := false
	for _, p := range listed {
		found = found || p.Metadata.Name == name
	}
	if !found {
		t.Fatalf("[%s] list does not name %s: %s", proto.name, name, out)
	}
	out, err = runCLIProto(t, node, proto, "abac", "policy", "list")
	if err != nil || !strings.Contains(out, name) {
		t.Fatalf("[%s] abac policy list (table): %v\noutput: %s", proto.name, err, out)
	}

	// --- refusals ------------------------------------------------------------
	if out, err = runCLIProto(t, node, proto, "abac", "policy", "create", "--file", enabledDoc); err == nil {
		t.Fatalf("[%s] abac policy create of an existing name should fail, got: %s", proto.name, out)
	}
	if out, err = runCLIProto(t, node, proto, "abac", "policy", "get", name+"-absent"); err == nil {
		t.Fatalf("[%s] abac policy get of a missing policy should fail, got: %s", proto.name, out)
	}
	if out, err = runCLIProto(t, node, proto, "abac", "policy", "list", "--tenant", "abac-cli-foreign-tenant"); err == nil {
		t.Fatalf("[%s] abac policy list naming another tenant should fail, got: %s", proto.name, out)
	}

	// --- delete --------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "abac", "policy", "delete", "--tenant", tenant, name)
	if err != nil || !strings.Contains(out, "Deleted policy "+name) {
		t.Fatalf("[%s] abac policy delete: %v\noutput: %s", proto.name, err, out)
	}
	if out, err = runCLIProto(t, node, proto, "abac", "policy", "get", name); err == nil {
		t.Fatalf("[%s] abac policy get after delete should fail, got: %s", proto.name, out)
	}
}
