//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestStrictTenant_DatabaseNeedsDedicatedWAL_PerProtocol drives
// docs/multi-tenancy/engineering.md phase 5, G1 through the CLI on every
// protocol: under a strict cluster floor (tenant.isolation_policy: strict)
// every isolated tenant is strict, so its raft database is refused without a
// dedicated WAL (FailedPrecondition, naming --dedicated-wal) and created with
// one.
func TestStrictTenant_DatabaseNeedsDedicatedWAL_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t, "isolation_policy: strict")
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testStrictTenantDedicatedWALOverProtocol(t, node, proto)
		})
	}
}

func testStrictTenantDedicatedWALOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "strict-wal-" + suffix
	createTenant(t, node, proto, tenantID, "Strict WAL", "raft", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "db-user", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

	shared := "shared-" + suffix
	out, err := runQRDB(t, node.cli, conn, "database", "create", shared, "--backend", "raft")
	if err == nil {
		t.Fatalf("[%s] database create %s without --dedicated-wal succeeded for a strict tenant\noutput: %s", proto.name, shared, out)
	}
	for _, want := range []string{"strict isolation policy", "dedicated WAL", "--dedicated-wal"} {
		if !strings.Contains(out, want) {
			t.Fatalf("[%s] refusal output lacks %q:\n%s", proto.name, want, out)
		}
	}
	if out, err := runQRDB(t, node.cli, conn, "database", "list"); err != nil || strings.Contains(out, shared) {
		t.Fatalf("[%s] database list after the refusal (err=%v) names %s:\n%s", proto.name, err, shared, out)
	}

	dedicated := "dedicated-" + suffix
	if out, err := runQRDB(t, node.cli, conn, "database", "create", dedicated, "--backend", "raft", "--dedicated-wal"); err != nil {
		t.Fatalf("[%s] database create %s --dedicated-wal: %v\noutput: %s", proto.name, dedicated, err, out)
	}
	const key, value = "greeting", "hello"
	// database create returns before the new group has elected a leader, so
	// the first write can meet the documented retryable 503.
	if out, err := runQRDBRetryingUnavailable(t, node.cli, conn, "--database", dedicated, "kv", "put", key, value); err != nil {
		t.Fatalf("[%s] kv put into %s: %v\noutput: %s", proto.name, dedicated, err, out)
	}
	out, err = runQRDB(t, node.cli, conn, "--database", dedicated, "kv", "get", key)
	if err != nil || strings.TrimSpace(out) != value {
		t.Fatalf("[%s] kv get %s from %s = %q (err=%v), want %q", proto.name, key, dedicated, out, err, value)
	}
}

// TestStrictTenant_DefaultDatabaseWriteRefused_PerProtocol drives G1 for the
// implicit default database through the CLI on every protocol: under a
// strict cluster floor a barrier tenant's KV put with no --database (and
// with the default name) would land in the shared system shard, whose WAL is
// under the node key, so it is refused (FailedPrecondition / 409, naming the
// tenant) and nothing is stored: a read afterwards is refused the same way
// and returns no value.
func TestStrictTenant_DefaultDatabaseWriteRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t, "isolation_policy: strict")
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testStrictTenantDefaultDatabaseOverProtocol(t, node, proto)
		})
	}
}

func testStrictTenantDefaultDatabaseOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "strict-default-" + suffix
	createTenant(t, node, proto, tenantID, "Strict Default", "barrier", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "db-user", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

	const key, value = "greeting", "hello"
	for _, dbArgs := range [][]string{nil, {"--database", "system"}} {
		put := append(append([]string{}, dbArgs...), "kv", "put", key, value)
		out, err := runQRDB(t, node.cli, conn, put...)
		if err == nil {
			t.Fatalf("[%s] %v succeeded for a strict tenant's shared default store\noutput: %s", proto.name, put, out)
		}
		for _, want := range []string{tenantID, "strict isolation policy", "dedicated WAL"} {
			if !strings.Contains(out, want) {
				t.Fatalf("[%s] %v refusal output lacks %q:\n%s", proto.name, put, want, out)
			}
		}
		get := append(append([]string{}, dbArgs...), "kv", "get", key)
		out, err = runQRDB(t, node.cli, conn, get...)
		if err == nil || strings.Contains(out, value) {
			t.Fatalf("[%s] %v after the refusal = %q (err=%v), want a refusal and no value", proto.name, get, out, err)
		}
	}
}
