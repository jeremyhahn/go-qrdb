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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// tenantStatusCLIJSON is the slice of `qrdb --output json tenant get|update`
// this suite reads.
type tenantStatusCLIJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// TestTenantStatus_CLI_SuspendRefusesItsPrincipals_PerProtocol drives a
// tenant's status through the qrdb CLI on every transport (docs/multi-tenancy,
// "Status"): a suspended tenant's principals are refused by the auth chain
// with the typed not-active error, reactivating restores them, another tenant
// is untouched throughout, and an unknown status and the one refused
// transition (disabled -> suspended) are refused without changing the
// stored status.
func TestTenantStatus_CLI_SuspendRefusesItsPrincipals_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantStatusOverProtocol(t, node, proto)
		})
	}
}

// TestTenantStatus_CLI_SuspendRefusesOpenSessions_PerProtocol opens a session
// for one tenant principal on every protocol -- each is served, so every
// transport's auth chain holds the tenant as active -- then suspends the tenant
// once, through the CLI, and requires the next operation of every one of those
// sessions to be refused. Reactivation readmits them all the same way.
func TestTenantStatus_CLI_SuspendRefusesOpenSessions_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)
	admin := multiProtocols[0]
	suffix := fmt.Sprintf("open-%d", time.Now().UnixNano())
	tenant := "status-open-" + suffix
	createTenant(t, node, admin, tenant, "Open Session Tenant", "prefix", false)
	cert, key := provisionTenantIdentity(t, node, tenant, "status-open-reader-"+suffix, iamPermissions)

	call := func(conn []string) (string, error) {
		return runQRDB(t, node.cli, conn, "roles", "list")
	}
	sessions := make(map[string][]string, len(multiProtocols))
	for _, proto := range multiProtocols {
		conn := tenantCLIArgsForProto(node, proto, cert, key)
		if out, err := call(conn); err != nil {
			t.Fatalf("[%s] opening the session: %v\noutput: %s", proto.name, err, out)
		}
		sessions[proto.name] = conn
	}

	if got := setTenantStatus(t, node, admin, tenant, "suspended"); got != "suspended" {
		t.Fatalf("tenant update --status suspended answered status %q", got)
	}
	for _, proto := range multiProtocols {
		if out, refused := awaitTenantRefusal(call, sessions[proto.name]); !refused {
			t.Errorf("[%s] the open session's next operation must be refused with %q within %v, got: %s",
				proto.name, tenantNotActive, tenantStatusSettle, out)
		}
	}

	if got := setTenantStatus(t, node, admin, tenant, "active"); got != "active" {
		t.Fatalf("tenant update --status active answered status %q", got)
	}
	for _, proto := range multiProtocols {
		if out, served := awaitTenantServed(call, sessions[proto.name]); !served {
			t.Errorf("[%s] the session was not served within %v of reactivation: %s",
				proto.name, tenantStatusSettle, out)
		}
	}
}

// tenantNotActive is the typed refusal every transport carries for a principal
// whose tenant is not active.
const tenantNotActive = "tenant is not active"

// tenantStatusSettle bounds how long a status change takes to reach a node's
// auth chain: the chain answers from a cache bounded by
// middleware.TenantStatusTTL, so the bound leaves a wide margin over it.
const tenantStatusSettle = 5 * middleware.TenantStatusTTL

// tenantStatusPoll is the interval the status waits poll at.
const tenantStatusPoll = 100 * time.Millisecond

// awaitTenantRefusal polls call over conn until it is refused for its
// tenant's status, and returns the last output and whether it was.
func awaitTenantRefusal(call func([]string) (string, error), conn []string) (string, bool) {
	var out string
	refused := pollUntil(tenantStatusSettle, tenantStatusPoll, func() bool {
		var err error
		out, err = call(conn)
		return err != nil && strings.Contains(out, tenantNotActive)
	})
	return out, refused
}

// awaitTenantServed polls call over conn until it is served, and returns the
// last output and whether it was.
func awaitTenantServed(call func([]string) (string, error), conn []string) (string, bool) {
	var out string
	served := pollUntil(tenantStatusSettle, tenantStatusPoll, func() bool {
		var err error
		out, err = call(conn)
		return err == nil
	})
	return out, served
}

func testTenantStatusOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "status-a-" + suffix
	tenantB := "status-b-" + suffix
	createTenant(t, node, proto, tenantA, "Status Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "Status Tenant B", "prefix", false)
	certA, keyA := provisionTenantIdentity(t, node, tenantA, "status-reader-"+suffix, iamPermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "status-reader-"+suffix, iamPermissions)
	connA := tenantCLIArgsForProto(node, proto, certA, keyA)
	connB := tenantCLIArgsForProto(node, proto, certB, keyB)

	// A tenant principal's own-tenant call: refused only by the tenant's
	// status, never by RBAC, since the role grants it.
	call := func(conn []string) (string, error) {
		return runQRDB(t, node.cli, conn, "roles", "list")
	}
	mustServe := func(conn []string, who, when string) {
		t.Helper()
		if out, err := call(conn); err != nil {
			t.Fatalf("[%s] %s's principal %s: %v\noutput: %s", proto.name, who, when, err, out)
		}
	}
	// mustRefuseNotActive asserts the auth chain refused tenant A's principal
	// for its tenant's status, with the typed refusal every transport carries.
	mustRefuseNotActive := func(status string) {
		t.Helper()
		if out, refused := awaitTenantRefusal(call, connA); !refused {
			t.Fatalf("[%s] a %s tenant's principal must be refused with %q within %v, got: %s",
				proto.name, status, tenantNotActive, tenantStatusSettle, out)
		}
	}
	mustServe(connA, tenantA, "while active")
	mustServe(connB, tenantB, "while active")

	if got := setTenantStatus(t, node, proto, tenantA, "suspended"); got != "suspended" {
		t.Fatalf("[%s] tenant update --status suspended answered status %q", proto.name, got)
	}
	if got := tenantStatusOf(t, node, proto, tenantA); got != "suspended" {
		t.Fatalf("[%s] tenant get after suspend: status %q, want suspended", proto.name, got)
	}

	// The chain reads the status through a cache bounded by
	// TenantStatusTTL, so the refusal lands within that bound.
	mustRefuseNotActive("suspended")
	mustServe(connB, tenantB, "while "+tenantA+" is suspended")

	if got := setTenantStatus(t, node, proto, tenantA, "active"); got != "active" {
		t.Fatalf("[%s] tenant update --status active answered status %q", proto.name, got)
	}
	if out, served := awaitTenantServed(call, connA); !served {
		t.Fatalf("[%s] %s's principal was not served within %v of reactivation: %s",
			proto.name, tenantA, tenantStatusSettle, out)
	}

	// An unknown status is refused and nothing changes.
	if out, err := runCLIProto(t, node, proto, "tenant", "update", tenantA, "--status", "paused"); err == nil {
		t.Fatalf("[%s] tenant update --status paused succeeded: %s", proto.name, out)
	} else if !strings.Contains(out, "invalid status") {
		t.Fatalf("[%s] tenant update --status paused must be refused as an invalid status, got: %s", proto.name, out)
	}
	if got := tenantStatusOf(t, node, proto, tenantA); got != "active" {
		t.Fatalf("[%s] an invalid status left status %q, want active", proto.name, got)
	}

	// Disabled -> suspended is the one refused transition.
	if got := setTenantStatus(t, node, proto, tenantA, "disabled"); got != "disabled" {
		t.Fatalf("[%s] tenant update --status disabled answered status %q", proto.name, got)
	}
	mustRefuseNotActive("disabled")
	if out, err := runCLIProto(t, node, proto, "tenant", "update", tenantA, "--status", "suspended"); err == nil {
		t.Fatalf("[%s] disabled -> suspended succeeded: %s", proto.name, out)
	} else if !strings.Contains(out, "cannot transition") {
		t.Fatalf("[%s] disabled -> suspended must be refused as a transition, got: %s", proto.name, out)
	}
	if got := tenantStatusOf(t, node, proto, tenantA); got != "disabled" {
		t.Fatalf("[%s] a refused transition left status %q, want disabled", proto.name, got)
	}
	mustServe(connB, tenantB, "while "+tenantA+" is disabled")
}

// setTenantStatus sets tenantID's status as the system administrator and
// returns the status the update answered with.
func setTenantStatus(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID, status string) string {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "tenant", "update", tenantID, "--status", status)
	if err != nil {
		t.Fatalf("[%s] tenant update --status %s: %v\noutput: %s", proto.name, status, err, out)
	}
	return decodeTenantStatus(t, proto, out)
}

// tenantStatusOf reads tenantID's status as the system administrator.
func tenantStatusOf(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) string {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get: %v\noutput: %s", proto.name, err, out)
	}
	return decodeTenantStatus(t, proto, out)
}

func decodeTenantStatus(t *testing.T, proto multiProtocol, out string) string {
	t.Helper()
	var got tenantStatusCLIJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("[%s] decode tenant JSON: %v\noutput: %s", proto.name, err, out)
	}
	return got.Status
}
