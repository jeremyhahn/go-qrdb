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

//go:build integration

// The separation-of-duties posture end to end (go-qrdb#1128): what strict
// separation refuses the tenant Admin over every transport, what the standard
// posture lets an Admin holding the so role do, and that a restart under the
// other setting leaves one posture-change audit event and the new posture in
// the embedded accessor. Create-time admin-as-SO refusal and the Admin's
// grant of the so role to a fresh account are tenant_so_cli_test.go's
// (go-qrdb#1042).
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// postureTenant is a tenant created over one transport: its Admin's and its
// SO's connection flags and account ids, and its so role.
type postureTenant struct {
	id                string
	adminConn, soConn []string
	adminUser, soUser string
	soRole            string
	cli               string
	proto             multiProtocol
}

// createPostureTenant creates a tenant over proto whose SO is its Admin when
// adminIsSO, and a second account otherwise.
func createPostureTenant(t *testing.T, n *multiProtoNode, proto multiProtocol, prefix string, adminIsSO bool) *postureTenant {
	t.Helper()
	id := fmt.Sprintf("%s-%s-%d", prefix, proto.name, time.Now().UnixNano())
	adminCSR := tenantSOCSR(t, n, proto, id+"-admin")
	soName, soCSR := id+"-admin", adminCSR
	if !adminIsSO {
		soName, soCSR = id+"-so", tenantSOCSR(t, n, proto, id+"-so")
	}
	outDir := t.TempDir()
	if out, err := runCLIProto(t, n, proto, "tenant", "create",
		"--id", id, "--name", id, "--isolation-type", "prefix",
		"--admin-username", id+"-admin", "--admin-csr", filepath.Join(adminCSR, "csr.pem"),
		"--so-username", soName, "--so-csr", filepath.Join(soCSR, "csr.pem"), "--so-pin", "so-pin-1234",
		"--out", outDir,
	); err != nil {
		t.Fatalf("[%s] tenant create: %v\noutput: %s", proto.name, err, out)
	}
	pt := &postureTenant{
		id:        id,
		adminConn: tenantCLIArgsForProto(n, proto, filepath.Join(outDir, "admin-cert.pem"), filepath.Join(adminCSR, "key.pem")),
		soRole:    role.SystemRoleID(id, role.RoleNameSO),
		cli:       n.cli,
		proto:     proto,
	}
	pt.soConn = pt.adminConn
	if !adminIsSO {
		pt.soConn = tenantCLIArgsForProto(n, proto, filepath.Join(outDir, "so-cert.pem"), filepath.Join(soCSR, "key.pem"))
	}
	pt.adminUser = pt.userID(t, id+"-admin")
	pt.soUser = pt.userID(t, soName)
	return pt
}

// userID answers the id of the tenant account named username, listed by the
// tenant's Admin.
func (pt *postureTenant) userID(t *testing.T, username string) string {
	t.Helper()
	out, err := runQRDB(t, pt.cli, pt.adminConn, "--output", "json", "users", "list", "--tenant-id", pt.id)
	if err != nil {
		t.Fatalf("[%s] users list: %v\noutput: %s", pt.proto.name, err, out)
	}
	var listed userListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode users list JSON: %v\noutput: %s", pt.proto.name, jerr, out)
	}
	for _, u := range listed.Users {
		if u.Username == username {
			return fmt.Sprintf("%d", u.ID)
		}
	}
	t.Fatalf("[%s] users list has no %s: %s", pt.proto.name, username, out)
	return ""
}

// createUser creates an account in the tenant as its Admin and answers its id.
func (pt *postureTenant) createUser(t *testing.T, username string) string {
	t.Helper()
	out, err := runQRDB(t, pt.cli, pt.adminConn, "--output", "json", "users", "create", "--without-email-login-key", "--username", username)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", pt.proto.name, err, out)
	}
	var u userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", pt.proto.name, jerr, out)
	}
	return fmt.Sprintf("%d", u.ID)
}

// holdsSO reports whether the account userID holds the tenant's so role, as
// its SO reads it.
func (pt *postureTenant) holdsSO(t *testing.T, userID string) bool {
	t.Helper()
	out, err := runQRDB(t, pt.cli, pt.soConn, "--output", "json", "users", "get", userID)
	if err != nil {
		t.Fatalf("[%s] users get %s: %v\noutput: %s", pt.proto.name, userID, err, out)
	}
	var u userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
		t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", pt.proto.name, jerr, out)
	}
	for _, id := range u.RoleIDs {
		if id == pt.soRole {
			return true
		}
	}
	return false
}

