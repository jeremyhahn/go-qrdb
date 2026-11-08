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

package integration_test

import (
	"strings"
	"testing"
)

// TestCADelegatePolicy_CLI_PerProtocol drives a tenant's standing delegation
// policy end to end through the qrdb CLI on every protocol (#1186): the
// tenant writes the policy, its delegation renews itself within it and the
// renewed certificate reads the tenant's own data, and once the tenant deletes
// the policy the next mint is refused.
//
// The first mint is in process (Server.MintDelegation) and no CLI reaches it,
// so the tenant's own `ca delegate` stands in for it; every later mint is the
// delegation's own renewal through `ca delegate`, which is what the policy
// admits and what deleting it stops. A renewal is a `ca delegate` call, gated
// by certs:delegate like any other, so the credential that renews carries it
// in its scope, the policy admits it, and the principal is granted it.
func TestCADelegatePolicy_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node, _, _ := startDelegationNode(t)
	protocols := multiProtocols
	waitForDelegationNode(t, node, protocols)

	for _, proto := range protocols {
		t.Run(proto.name, func(t *testing.T) {
			admin := node.cliArgsFor(proto)
			key := "delegated/policy/" + proto.name
			if out, err := runQRDB(t, node.cli, admin, delegationKV("put", key, delegationSeed)...); err != nil {
				t.Fatalf("[%s] seed %s: %v\noutput: %s", proto.name, key, err, out)
			}

			// The tenant writes the policy, and lists it back.
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "put",
				"--principal", delegationPrincipal, "--scope", "certs:delegate,kv:get", "--ttl", "15m"); err != nil {
				t.Fatalf("[%s] ca delegate policy put: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "list"); err != nil || !strings.Contains(out, delegationPrincipal) {
				t.Fatalf("[%s] ca delegate policy list does not show %s: %v\noutput: %s", proto.name, delegationPrincipal, err, out)
			}

			// The delegation renews itself within the policy, and the
			// renewed certificate makes an in-tenant call.
			_, first := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", "certs:delegate", "kv:get")
			delegated := tenantCLIArgsForProto(node, proto, first.certPath, first.keyPath)
			_, renewed := issueDelegationCLI(t, node.cli, delegated, proto, delegationPrincipal, "10m", "kv:get")
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, renewed.certPath, renewed.keyPath),
				delegationKV("get", key)...); err != nil || !strings.Contains(out, delegationSeed) {
				t.Fatalf("[%s] the renewed delegation's in-tenant kv get: %v\noutput: %s", proto.name, err, out)
			}

			// Beyond the policy's scope, the renewal is refused.
			if out, err := runQRDB(t, node.cli, delegated, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", "kv:put"); err == nil {
				t.Fatalf("[%s] a renewal wider than the policy was admitted: %s", proto.name, out)
			}

			// Deleting the policy revokes it: the next mint is refused.
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "delete", delegationPrincipal); err != nil {
				t.Fatalf("[%s] ca delegate policy delete: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, delegated, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", "kv:get"); err == nil {
				t.Fatalf("[%s] a mint after the policy was deleted was admitted: %s", proto.name, out)
			}
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "list"); err != nil || strings.Contains(out, delegationPrincipal) {
				t.Fatalf("[%s] ca delegate policy list still shows %s after the delete: %v\noutput: %s", proto.name, delegationPrincipal, err, out)
			}
		})
	}
}
