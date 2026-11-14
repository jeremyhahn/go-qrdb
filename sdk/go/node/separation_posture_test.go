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

package node

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newPostureNode starts a standalone node under the given
// Auth.SeparationOfDuties setting whose audit log is trail.
func newPostureNode(t *testing.T, separation bool, trail audit.AuditLogger) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	cfg.Auth.SeparationOfDuties = separation
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithAuditLogger(trail)})
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func postureEvents(t *testing.T, trail audit.AuditLogger) []audit.AuditEvent {
	t.Helper()
	page, err := trail.Query(context.Background(), audit.Filter{Action: wiring.ActionSeparationPostureChange})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return page.Events
}

func runPostureFirstBoot(t *testing.T, n *Node) {
	t.Helper()
	svcs := n.Services()
	// No system SO: it is optional in both postures (SPEC row 124), and this
	// test is about the posture record, not the SO's enrolment.
	if _, err := n.FirstBoot(context.Background(), firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatalf("FirstBoot: %v", err)
	}
}

// TestNode_FirstBoot_StrictNodeOverridesARelaxedCaller proves a strict node's
// posture wins over a Dependencies literal that leaves SeparationOfDuties
// false: first boot with no system SO succeeds (the SO is optional, SPEC row
// 124) and, strict, grants the bootstrap admin no Owner role.
func TestNode_FirstBoot_StrictNodeOverridesARelaxedCaller(t *testing.T) {
	ctx := context.Background()
	n := newPostureNode(t, true, audit.NewRingBuffer(64))
	svcs := n.Services()
	deps := firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler),
	}
	result, err := n.FirstBoot(ctx, deps)
	if err != nil {
		t.Fatalf("FirstBoot on a strict node with no SO: %v, want success", err)
	}
	if result.SOCreated {
		t.Fatal("FirstBoot enrolled an SO it was not given")
	}
	admin, err := n.UserService().GetUserByUsername(ctx, tenantid.System, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}
	if owner := role.SystemRoleID(tenantid.System, role.RoleNameOwner); slices.Contains(admin.RoleIDs, owner) {
		t.Fatalf("admin roles = %v, want no %s on a strict node", admin.RoleIDs, owner)
	}
}

// TestNode_FirstBoot_RecordsSeparationPosture: first boot records the
// posture the node resolved and leaves one event in the node's audit log; a
// second first boot under the same posture leaves no other.
func TestNode_FirstBoot_RecordsSeparationPosture(t *testing.T) {
	trail := audit.NewRingBuffer(64)
	n := newPostureNode(t, true, trail)

	runPostureFirstBoot(t, n)
	events := postureEvents(t, trail)
	if len(events) != 1 {
		t.Fatalf("posture events after first boot = %d, want 1", len(events))
	}
	if got := events[0].Attributes["posture"]; got != wiring.PostureStrict {
		t.Errorf("posture = %v, want %q", got, wiring.PostureStrict)
	}
	if got := events[0].Attributes["previous"]; got != wiring.PostureNone {
		t.Errorf("previous = %v, want %q", got, wiring.PostureNone)
	}
	rec, err := n.Services().KV.Get(context.Background(), wiring.SeparationPostureKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(rec) != wiring.PostureStrict {
		t.Errorf("recorded posture = %q, want %q", rec, wiring.PostureStrict)
	}

	runPostureFirstBoot(t, n)
	if got := len(postureEvents(t, trail)); got != 1 {
		t.Errorf("posture events after a second first boot = %d, want 1", got)
	}
}

// TestNode_RecordSeparationPosture_NoServicesIsANoOp: a node that has not
// built its services has no store to record into and reports no error.
func TestNode_RecordSeparationPosture_NoServicesIsANoOp(t *testing.T) {
	n := &Node{}
	n.separationConfigured.Store(true)
	if err := n.recordSeparationPosture(context.Background()); err != nil {
		t.Fatalf("recordSeparationPosture: %v", err)
	}
}