// TestSeparationPosture_CLI_StrictRefusesAdmin_PerProtocol: under strict
// separation the tenant Admin may not grant the so role to an existing Admin
// account, its own, nor perform the SO-exclusive revocation of the so role;
// the tenant's SO performs both.
func TestSeparationPosture_CLI_StrictRefusesAdmin_PerProtocol(t *testing.T) {
	t.Parallel()
	n := startStrictSeparationNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			pt := createPostureTenant(t, n, proto, "sps", false)

			if out, err := runQRDB(t, pt.cli, pt.adminConn, "roles", "assign", "--user", pt.adminUser, "--role", pt.soRole); err == nil {
				t.Fatalf("[%s] strict separation: the Admin granted the so role to an Admin account: %s", proto.name, out)
			}
			if pt.holdsSO(t, pt.adminUser) {
				t.Fatalf("[%s] the Admin account holds the so role after a refused grant", proto.name)
			}

			if out, err := runQRDB(t, pt.cli, pt.adminConn, "roles", "unassign", "--user", pt.soUser, "--role", pt.soRole); err == nil {
				t.Fatalf("[%s] strict separation: the Admin revoked the SO's so role: %s", proto.name, out)
			}
			if !pt.holdsSO(t, pt.soUser) {
				t.Fatalf("[%s] the SO lost the so role to a refused revocation", proto.name)
			}

			// The SO cannot make the Admin an SO either: no principal holds
			// both roles under strict separation.
			if out, err := runQRDB(t, pt.cli, pt.soConn, "roles", "assign", "--user", pt.adminUser, "--role", pt.soRole); err == nil {
				t.Fatalf("[%s] strict separation: the SO granted the so role to the Admin: %s", proto.name, out)
			}
			if pt.holdsSO(t, pt.adminUser) {
				t.Fatalf("[%s] the Admin account holds the so role after the SO's refused grant", proto.name)
			}

			// roles:create and the built-in role reconcile are SO-exclusive.
			pt.soExclusiveOps(t, "admin", pt.adminConn, true)
			pt.soExclusiveOps(t, "so", pt.soConn, false)

			// Control: the SO grants and revokes the so role.
			uma := pt.createUser(t, pt.id+"-uma")
			if out, err := runQRDB(t, pt.cli, pt.soConn, "roles", "assign", "--user", uma, "--role", pt.soRole); err != nil {
				t.Fatalf("[%s] the SO granting the so role: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, pt.cli, pt.soConn, "roles", "unassign", "--user", uma, "--role", pt.soRole); err != nil {
				t.Fatalf("[%s] the SO revoking the so role: %v\noutput: %s", proto.name, err, out)
			}
			if pt.holdsSO(t, uma) {
				t.Fatalf("[%s] %s holds the so role after the SO revoked it", proto.name, uma)
			}
		})
	}
}

// TestSeparationPosture_CLI_SystemSO_PerProtocol drives the system SO a
// strict node's ceremony enrolled (#1128) over every transport: it creates
// a system role and runs the system tenant's and the all-tenants built-in
// role reconcile, each of which strict separation refuses the system Admin.
func TestSeparationPosture_CLI_SystemSO_PerProtocol(t *testing.T) {
	t.Parallel()
	n := startStrictSeparationNode(t)
	if n.systemSOCert == "" {
		t.Fatal("the strict node enrolled no system SO")
	}
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			pt := createPostureTenant(t, n, proto, "sso", false)
			soConn := tenantCLIArgsForProto(n, proto, n.systemSOCert, n.systemSOKey)
			ops := func(caller string) map[string][]string {
				return map[string][]string{
					"roles create": {"roles", "create", "--name", pt.id + "-" + caller + "-sysauditor",
						"--permissions", "audit:read", "--tenant-id", "system"},
					// The per-tenant reconcile is the caller's own tenant's;
					// the all-tenants pass reaches pt's.
					"roles reconcile --tenant":      {"roles", "reconcile", "--tenant", "system"},
					"roles reconcile --all-tenants": {"roles", "reconcile", "--all-tenants"},
				}
			}
			for name, args := range ops("admin") {
				out, err := runCLIProto(t, n, proto, args...)
				if err == nil {
					t.Fatalf("[%s] strict separation: %s ran as the system Admin: %s", proto.name, name, out)
				}
				lower := strings.ToLower(out)
				if !strings.Contains(lower, "separation of duties") &&
					!strings.Contains(lower, "so role required") &&
					!strings.Contains(lower, "permission denied") &&
					!strings.Contains(lower, "forbidden") {
					t.Fatalf("[%s] %s as the system Admin must be refused as a permission denial, got: %s", proto.name, name, out)
				}
			}
			for name, args := range ops("so") {
				if out, err := runQRDB(t, n.cli, soConn, args...); err != nil {
					t.Fatalf("[%s] %s as the system SO: %v\noutput: %s", proto.name, name, err, out)
				}
			}
		})
	}
}

