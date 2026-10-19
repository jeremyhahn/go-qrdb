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

// roles_own_permissions_cli_test.go drives `qrdb roles own-permissions`
// through the real client CLI binary against every transport (REST, gRPC,
// QUIC, MCP, Unix). Each run reaches the server's own-permissions endpoint
// through that transport's network RBACClient, so the answer is the
// server's, for the authenticated caller.
package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// ownPermissionsCLIJSON mirrors the `roles own-permissions --output json`
// document.
type ownPermissionsCLIJSON struct {
	Permissions []struct {
		Name     string `json:"name"`
		Resource string `json:"resource"`
		Action   string `json:"action"`
		Scope    string `json:"scope"`
	} `json:"permissions"`
}

// TestRolesOwnPermissions_CLI_PerProtocol lists the caller's own effective
// permissions over every transport. The caller is the node's administrator,
// so the answer holds the role-management permissions the lifecycle test
// exercises; every entry is concrete, carries its scope, and the answer is
// the same over every transport.
func TestRolesOwnPermissions_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	var first []string
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "own-permissions")
			if err != nil {
				t.Fatalf("[%s] roles own-permissions: %v\noutput: %s", proto.name, err, out)
			}
			var doc ownPermissionsCLIJSON
			if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
				t.Fatalf("[%s] decode roles own-permissions JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			names := make([]string, len(doc.Permissions))
			for i, p := range doc.Permissions {
				if p.Name != p.Resource+":"+p.Action || p.Scope == "" {
					t.Fatalf("[%s] malformed permission %+v", proto.name, p)
				}
				if strings.Contains(p.Name, "*") {
					t.Fatalf("[%s] wildcard %q was not expanded", proto.name, p.Name)
				}
				names[i] = p.Name
			}
			for _, want := range []string{"roles:create", "roles:list", "users:read"} {
				if !slices.Contains(names, want) {
					t.Fatalf("[%s] own permissions lack %q: %v", proto.name, want, names)
				}
			}
			if first == nil {
				first = names
			} else if !stringSliceEqualUnordered(names, first) {
				t.Fatalf("[%s] own permissions differ from the first transport's:\n got %v\nwant %v", proto.name, names, first)
			}

			table, err := runCLIProto(t, node, proto, "roles", "own-permissions")
			if err != nil {
				t.Fatalf("[%s] roles own-permissions (table): %v\noutput: %s", proto.name, err, table)
			}
			for _, want := range []string{"NAME", "RESOURCE", "ACTION", "SCOPE", "roles:list"} {
				if !strings.Contains(table, want) {
					t.Fatalf("[%s] table lacks %q:\n%s", proto.name, want, table)
				}
			}
		})
	}
}

// TestRolesOwnPermissions_CLI_RejectsArguments shows the command takes no
// positional argument: it answers for the caller only.
func TestRolesOwnPermissions_CLI_RejectsArguments(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)
	proto := multiProtocols[0]
	if out, err := runCLIProto(t, node, proto, "roles", "own-permissions", "someone-else"); err == nil {
		t.Fatalf("[%s] roles own-permissions accepted a positional argument:\n%s", proto.name, out)
	}
}
