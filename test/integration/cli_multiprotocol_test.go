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

// cli_multiprotocol_test.go is the self-spawned, no-Docker equivalent of
// test/integration/api/cli_multiprotocol_test.go (api-server's Docker-compose
// suite): the qrdb CLI's version / --server-flag-format / admin
// health/status/peers surface, including --output json and yaml, driven over
// every transport the CLI can select -- REST, gRPC, QUIC, MCP, Unix, sharing
// one startMultiProtocolNode (multi_protocol_helpers_test.go) -- plus the
// embedded transport, which the CLI cannot select at all: cmd/qrdb's
// buildClient refuses --transport embedded unconditionally in every build
// (cmd/qrdb/config.go), and no --server scheme selects it either
// (cmd/qrdb/root.go's schemePrefixes has no "embedded://" entry). Embedded is
// reachable only through sdk/go/node's in-process *sdk.Client, so this file
// drives it directly through a dedicated single-purpose node and asserts the
// same decisions (health public, status/peers permission-gated) the network
// transports reach through the CLI -- exactly the pattern
// replica_forwarding_embedded_test.go already established for its own
// feature's embedded coverage.
//
// The kv command tree itself is already covered exhaustively per transport
// by kv_cli_test.go (key-value); this file's concern is the api-server-owned
// surface that was previously Docker-only and untested for QUIC and
// embedded.
package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// embeddedAdminNode is a single in-process standalone node serving no
// network transport, with auth enabled, carrying a role granting exactly the
// admin:health / admin:status / admin:peers permissions the CLI-driven
// subtests below exercise over the network transports. adminCtx is the
// embedded transport's own authentication model (auth.WithIdentity) -- there
// is no CLI subprocess, TLS handshake, or login ceremony for this transport
// to drive instead.
type embeddedAdminNode struct {
	node     *node.Node
	adminCtx context.Context
}

// startEmbeddedAdminNode boots the node described by embeddedAdminNode's doc
// comment and returns it ready to use: Services().Role/.User are already
// seeded with an admin identity bound to adminCtx.
func startEmbeddedAdminNode(t *testing.T) *embeddedAdminNode {
	t.Helper()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// cfg.Auth.Enabled defaults to true (qrdbconfig.DefaultConfig): the
	// production posture every network-transport subtest in this file also
	// runs under (startMultiProtocolNode's --auth-enabled).

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("startEmbeddedAdminNode: NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svcNode := n.Services()
	adminRole, err := svcNode.Role.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:     "api-server-embedded-admin",
		TenantID: tenantid.System,
		Permissions: []permission.Permission{
			permission.AdminHealth,
			permission.AdminStatus,
			permission.AdminPeers,
		},
	})
	if err != nil {
		t.Fatalf("startEmbeddedAdminNode: CreateRole: %v", err)
	}
	if _, err := svcNode.User.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "api-server-embedded-admin",
		TenantID:             tenantid.System,
		RoleIDs:              []string{adminRole.ID},
	}); err != nil {
		t.Fatalf("startEmbeddedAdminNode: CreateUser: %v", err)
	}

	adminCtx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "api-server-embedded-admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRole.ID},
	})

	return &embeddedAdminNode{node: n, adminCtx: adminCtx}
}

// TestCLIMultiProtocolVersion mirrors the Docker suite's identically-named
// test: `qrdb version` bypasses client construction entirely
// (cmd/qrdb/version.go's RunE never calls buildClient), so it is exercised
// once, independent of transport -- TestCLIServerFlagFormats below is what
// proves transport selection parity at the flag-parsing level.
func TestCLIMultiProtocolVersion(t *testing.T) {
	cli := buildCLIBinary(t)

	out, err := runCLI(t, cli, "version")
	if err != nil {
		t.Fatalf("version: %v\noutput: %s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "qrdb") {
		t.Fatalf("version output = %q, want it to contain %q", out, "qrdb")
	}
}

// TestCLIServerFlagFormats verifies the CLI accepts every documented
// --server URL format (cmd/qrdb/root.go's schemePrefixes, plus a bare
// host:port) across the five CLI-selectable transports. There is no
// "embedded://" scheme -- schemePrefixes has no such entry, and
// TestCLIMultiProtocolHealth's embedded subtest below is what exercises that
// transport instead. version is used because it never dials the server, so
// this validates flag parsing in isolation, exactly as the Docker suite's
// TestCLIServerFlagFormats does.
func TestCLIServerFlagFormats(t *testing.T) {
	cli := buildCLIBinary(t)

	formats := []struct {
		name   string
		server string
	}{
		{"unix-socket", "unix:///tmp/does-not-need-to-exist.sock"},
		{"https", "https://127.0.0.1:1"},
		{"grpcs", "grpcs://127.0.0.1:1"},
		{"mcp", "mcp://127.0.0.1:1"},
		{"http", "http://127.0.0.1:1"},
		{"quic", "quic://127.0.0.1:1"},
		{"quics", "quics://127.0.0.1:1"},
		{"host-port-bare", "127.0.0.1:1"},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			out, err := runCLI(t, cli, "--server", f.server, "version")
			if err != nil {
				t.Logf("[%s] version --server %s: %v", f.name, f.server, err)
			}
			if !strings.Contains(strings.ToLower(out), "qrdb") {
				t.Fatalf("[%s] version --server %s = %q, want it to contain %q", f.name, f.server, out, "qrdb")
			}
		})
	}
}