// TestSeparationPosture_EmbeddedStrictRequiresSO boots an embedded node
// under strict separation with no system SO: first boot refuses it, typed,
// before any write (#1128).
func TestSeparationPosture_EmbeddedStrictRequiresSO(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	cfg.Auth.SeparationOfDuties = true
	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	defer func() { _ = n.Close() }()
	svcs := n.Services()
	_, err = n.FirstBoot(context.Background(), firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler), SeparationOfDuties: true,
	})
	var required *firstboot.SystemSORequiredError
	if !errors.As(err, &required) {
		t.Fatalf("strict first boot without an SO = %v, want *firstboot.SystemSORequiredError", err)
	}
}

// TestSeparationPosture_CLI_StandardAdminHoldingSO_PerProtocol: under the
// standard posture an Admin that is its tenant's SO grants and revokes the so
// role, the operations strict separation keeps to an SO that is not the
// Admin.
func TestSeparationPosture_CLI_StandardAdminHoldingSO_PerProtocol(t *testing.T) {
	t.Parallel()
	n := startMultiProtocolNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			pt := createPostureTenant(t, n, proto, "spa", true)
			if !pt.holdsSO(t, pt.adminUser) {
				t.Fatalf("[%s] the Admin created as the SO does not hold the so role", proto.name)
			}
			uma := pt.createUser(t, pt.id+"-uma")
			if out, err := runQRDB(t, pt.cli, pt.adminConn, "roles", "assign", "--user", uma, "--role", pt.soRole); err != nil {
				t.Fatalf("[%s] the Admin holding SO granting the so role: %v\noutput: %s", proto.name, err, out)
			}
			if !pt.holdsSO(t, uma) {
				t.Fatalf("[%s] %s lacks the so role after the grant", proto.name, uma)
			}
			if out, err := runQRDB(t, pt.cli, pt.adminConn, "roles", "unassign", "--user", uma, "--role", pt.soRole); err != nil {
				t.Fatalf("[%s] the Admin holding SO revoking the so role: %v\noutput: %s", proto.name, err, out)
			}
			if pt.holdsSO(t, uma) {
				t.Fatalf("[%s] %s holds the so role after the revocation", proto.name, uma)
			}
			pt.soExclusiveOps(t, "admin", pt.adminConn, false)
		})
	}
}

// soExclusiveOps runs the SO-exclusive role operations, roles create and
// roles reconcile, as the caller conn names, and asserts each is refused as a
// permission denial (refused) or succeeds.
func (pt *postureTenant) soExclusiveOps(t *testing.T, caller string, conn []string, refused bool) {
	t.Helper()
	// The role carries audit:read, which the SO role holds: a grantor may
	// grant only permissions it holds (#1278), so a permission outside the
	// caller's holdings would be refused whatever the posture.
	ops := map[string][]string{
		"roles create":    {"roles", "create", "--name", pt.id + "-" + caller + "-auditor", "--permissions", "audit:read", "--tenant-id", pt.id},
		"roles reconcile": {"roles", "reconcile", "--tenant", pt.id},
	}
	for name, args := range ops {
		out, err := runQRDB(t, pt.cli, conn, args...)
		if !refused {
			if err != nil {
				t.Fatalf("[%s] %s as %s: %v\noutput: %s", pt.proto.name, name, caller, err, out)
			}
			continue
		}
		if err == nil {
			t.Fatalf("[%s] strict separation: %s ran as %s: %s", pt.proto.name, name, caller, out)
		}
		lower := strings.ToLower(out)
		if !strings.Contains(lower, "separation of duties") &&
			!strings.Contains(lower, "so role required") &&
			!strings.Contains(lower, "permission denied") &&
			!strings.Contains(lower, "forbidden") {
			t.Fatalf("[%s] %s as %s must be refused as a permission denial, got: %s", pt.proto.name, name, caller, out)
		}
	}
}

