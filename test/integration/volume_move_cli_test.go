package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// volumeMoveDeadline bounds how long a test waits for a started move to
// finish: one node copies the tenant's directory into the destination pool,
// cuts the tenant record over and retires the source.
const volumeMoveDeadline = 3 * time.Minute

// cliVolumePoolMoveJSON is the slice of `qrdb volume-pool move-tenant|get-move
// --output json` the move tests assert on.
type cliVolumePoolMoveJSON struct {
	TenantID string `json:"tenantId"`
	FromPool string `json:"fromPool"`
	ToPool   string `json:"toPool"`
	State    string `json:"state"`
}

// TestVolumePool_CLI_MoveTenant_PerProtocol drives `qrdb volume-pool
// move-tenant`, `get-move` and `cancel-move` against a real qrdbd over every
// CLI transport. The node has two configured directory pools, "default" and
// "second", that every tenant is entitled to, and a third catalog pool,
// "stranger", that no tenant is entitled to. Per transport a volume tenant
// placed on "default" starts a move to "second": move-tenant and get-move show
// it, cancel-move aborts it, get-move follows it until it ends, and a
// cancelled tenant stays on "default". Per transport a second volume tenant on
// "default" moves to "second" without a cancel: get-move follows the move
// until it is gone, the tenant is then on "second", the key it wrote before
// the move reads back over its own CLI identity, and a write after the move
// reads back too. Refusals on every transport: a tenant
// with no move answers not found to get-move and cancel-move, a move to a
// pool the tenant is not entitled to is refused, and a principal without
// volume_pools:move is refused and starts nothing. The refusal of a move from
// an encrypted pool to an unencrypted one needs a LUKS pool; the handler tests
// of every transport prove it.
func TestVolumePool_CLI_MoveTenant_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startVolumeMoveNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Run("cancel", func(t *testing.T) {
				testVolumeMoveOverProtocol(t, node, proto)
			})
			t.Run("complete", func(t *testing.T) {
				testVolumeMoveCompletesOverProtocol(t, node, proto)
			})
		})
	}
}

func testVolumeMoveOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", strings.ToLower(proto.name), time.Now().UnixNano())
	tenantID := "vm-" + suffix
	dbName := "movedata-" + suffix
	const key = "greeting"
	value := "before-the-move-" + suffix

	createTenant(t, node, proto, tenantID, "Moving Tenant", "volume", true)
	if pool := volumeMoveTenantPool(t, node, proto, tenantID); pool != "default" {
		t.Fatalf("[%s] tenant %s placed on pool %q, want default", proto.name, tenantID, pool)
	}

	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "mover", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	if out, err := runQRDB(t, node.cli, conn, "database", "create", dbName, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, conn, "--database", dbName, "kv", "put", key, value); err != nil {
		t.Fatalf("[%s] tenant kv put: %v\noutput: %s", proto.name, err, out)
	}

	// --- no move yet: get-move and cancel-move answer not found -----------
	for _, verb := range []string{"get-move", "cancel-move"} {
		out, err := runCLIProto(t, node, proto, "volume-pool", verb, tenantID)
		if err == nil {
			t.Fatalf("[%s] volume-pool %s of a tenant with no move must fail, got: %s", proto.name, verb, out)
		}
		if !volumeMoveIsNoMove(out) {
			t.Fatalf("[%s] volume-pool %s of a tenant with no move: want not found, got: %s", proto.name, verb, out)
		}
	}

	// --- a pool the tenant is not entitled to is refused -------------------
	out, err := runCLIProto(t, node, proto, "volume-pool", "move-tenant", tenantID, "--to", "stranger")
	if err == nil {
		t.Fatalf("[%s] move-tenant to a pool the tenant is not entitled to must be refused, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "entitle") {
		t.Fatalf("[%s] move-tenant to an unentitled pool: want an entitlement refusal, got: %s", proto.name, out)
	}

	// --- a principal without volume_pools:move is refused ------------------
	if out, err := runQRDB(t, node.cli, conn, "volume-pool", "move-tenant", tenantID, "--to", "second"); err == nil {
		t.Fatalf("[%s] move-tenant by a principal without volume_pools:move must be refused, got: %s", proto.name, out)
	}
	if out, err := runCLIProto(t, node, proto, "volume-pool", "get-move", tenantID); err == nil {
		t.Fatalf("[%s] a refused move-tenant started a move: %s", proto.name, out)
	}

	// --- move-tenant --to second --------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "move-tenant", tenantID, "--to", "second")
	if err != nil {
		t.Fatalf("[%s] volume-pool move-tenant: %v\noutput: %s", proto.name, err, out)
	}
	var started cliVolumePoolMoveJSON
	if jerr := json.Unmarshal([]byte(out), &started); jerr != nil {
		t.Fatalf("[%s] decode move-tenant JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if started.TenantID != tenantID || started.FromPool != "default" || started.ToPool != "second" || started.State == "" {
		t.Fatalf("[%s] move-tenant = %+v, want tenant %s from default to second with a state", proto.name, started, tenantID)
	}

	// --- get-move shows the started move -------------------------------------
	// A move that already ended answers no move; move-tenant showed it.
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", tenantID)
	if err != nil && !volumeMoveIsNoMove(out) {
		t.Fatalf("[%s] volume-pool get-move: %v\noutput: %s", proto.name, err, out)
	}
	if err == nil {
		var m cliVolumePoolMoveJSON
		if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
			t.Fatalf("[%s] decode get-move JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if m.TenantID != tenantID || m.FromPool != "default" || m.ToPool != "second" || m.State == "" {
			t.Fatalf("[%s] get-move = %+v, want the move of %s from default to second", proto.name, m, tenantID)
		}
	}

	// --- cancel-move aborts it, or answers that it has ended or is past
	// its commit point --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "cancel-move", tenantID)
	cancelled := err == nil
	switch {
	case cancelled:
		var m cliVolumePoolMoveJSON
		if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
			t.Fatalf("[%s] decode cancel-move JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if m.TenantID != tenantID || m.State != "aborting" {
			t.Fatalf("[%s] cancel-move = %+v, want the move of %s aborting", proto.name, m, tenantID)
		}
	case volumeMoveIsNoMove(out), strings.Contains(strings.ToLower(out), "cannot cancel"):
	default:
		t.Fatalf("[%s] volume-pool cancel-move: %v\noutput: %s", proto.name, err, out)
	}

	// --- get-move follows the move until it is gone ---------------------------
	deadline := time.Now().Add(volumeMoveDeadline)
	for {
		out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", tenantID)
		if err != nil {
			if !volumeMoveIsNoMove(out) {
				t.Fatalf("[%s] volume-pool get-move: %v\noutput: %s", proto.name, err, out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] move of %s did not end within %s; last get-move: %s", proto.name, tenantID, volumeMoveDeadline, out)
		}
	}

	// --- a cancelled move leaves the tenant on its source pool ---------------
	if cancelled {
		if pool := volumeMoveTenantPool(t, node, proto, tenantID); pool != "default" {
			t.Fatalf("[%s] after cancel-move tenant %s is on pool %q, want default", proto.name, tenantID, pool)
		}
	}
}

// testVolumeMoveCompletesOverProtocol moves a fresh volume tenant from
// "default" to "second" over proto and proves the move completed: the tenant
// is placed on "second" and its data, written before the move, reads back
// over its own CLI identity, as does a write made after it.
func testVolumeMoveCompletesOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", strings.ToLower(proto.name), time.Now().UnixNano())
	tenantID := "vc-" + suffix
	dbName := "moved-" + suffix
	const key, afterKey = "greeting", "after"
	value := "before-the-move-" + suffix
	afterValue := "after-the-move-" + suffix

	createTenant(t, node, proto, tenantID, "Moved Tenant", "volume", true)
	if pool := volumeMoveTenantPool(t, node, proto, tenantID); pool != "default" {
		t.Fatalf("[%s] tenant %s placed on pool %q, want default", proto.name, tenantID, pool)
	}
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "mover", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	if out, err := runQRDB(t, node.cli, conn, "database", "create", dbName, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, conn, "--database", dbName, "kv", "put", key, value); err != nil {
		t.Fatalf("[%s] tenant kv put: %v\noutput: %s", proto.name, err, out)
	}

	out, err := runCLIProto(t, node, proto, "--output", "json", "volume-pool", "move-tenant", tenantID, "--to", "second")
	if err != nil {
		t.Fatalf("[%s] volume-pool move-tenant: %v\noutput: %s", proto.name, err, out)
	}

	deadline := time.Now().Add(volumeMoveDeadline)
	for {
		out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", tenantID)
		if err != nil {
			if !volumeMoveIsNoMove(out) {
				t.Fatalf("[%s] volume-pool get-move: %v\noutput: %s", proto.name, err, out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] move of %s did not end within %s; last get-move: %s", proto.name, tenantID, volumeMoveDeadline, out)
		}
	}

	if pool := volumeMoveTenantPool(t, node, proto, tenantID); pool != "second" {
		t.Fatalf("[%s] after the move tenant %s is on pool %q, want second", proto.name, tenantID, pool)
	}
	out, err = runQRDB(t, node.cli, conn, "--database", dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("[%s] tenant kv get after the move: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, value) {
		t.Fatalf("[%s] kv get %s after the move = %q, want %q", proto.name, key, out, value)
	}
	if out, err := runQRDB(t, node.cli, conn, "--database", dbName, "kv", "put", afterKey, afterValue); err != nil {
		t.Fatalf("[%s] tenant kv put after the move: %v\noutput: %s", proto.name, err, out)
	}
	out, err = runQRDB(t, node.cli, conn, "--database", dbName, "kv", "get", afterKey)
	if err != nil {
		t.Fatalf("[%s] tenant kv get of the post-move key: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, afterValue) {
		t.Fatalf("[%s] kv get %s after the move = %q, want %q", proto.name, afterKey, out, afterValue)
	}
}

// volumeMoveIsNoMove reports whether a CLI answer is the no-move refusal,
// whose wording differs per transport (gRPC "not found", REST 404).
func volumeMoveIsNoMove(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "not found") || strings.Contains(lower, "no move journalled") || strings.Contains(lower, "status 404")
}

// volumeMoveTenantPool returns the volume pool `qrdb tenant get` names for
// tenantID.
func volumeMoveTenantPool(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) string {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get %s: %v\noutput: %s", proto.name, tenantID, err, out)
	}
	var info struct {
		VolumePool string `json:"volume_pool"`
	}
	if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
		t.Fatalf("[%s] decode tenant get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return info.VolumePool
}

// volumeMoveYAML is startVolumeMoveNode's qrdbd configuration: a passphrase
// system barrier, two directory pools and default entitlements that admit
// volume tenants on both, "default" first.
func volumeMoveYAML(defaultDir, secondDir string) string {
	return fmt.Sprintf(`seal:
  enabled: true
  strategy: passphrase
storage:
  volume_pools:
    - name: default
      path: %s
    - name: second
      path: %s
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - volume
    allow_volume_storage: true
    allow_system_managed_barrier: true
    allowed_volume_pools:
      - default
      - second
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
`, defaultDir, secondDir)
}

// startVolumeMoveNode boots a single-node qrdbd like
// startTenantEntitlementNode (every transport, TLS, auth, an unsealed
// passphrase system barrier) on volumeMoveYAML, and registers the
// "default", "second" and "stranger" pools in the replicated catalog.
func startVolumeMoveNode(t *testing.T) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(volumeMoveYAML(t.TempDir(), t.TempDir())), 0o600); err != nil {
		t.Fatalf("startVolumeMoveNode: config: %v", err)
	}

	// A Unix socket path is limited to ~108 bytes; t.TempDir() can exceed it.
	sockDir, err := os.MkdirTemp("", "qrdb-vmove-")
	if err != nil {
		t.Fatalf("startVolumeMoveNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp
	restPort, grpcPort, mcpPort, quicPort := tcpPorts[0], tcpPorts[1], tcpPorts[2], quicPorts[0]

	const passphrase = "volume-move-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	args = append(args, ca.tlsFlags(t)...)

	proc := startNode(t, bin, args, "[volume-move] ")

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	clientCert, clientKey := ca.boundAdminCert(t, dataDir)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		passphrase: passphrase,
		bin:        bin,
		startArgs:  args,
		proc:       proc,
	}

	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	// The replicated catalog has no pool until an API call creates one
	// (docs/volume-pools/architecture.md D2).
	for _, pool := range []string{"default", "second", "stranger"} {
		createVolumePoolCLI(t, cli, n.cliArgsFor(multiProtocols[0]), pool)
	}
	return n
}