// TestCLIMultiProtocolHealth tests `admin health` across every CLI
// transport, plus the embedded transport's direct equivalent. Health is a
// public route on every transport (docs/api-server/engineering.md's public
// routes table), so the embedded subtest deliberately calls it with no
// identity bound to ctx.
func TestCLIMultiProtocolHealth(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, mp, proto, "admin", "health")
			if err != nil {
				t.Fatalf("[%s] admin health: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "Status:") {
				t.Fatalf("[%s] admin health = %q, want it to contain %q", proto.name, out, "Status:")
			}
		})
	}

	t.Run("embedded", func(t *testing.T) {
		en := startEmbeddedAdminNode(t)
		resp, err := en.node.Client().Admin().Health(context.Background())
		if err != nil {
			t.Fatalf("[embedded] Admin().Health() with no identity: %v", err)
		}
		if resp.Status == "" {
			t.Fatalf("[embedded] Admin().Health() returned an empty Status")
		}
	})
}

// TestCLIMultiProtocolAdminStatus tests `admin status` across every CLI
// transport, plus the embedded transport's direct equivalent (the same
// admin:status permission, granted to startEmbeddedAdminNode's seeded
// identity).
func TestCLIMultiProtocolAdminStatus(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, mp, proto, "admin", "status")
			if err != nil {
				t.Fatalf("[%s] admin status: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "NodeID:") {
				t.Fatalf("[%s] admin status = %q, want it to contain %q", proto.name, out, "NodeID:")
			}
		})
	}

	t.Run("embedded", func(t *testing.T) {
		en := startEmbeddedAdminNode(t)

		if _, err := en.node.Client().Admin().Status(context.Background()); err == nil {
			t.Fatal("[embedded] Admin().Status() with no identity succeeded, want it refused")
		}

		resp, err := en.node.Client().Admin().Status(en.adminCtx)
		if err != nil {
			t.Fatalf("[embedded] Admin().Status() with the seeded admin identity: %v", err)
		}
		if resp.NodeID == "" {
			t.Fatalf("[embedded] Admin().Status() returned an empty NodeID")
		}
	})
}

// TestCLIMultiProtocolAdminPeers tests `admin peers` across every CLI
// transport, plus the embedded transport's direct equivalent.
func TestCLIMultiProtocolAdminPeers(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, mp, proto, "admin", "peers")
			if err != nil {
				t.Fatalf("[%s] admin peers: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "SHARD_ID") {
				t.Fatalf("[%s] admin peers = %q, want it to contain %q", proto.name, out, "SHARD_ID")
			}
		})
	}

	t.Run("embedded", func(t *testing.T) {
		en := startEmbeddedAdminNode(t)

		if _, err := en.node.Client().Admin().Peers(context.Background()); err == nil {
			t.Fatal("[embedded] Admin().Peers() with no identity succeeded, want it refused")
		}

		resp, err := en.node.Client().Admin().Peers(en.adminCtx)
		if err != nil {
			t.Fatalf("[embedded] Admin().Peers() with the seeded admin identity: %v", err)
		}
		if resp == nil {
			t.Fatal("[embedded] Admin().Peers() returned a nil response")
		}
	})
}

// TestCLIMultiProtocolOutputFormatAdminStatus tests --output json and
// --output yaml on `admin status` across every CLI transport. --output is a
// CLI-only presentation concern (cmd/qrdb/root.go's outputFormat flag); the
// embedded transport has no CLI and so no analogue -- its typed
// *transport.StatusResponse (asserted directly in
// TestCLIMultiProtocolAdminStatus) is the thing --output json/yaml is
// rendering on every other transport.
func TestCLIMultiProtocolOutputFormatAdminStatus(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, mp, proto, "--output", "json", "admin", "status")
			if err != nil {
				t.Fatalf("[%s] admin status --output json: %v\noutput: %s", proto.name, err, out)
			}
			var decoded map[string]any
			if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
				t.Fatalf("[%s] decode admin status --output json: %v\noutput: %s", proto.name, jerr, out)
			}
			if _, ok := decoded["node_id"]; !ok {
				t.Fatalf("[%s] admin status --output json = %v, want a %q key", proto.name, decoded, "node_id")
			}

			out, err = runCLIProto(t, mp, proto, "--output", "yaml", "admin", "status")
			if err != nil {
				t.Fatalf("[%s] admin status --output yaml: %v\noutput: %s", proto.name, err, out)
			}
			// yaml.Marshal has no json tags to key off: it lowercases the Go
			// field name with no separator ("nodeid:"), unlike --output
			// json's "node_id" (StatusResponse's json tag).
			if !strings.Contains(out, "nodeid:") {
				t.Fatalf("[%s] admin status --output yaml = %q, want it to contain %q", proto.name, out, "nodeid:")
			}
		})
	}
}

// TestCLIMultiProtocolOutputFormatAdminPeers tests --output json and
// --output yaml on `admin peers` across every CLI transport. See
// TestCLIMultiProtocolOutputFormatAdminStatus's doc comment for why embedded
// has no subtest here.
func TestCLIMultiProtocolOutputFormatAdminPeers(t *testing.T) {
	mp := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, mp, proto, "--output", "json", "admin", "peers")
			if err != nil {
				t.Fatalf("[%s] admin peers --output json: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "{") && !strings.Contains(out, "[") {
				t.Fatalf("[%s] admin peers --output json = %q, want JSON structure", proto.name, out)
			}

			out, err = runCLIProto(t, mp, proto, "--output", "yaml", "admin", "peers")
			if err != nil {
				t.Fatalf("[%s] admin peers --output yaml: %v\noutput: %s", proto.name, err, out)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("[%s] admin peers --output yaml produced no output", proto.name)
			}
		})
	}
}
