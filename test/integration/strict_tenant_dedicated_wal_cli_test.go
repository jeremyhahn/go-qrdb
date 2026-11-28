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

// vectorOnlyPermissions lets a tenant identity create and drop a vector
// namespace without any KV permission, so the only data the tenant holds in
// the shared default store is a vector namespace.
var vectorOnlyPermissions = []string{
	"databases:read", "databases:list",
	"vector:namespace_create", "vector:namespace_delete",
	"vector:namespace_list", "vector:namespace_info",
	"admin:health",
}

// TestStrictTenant_VectorOnlyHomeTenantTighteningSucceeds_PerProtocol proves,
// over every transport, that a barrier tenant whose only data is a vector
// namespace can be tightened to strict: a barrier tenant's vectors live in
// its tenant-keyed home, not on the node-keyed WAL, and the node-key log
// probe reads only the node-local vector store, so it reports nothing and
// the tenant's policy becomes strict.
func TestStrictTenant_VectorOnlyHomeTenantTighteningSucceeds_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testStrictTenantVectorOnlyTighteningOverProtocol(t, node, proto)
		})
	}
}

func testStrictTenantVectorOnlyTighteningOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "strict-vec-" + suffix
	createTenant(t, node, proto, tenantID, "Strict Vector Only", "barrier", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "vec-user", vectorOnlyPermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	sys := node.cliArgsFor(proto)

	ns := "embeddings-" + suffix
	if out, err := runQRDB(t, node.cli, conn, "vector", "namespace", "create",
		"--namespace", ns, "--dimension", "2", "--distance-metric", "cosine"); err != nil {
		t.Fatalf("[%s] vector namespace create in the tenant's home: %v\noutput: %s", proto.name, err, out)
	}

	if out, err := runQRDB(t, node.cli, sys, "tenant", "update", tenantID, "--isolation-policy", "strict"); err != nil {
		t.Fatalf("[%s] tightening a tenant whose vectors are in its home to strict: %v\noutput: %s", proto.name, err, out)
	}
	got, err := runQRDB(t, node.cli, sys, "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get after the tightening: %v\noutput: %s", proto.name, err, got)
	}
	if !strings.Contains(got, "Isolation policy: strict") {
		t.Fatalf("[%s] tenant policy is not strict after the tightening\noutput: %s", proto.name, got)
	}
}

// sqlDefaultDatabaseName is the SQL default database the node in
// TestStrictTenant_SQLDefaultDatabaseTenantTighteningRefused_PerProtocol
// declares with --sql-database.
const sqlDefaultDatabaseName = "sqldefault"

// TestStrictTenant_SQLDefaultDatabaseTenantTighteningRefused_PerProtocol
// proves, through the real qrdbd binary, that the strict tightening's
// node-key log probe reads the SQL default database: a non-strict barrier
// tenant that wrote under the SQL default (which every tenant may address in
// the shared store) is refused the tightening to strict with the probe's
// "has databases on the node-keyed WAL" error, over every transport, and its
// policy stays non-strict. The SQL default reaches the probe only through
// qrdbd's SharedDatabaseNames wiring (lifecycle.SharedNames in
// cmd/qrdbd/start.go); the audit-route refusal alone does not name the
// node-keyed WAL, so it cannot pass this test.
func TestStrictTenant_SQLDefaultDatabaseTenantTighteningRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNodeWithArgs(t, []string{"--sql-database", sqlDefaultDatabaseName})
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testStrictTenantSQLDefaultTighteningOverProtocol(t, node, proto)
		})
	}
}

func testStrictTenantSQLDefaultTighteningOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "strict-sqldef-" + suffix
	createTenant(t, node, proto, tenantID, "Strict SQL Default", "barrier", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "sqldef-user", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	sys := node.cliArgsFor(proto)

	if out, err := runQRDBRetryingUnavailable(t, node.cli, conn, "--database", sqlDefaultDatabaseName, "kv", "put", "greeting", "hello"); err != nil {
		t.Fatalf("[%s] kv put into the SQL default database %s: %v\noutput: %s", proto.name, sqlDefaultDatabaseName, err, out)
	}

	out, err := runQRDB(t, node.cli, sys, "tenant", "update", tenantID, "--isolation-policy", "strict")
	if err == nil {
		t.Fatalf("[%s] tightening a tenant with data under the SQL default database to strict succeeded\noutput: %s", proto.name, out)
	}
	for _, want := range []string{tenantID, "node-keyed WAL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("[%s] tightening refusal output lacks %q:\n%s", proto.name, want, out)
		}
	}
	got, err := runQRDB(t, node.cli, sys, "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get after the refusal: %v\noutput: %s", proto.name, err, got)
	}
	if strings.Contains(got, "Isolation policy: strict") {
		t.Fatalf("[%s] tenant policy is strict after a refused tightening\noutput: %s", proto.name, got)
	}
}
