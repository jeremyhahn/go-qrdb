//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestBarrierTenant_DedicatedWALRefused_PerProtocol drives `qrdb database
// create --backend raft --dedicated-wal` for a barrier-isolated tenant over
// every transport. A barrier tenant's raft-backed database is served from
// the shared system store, so a dedicated group and WAL would never carry its
// data: the create is refused with the typed off-data-path error and nothing
// is listed; the name then creates on the default backend and serves a KV
// round trip.
func TestBarrierTenant_DedicatedWALRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testBarrierTenantDedicatedWALOverProtocol(t, node, proto)
		})
	}
}

func testBarrierTenantDedicatedWALOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "barrier-wal-" + suffix
	createTenant(t, node, proto, tenantID, "Barrier WAL", "barrier", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "db-user", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

	db := "orders-" + suffix
	out, err := runQRDB(t, node.cli, conn, "database", "create", db, "--backend", "raft", "--dedicated-wal")
	if err == nil {
		t.Fatalf("[%s] database create %s --dedicated-wal succeeded for a barrier tenant\noutput: %s", proto.name, db, out)
	}
	for _, want := range []string{"the shared system store", "dedicated WAL would never carry its data"} {
		if !strings.Contains(out, want) {
			t.Fatalf("[%s] refusal output lacks %q:\n%s", proto.name, want, out)
		}
	}
	if out, err := runQRDB(t, node.cli, conn, "database", "list"); err != nil || strings.Contains(out, db) {
		t.Fatalf("[%s] database list after the refusal (err=%v) names %s:\n%s", proto.name, err, db, out)
	}

	// The refusal persisted nothing: the name is free for a database on the
	// tenant's default backend, which serves a KV round trip.
	if out, err := runQRDB(t, node.cli, conn, "database", "create", db); err != nil {
		t.Fatalf("[%s] database create %s after the refusal: %v\noutput: %s", proto.name, db, err, out)
	}
	const key, value = "greeting", "hello"
	if out, err := runQRDBRetryingUnavailable(t, node.cli, conn, "--database", db, "kv", "put", key, value); err != nil {
		t.Fatalf("[%s] kv put into %s: %v\noutput: %s", proto.name, db, err, out)
	}
	out, err = runQRDB(t, node.cli, conn, "--database", db, "kv", "get", key)
	if err != nil || strings.TrimSpace(out) != value {
		t.Fatalf("[%s] kv get %s from %s = %q (err=%v), want %q", proto.name, key, db, out, err, value)
	}
}