// postureChanges answers the posture-change events the node's audit log
// holds, queried over proto.
func postureChanges(t *testing.T, n *multiProtoNode, proto multiProtocol) []auditEventCLIJSON {
	t.Helper()
	out, err := runCLIProto(t, n, proto, "--output", "json", "audit", "query",
		"--action", wiring.ActionSeparationPostureChange, "--limit", "1000")
	if err != nil {
		t.Fatalf("[%s] audit query: %v\noutput: %s", proto.name, err, out)
	}
	var page auditPageCLIJSON
	if jerr := json.Unmarshal([]byte(out), &page); jerr != nil {
		t.Fatalf("[%s] decode audit query JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return page.Events
}

// countPosture counts the events recording a change from previous to next.
func countPosture(events []auditEventCLIJSON, previous, next string) int {
	n := 0
	for _, ev := range events {
		if ev.Attributes["previous"] == previous && ev.Attributes["posture"] == next {
			n++
		}
	}
	return n
}

// TestSeparationPosture_CLI_RestartFlipAudited: a daemon booted under strict
// separation and restarted under the standard posture leaves exactly one
// strict-to-standard posture-change event, read over every transport; a
// further restart under the same setting adds none.
func TestSeparationPosture_CLI_RestartFlipAudited(t *testing.T) {
	t.Parallel()
	n := startStrictSeparationNode(t)

	if err := os.WriteFile(n.configPath, []byte(strings.Replace(mustReadFile(t, n.configPath), strictSeparationYAML, "", 1)), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	// n.strictSeparation stays set: the system admin first boot created
	// under strict separation still holds no data permission, which is what
	// the restart's readiness probe asks.
	n.restart(t, "[posture-flip] ")

	var total int
	for _, proto := range multiProtocols {
		events := postureChanges(t, n, proto)
		if got := countPosture(events, wiring.PostureStrict, wiring.PostureStandard); got != 1 {
			t.Fatalf("[%s] strict-to-standard events = %d, want 1: %+v", proto.name, got, events)
		}
		total = len(events)
	}

	n.restart(t, "[posture-same] ")
	for _, proto := range multiProtocols {
		if got := len(postureChanges(t, n, proto)); got != total {
			t.Fatalf("[%s] posture-change events after an unchanged restart = %d, want %d", proto.name, got, total)
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// startPostureNode starts an embedded standalone node over dataDir under the
// given auth.separation_of_duties, runs first boot, and answers it with its
// audit log.
func startPostureNode(t *testing.T, dataDir string, separation bool) (*node.Node, audit.AuditLogger) {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	cfg.Auth.SeparationOfDuties = separation
	trail := audit.NewRingBuffer(64)
	n, err := node.NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithAuditLogger(trail)})
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	svcs := n.Services()
	if _, err := n.FirstBoot(context.Background(), firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		_ = n.Close()
		t.Fatalf("FirstBoot: %v", err)
	}
	return n, trail
}

// embeddedPostureChanges answers the posture-change events in trail.
func embeddedPostureChanges(t *testing.T, trail audit.AuditLogger) []audit.AuditEvent {
	t.Helper()
	page, err := trail.Query(context.Background(), audit.Filter{Action: wiring.ActionSeparationPostureChange})
	if err != nil {
		t.Fatalf("audit Query: %v", err)
	}
	return page.Events
}

// TestSeparationPosture_EmbeddedRestartFlip: an embedded node restarted over
// its data under the other auth.separation_of_duties setting emits one
// posture-change event naming both postures, its StrictSeparation accessor
// answers the new posture, and a restart under the same setting emits none.
func TestSeparationPosture_EmbeddedRestartFlip(t *testing.T) {
	dataDir := t.TempDir()

	first, trail := startPostureNode(t, dataDir, false)
	if first.StrictSeparation() {
		t.Fatal("StrictSeparation() = true under the standard posture")
	}
	if got := len(embeddedPostureChanges(t, trail)); got != 1 {
		t.Fatalf("posture events on first boot = %d, want 1", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	flipped, trail := startPostureNode(t, dataDir, true)
	if !flipped.StrictSeparation() {
		t.Fatal("StrictSeparation() = false after a restart under strict separation")
	}
	events := embeddedPostureChanges(t, trail)
	if len(events) != 1 {
		t.Fatalf("posture events after the flip = %d, want 1", len(events))
	}
	if prev, next := events[0].Attributes["previous"], events[0].Attributes["posture"]; prev != wiring.PostureStandard || next != wiring.PostureStrict {
		t.Fatalf("posture event = %v -> %v, want %s -> %s", prev, next, wiring.PostureStandard, wiring.PostureStrict)
	}
	if err := flipped.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	same, trail := startPostureNode(t, dataDir, true)
	defer func() { _ = same.Close() }()
	if !same.StrictSeparation() {
		t.Fatal("StrictSeparation() = false after an unchanged restart")
	}
	if got := len(embeddedPostureChanges(t, trail)); got != 0 {
		t.Fatalf("posture events after an unchanged restart = %d, want 0", got)
	}
}
