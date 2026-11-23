//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	TenantID    string   `json:"tenantId"`
	FromPool    string   `json:"fromPool"`
	ToPool      string   `json:"toPool"`
	State       string   `json:"state"`
	NodesCopied []string `json:"nodesCopied"`
	StepNode    string   `json:"stepNode"`
}

// TestVolumePool_CLI_MoveTenant_PerProtocol drives `qrdb volume-pool
// move-tenant`, `get-move` and `cancel-move` against a real qrdbd over every
// CLI transport. The node has two configured directory pools, "default" and
// "second", that every tenant is entitled to, and a third catalog pool,
// "stranger", that no tenant is entitled to. Per transport a volume tenant
// placed on "default" starts a move to "second": move-tenant and get-move show
// it, cancel-move aborts it, get-move follows it until it ends, and the
// cancelled tenant stays on "default" with every value it wrote readable and
// a new write landing. The case fails unless a cancel takes effect: a move
// that ends before its cancel is checked as completed and the cancel is
// retried on a fresh tenant, up to volumeMoveCancelAttempts. Per transport a second volume tenant on
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

	mt := seedVolumeMoveTenant(t, node, proto, "vm", 0)
	tenantID := mt.tenantID

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
	if out, err := runQRDB(t, node.cli, mt.conn, "volume-pool", "move-tenant", tenantID, "--to", "second"); err == nil {
		t.Fatalf("[%s] move-tenant by a principal without volume_pools:move must be refused, got: %s", proto.name, out)
	}
	if out, err := runCLIProto(t, node, proto, "volume-pool", "get-move", tenantID); err == nil {
		t.Fatalf("[%s] a refused move-tenant started a move: %s", proto.name, out)
	}

	// --- move-tenant, then cancel-move at once --------------------------------
	// The move worker may finish a single-node move before the cancel
	// lands; that tenant is checked as completed, and the cancel is retried
	// on a fresh tenant. The case passes only once a cancel took effect.
	for attempt := 0; ; attempt++ {
		if attempt >= volumeMoveCancelAttempts {
			t.Fatalf("[%s] no cancel-move took effect in %d attempts: every move ended before its cancel", proto.name, volumeMoveCancelAttempts)
		}
		if attempt > 0 {
			mt = seedVolumeMoveTenant(t, node, proto, "vm", attempt)
		}
		if volumeMoveStartAndCancel(t, node, proto, mt) {
			return
		}
		t.Logf("[%s] attempt %d: the move of %s ended before its cancel; retrying on a fresh tenant", proto.name, attempt+1, mt.tenantID)
	}
}

// volumeMoveCancelAttempts bounds the fresh tenants the cancel case tries
// before it fails: each attempt either cancels or proves a completed move.
const volumeMoveCancelAttempts = 5

// volumeMoveTenant is a volume tenant on "default" with one database holding
// key=value, written through conn (the tenant's own data-plane identity).
type volumeMoveTenant struct {
	tenantID, dbName, key, value string
	conn                         []string
}

// seedVolumeMoveTenant creates a volume tenant on "default", a pebble
// database and one key, and proves the tenant is placed on "default".
func seedVolumeMoveTenant(t *testing.T, node *multiProtoNode, proto multiProtocol, prefix string, attempt int) volumeMoveTenant {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d-%d", strings.ToLower(proto.name), attempt, time.Now().UnixNano())
	mt := volumeMoveTenant{
		tenantID: prefix + "-" + suffix,
		dbName:   "movedata-" + suffix,
		key:      "greeting",
		value:    "before-the-move-" + suffix,
	}
	createTenant(t, node, proto, mt.tenantID, "Moving Tenant", "volume", true)
	if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != "default" {
		t.Fatalf("[%s] tenant %s placed on pool %q, want default", proto.name, mt.tenantID, pool)
	}
	certPath, keyPath := provisionTenantIdentity(t, node, mt.tenantID, "mover", dataPlanePermissions)
	mt.conn = tenantCLIArgsForProto(node, proto, certPath, keyPath)
	if out, err := runQRDB(t, node.cli, mt.conn, "database", "create", mt.dbName, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, mt.conn, "--database", mt.dbName, "kv", "put", mt.key, mt.value); err != nil {
		t.Fatalf("[%s] tenant kv put: %v\noutput: %s", proto.name, err, out)
	}
	return mt
}

// volumeMoveStartAndCancel moves mt to "second", cancels the move at once
// and follows it until it ends. It reports whether the cancel took effect:
// the move turned aborting (or recorded the cancel against its running
// step), and the tenant stays on "default". A move past its commit point,
// or already gone, is checked as completed instead: the tenant is on
// "second". Either way every written value reads back, and a new write
// lands and reads back.
func volumeMoveStartAndCancel(t *testing.T, node *multiProtoNode, proto multiProtocol, mt volumeMoveTenant) bool {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "volume-pool", "move-tenant", mt.tenantID, "--to", "second")
	if err != nil {
		t.Fatalf("[%s] volume-pool move-tenant: %v\noutput: %s", proto.name, err, out)
	}
	var started cliVolumePoolMoveJSON
	if jerr := json.Unmarshal([]byte(out), &started); jerr != nil {
		t.Fatalf("[%s] decode move-tenant JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if started.TenantID != mt.tenantID || started.FromPool != "default" || started.ToPool != "second" || started.State == "" {
		t.Fatalf("[%s] move-tenant = %+v, want tenant %s from default to second with a state", proto.name, started, mt.tenantID)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "cancel-move", mt.tenantID)
	cancelled := err == nil
	switch {
	case cancelled:
		var m cliVolumePoolMoveJSON
		if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
			t.Fatalf("[%s] decode cancel-move JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		// A cancel that lands while a step runs is recorded against the
		// step (state still copying); the step's end then turns the move
		// aborting. The tenant ending on "default" below proves it.
		if m.TenantID != mt.tenantID || (m.State != "aborting" && m.State != "copying") {
			t.Fatalf("[%s] cancel-move = %+v, want the move of %s aborting or its cancel recorded", proto.name, m, mt.tenantID)
		}
	case volumeMoveIsNoMove(out), strings.Contains(strings.ToLower(out), "cannot cancel"):
	default:
		t.Fatalf("[%s] volume-pool cancel-move: %v\noutput: %s", proto.name, err, out)
	}

	// --- get-move follows the move until it is gone ---------------------------
	deadline := time.Now().Add(volumeMoveDeadline)
	for {
		out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", mt.tenantID)
		if err != nil {
			if !volumeMoveIsNoMove(out) {
				t.Fatalf("[%s] volume-pool get-move: %v\noutput: %s", proto.name, err, out)
			}
			break
		}
		var m cliVolumePoolMoveJSON
		if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
			t.Fatalf("[%s] decode get-move JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if cancelled && (m.State == "copied" || m.State == "cutover") {
			t.Fatalf("[%s] a cancelled move of %s went on to %s", proto.name, mt.tenantID, m.State)
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] move of %s did not end within %s; last get-move: %s", proto.name, mt.tenantID, volumeMoveDeadline, out)
		}
	}

	wantPool := "second"
	if cancelled {
		wantPool = "default"
	}
	if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != wantPool {
		t.Fatalf("[%s] after the move (cancelled=%v) tenant %s is on pool %q, want %s", proto.name, cancelled, mt.tenantID, pool, wantPool)
	}

	// --- every written value reads back, and a new write lands -------------
	afterKey, afterValue := "after", "after-the-cancel-"+mt.tenantID
	volumeMoveExpectValue(t, node, proto, mt, mt.key, mt.value)
	if out, err := runQRDB(t, node.cli, mt.conn, "--database", mt.dbName, "kv", "put", afterKey, afterValue); err != nil {
		t.Fatalf("[%s] tenant kv put after the move ended: %v\noutput: %s", proto.name, err, out)
	}
	volumeMoveExpectValue(t, node, proto, mt, afterKey, afterValue)
	volumeMoveExpectValue(t, node, proto, mt, mt.key, mt.value)
	return cancelled
}

// volumeMoveExpectValue reads key back through the tenant's own identity
// and fails unless it holds want.
func volumeMoveExpectValue(t *testing.T, node *multiProtoNode, proto multiProtocol, mt volumeMoveTenant, key, want string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, mt.conn, "--database", mt.dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("[%s] tenant %s kv get %s: %v\noutput: %s", proto.name, mt.tenantID, key, err, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("[%s] tenant %s kv get %s = %q, want %q", proto.name, mt.tenantID, key, out, want)
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
      - raft
    allow_volume_storage: true
    allow_dedicated_raft: true
    allow_dedicated_wal: true
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

// TestVolumePool_CLI_MoveTenant_ThreeNode_ReplicaRestart moves a raft
// tenant placed on all three nodes from pool default to second while one
// replica restarts: the node is stopped as soon as move-tenant returns, so
// the move cannot complete until that node, after its restart, closes its
// stores, copies, reopens from second and catches up. Every value then reads
// back from every node, and a new write lands and replicates.
// TestVolumePool_CLI_MoveTenant_ThreeNode_DestinationUnreadyRefused proves
// the decision's admit check (docs/decisions/volume-pool-tenant-moves.md,
// Q3) end to end on every transport: a three-node cluster where node 3's
// "second" pool path does not exist and node 3 boots degraded
// (storage.allow_degraded_volume_boot) with that pool skipped. A move of a
// raft tenant placed on all three nodes from default to second is refused
// at admit, naming node 3 (the journal node ID is the replica ID, fixture
// index + 1) and saying the destination pool is not ready. Nothing is
// journalled, the tenant stays on default on every node, no destination
// directory appears on nodes 1 and 2, and the tenant keeps reading and
// replicating writes everywhere.
func TestVolumePool_CLI_MoveTenant_ThreeNode_DestinationUnreadyRefused(t *testing.T) {
	t.Parallel()
	nodes, dirs := volumeMoveClusterStartWith(t, volumeMoveClusterUnreadySecondYAML(t))

	// One loop, no t.Run per transport, like _ReplicaRestart: the cluster
	// is shared by every pass. Every failure names its transport.
	for _, proto := range multiProtocols {
		mt, conns := volumeMoveClusterSeed(t, nodes, proto, "vu")

		out, err := runCLIProto(t, nodes[0], proto, "volume-pool", "move-tenant", mt.tenantID, "--to", "second")
		if err == nil {
			t.Fatalf("[%s] move-tenant to a pool unready on node 3 succeeded: %s", proto.name, out)
		}
		if !strings.Contains(out, "not ready") || !strings.Contains(out, `node "3"`) {
			t.Fatalf("[%s] move-tenant refusal = %q, want it to say the destination is not ready on node \"3\"", proto.name, out)
		}

		if out, err := runCLIProto(t, nodes[0], proto, "volume-pool", "get-move", mt.tenantID); err == nil || !volumeMoveIsNoMove(out) {
			t.Fatalf("[%s] get-move after a refused move = %q (err=%v), want no move", proto.name, out, err)
		}
		for i, node := range nodes {
			if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != "default" {
				t.Fatalf("[%s] node %d: after the refused move tenant %s is on pool %q, want default", proto.name, i+1, mt.tenantID, pool)
			}
		}
		for i := range 2 {
			if _, err := os.Stat(filepath.Join(dirs[i].second, mt.tenantID)); !os.IsNotExist(err) {
				t.Fatalf("[%s] node %d: destination directory for tenant %s after a refused move: stat err=%v, want not exist", proto.name, i+1, mt.tenantID, err)
			}
		}
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)

		afterKey, afterValue := "after", "after-the-refusal-"+mt.tenantID
		volumeMoveClusterPut(t, nodes[0], proto, mt, conns[0], afterKey, afterValue)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, afterKey, afterValue)
	}
}

func TestVolumePool_CLI_MoveTenant_ThreeNode_ReplicaRestart(t *testing.T) {
	t.Parallel()
	nodes, dirs := volumeMoveClusterStart(t)

	// No t.Run per transport: the restarted node's process (and its log
	// writer) is owned by the test that starts it, so it must outlive one
	// transport's pass. Every failure names its transport.
	for _, proto := range multiProtocols {
		mt, conns := volumeMoveClusterSeed(t, nodes, proto, "vr")
		volumeMoveClusterStartMove(t, nodes[0], proto, mt.tenantID)

		nodes[2].restart(t, fmt.Sprintf("[vmove-cluster-node3-%s] ", strings.ToLower(proto.name)))

		volumeMoveClusterWaitGone(t, nodes[0], proto, mt.tenantID)
		for i, node := range nodes {
			if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != "second" {
				t.Fatalf("[%s] node %d: after the move tenant %s is on pool %q, want second", proto.name, i+1, mt.tenantID, pool)
			}
		}
		volumeMoveClusterExpectDirs(t, proto, dirs, mt.tenantID, false, true)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)

		afterKey, afterValue := "after", "after-the-move-"+mt.tenantID
		volumeMoveClusterPut(t, nodes[2], proto, mt, conns[2], afterKey, afterValue)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, afterKey, afterValue)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)
	}
}

// TestVolumePool_CLI_MoveTenant_ThreeNode_CancelAfterAckedWrite starts a
// move of a raft tenant on all three nodes, stops one replica so the move
// cannot complete, has the two running replicas acknowledge a new write
// mid-move, cancels, and restarts the stopped replica: the move rolls back,
// the tenant stays on default, and every node serves every acknowledged
// write.
func TestVolumePool_CLI_MoveTenant_ThreeNode_CancelAfterAckedWrite(t *testing.T) {
	t.Parallel()
	nodes, dirs := volumeMoveClusterStart(t)

	// No t.Run per transport: the restarted node's process (and its log
	// writer) is owned by the test that starts it, so it must outlive one
	// transport's pass. Every failure names its transport.
	for _, proto := range multiProtocols {
		mt, conns := volumeMoveClusterSeed(t, nodes, proto, "vk")
		volumeMoveClusterStartMove(t, nodes[0], proto, mt.tenantID)
		nodes[2].stop(t)

		ackedKey, ackedValue := "acked", "acked-mid-move-"+mt.tenantID
		volumeMoveClusterPut(t, nodes[0], proto, mt, conns[0], ackedKey, ackedValue)

		out, err := runCLIProto(t, nodes[0], proto, "--output", "json", "volume-pool", "cancel-move", mt.tenantID)
		if err != nil {
			t.Fatalf("[%s] volume-pool cancel-move with a replica down: %v\noutput: %s", proto.name, err, out)
		}
		var m cliVolumePoolMoveJSON
		if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
			t.Fatalf("[%s] decode cancel-move JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if m.TenantID != mt.tenantID || (m.State != "aborting" && m.State != "copying") {
			t.Fatalf("[%s] cancel-move = %+v, want the move of %s aborting or its cancel recorded", proto.name, m, mt.tenantID)
		}

		nodes[2].startAgain(t, fmt.Sprintf("[vmove-cluster-node3-%s] ", strings.ToLower(proto.name)))
		volumeMoveClusterWaitGone(t, nodes[0], proto, mt.tenantID)
		for i, node := range nodes {
			if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != "default" {
				t.Fatalf("[%s] node %d: after the cancel tenant %s is on pool %q, want default", proto.name, i+1, mt.tenantID, pool)
			}
		}
		volumeMoveClusterExpectDirs(t, proto, dirs, mt.tenantID, true, false)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, ackedKey, ackedValue)

		afterKey, afterValue := "after", "after-the-cancel-"+mt.tenantID
		volumeMoveClusterPut(t, nodes[2], proto, mt, conns[2], afterKey, afterValue)
		volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, afterKey, afterValue)
	}
}

// TestVolumePool_CLI_MoveTenant_ThreeNode_RestartWhileSettling restarts the
// replica whose copy step is settling (get-move shows state copying with the
// step node already in nodesCopied) and requires the move to complete anyway:
// every node on the second pool, every value readable everywhere, a write on
// the restarted node replicated, and no tenant directory left in the default
// pool or staging directory anywhere. A settle can end before a poll sees it,
// so each transport tries up to volumeMoveCancelAttempts fresh tenants.
func TestVolumePool_CLI_MoveTenant_ThreeNode_RestartWhileSettling(t *testing.T) {
	t.Parallel()
	nodes, dirs := volumeMoveClusterStart(t)

	// No t.Run per transport: the restarted node's process (and its log
	// writer) is owned by the test that starts it, so it must outlive one
	// transport's pass. Every failure names its transport.
	for _, proto := range multiProtocols {
		hit := false
		for attempt := 0; attempt < volumeMoveCancelAttempts && !hit; attempt++ {
			mt, conns := volumeMoveClusterSeed(t, nodes, proto, "vs")
			volumeMoveClusterStartMove(t, nodes[0], proto, mt.tenantID)

			stepIdx := volumeMoveClusterSettlingNode(t, nodes[0], proto, mt.tenantID)
			if stepIdx < 0 {
				volumeMoveClusterWaitGone(t, nodes[0], proto, mt.tenantID)
				continue
			}
			hit = true

			nodes[stepIdx].stop(t)
			nodes[stepIdx].startAgain(t, fmt.Sprintf("[vmove-settle-node%d-%s] ", stepIdx+1, strings.ToLower(proto.name)))

			volumeMoveClusterWaitGone(t, nodes[(stepIdx+1)%len(nodes)], proto, mt.tenantID)
			for i, node := range nodes {
				if pool := volumeMoveTenantPool(t, node, proto, mt.tenantID); pool != "second" {
					t.Fatalf("[%s] node %d: after a restart while node %d settled, tenant %s is on pool %q, want second", proto.name, i+1, stepIdx+1, mt.tenantID, pool)
				}
			}
			volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)

			afterKey, afterValue := "after", "after-the-settle-restart-"+mt.tenantID
			volumeMoveClusterPut(t, nodes[stepIdx], proto, mt, conns[stepIdx], afterKey, afterValue)
			volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, afterKey, afterValue)
			volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)
			volumeMoveClusterExpectDirs(t, proto, dirs, mt.tenantID, false, true)
		}
		if !hit {
			t.Fatalf("[%s] no move of %d tenants was seen with a copy step settling", proto.name, volumeMoveCancelAttempts)
		}
	}
}

// volumeMoveClusterSettlingNode polls get-move until the move of tenantID
// shows a settling copy step (state copying, stepNode already in nodesCopied)
// and returns the fixture index of that step's node, or -1 once the move has
// ended without a poll seeing it settle. The journal's node ID is the
// daemon's replica ID, which the cluster fixture sets to index+1.
func volumeMoveClusterSettlingNode(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) int {
	t.Helper()
	idx := -1
	var out string
	var err error
	if pollUntil(volumeMoveDeadline, 10*time.Millisecond, func() bool {
		out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", tenantID)
		if err != nil {
			return volumeMoveIsNoMove(out)
		}
		var m cliVolumePoolMoveJSON
		if json.Unmarshal([]byte(out), &m) != nil || m.State != "copying" || m.StepNode == "" {
			return false
		}
		for _, copied := range m.NodesCopied {
			if copied != m.StepNode {
				continue
			}
			for i := range 3 {
				if m.StepNode == strconv.Itoa(i+1) {
					idx = i
					return true
				}
			}
			t.Fatalf("[%s] move of %s settles on node %q, which is not a fixture replica ID", proto.name, tenantID, m.StepNode)
		}
		return false
	}) {
		return idx
	}
	t.Fatalf("[%s] move of %s neither settled nor ended within %s; last get-move (err=%v): %s", proto.name, tenantID, volumeMoveDeadline, err, out)
	return -1
}

// volumeMoveClusterYAML is volumeMoveYAML without its seal block, which the
// cluster fixture writes itself: one node's own pool directories.
func volumeMoveClusterYAML(defaultDir, secondDir string) string {
	return strings.TrimPrefix(volumeMoveYAML(defaultDir, secondDir), "seal:\n  enabled: true\n  strategy: passphrase\n")
}

// volumeMoveClusterStart starts a three-node cluster whose nodes each hold
// their own default and second pool directories, and creates both pools in
// the replicated catalog.
func volumeMoveClusterStart(t *testing.T) ([3]*multiProtoNode, [3]volumeMovePoolDirs) {
	t.Helper()
	return volumeMoveClusterStartWith(t, func(_ int, d volumeMovePoolDirs) string {
		return volumeMoveClusterYAML(d.def, d.second)
	})
}

// volumeMoveClusterStartWith is volumeMoveClusterStart with a per-node
// config: yaml builds node i's YAML from that node's pool directories.
func volumeMoveClusterStartWith(t *testing.T, yaml func(i int, d volumeMovePoolDirs) string) ([3]*multiProtoNode, [3]volumeMovePoolDirs) {
	t.Helper()
	var dirs [3]volumeMovePoolDirs
	for i := range dirs {
		dirs[i] = volumeMovePoolDirs{def: t.TempDir(), second: t.TempDir()}
	}
	nodes, _ := startMultiProtocolClusterWithNodeConfig(t, 0, func(i int) string {
		return yaml(i, dirs[i])
	})
	for _, pool := range []string{"default", "second"} {
		createVolumePoolCLI(t, nodes[0].cli, nodes[0].cliArgsFor(multiProtocols[0]), pool)
	}
	return nodes, dirs
}

// volumeMoveClusterUnreadySecondYAML is node i's config for the
// unready-destination test: node 3 (index 2) maps the "second" pool on a
// directory that does not exist and opts into the degraded boot
// (storage.allow_degraded_volume_boot), so it boots with that pool skipped
// and its node report does not show "second" ready. Node 3's dirs entry
// keeps an unused temporary second directory; its configured path is the
// missing one.
func volumeMoveClusterUnreadySecondYAML(t *testing.T) func(i int, d volumeMovePoolDirs) string {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	return func(i int, d volumeMovePoolDirs) string {
		if i != 2 {
			return volumeMoveClusterYAML(d.def, d.second)
		}
		return strings.Replace(volumeMoveClusterYAML(d.def, missing), "storage:\n", "storage:\n  allow_degraded_volume_boot: true\n", 1)
	}
}

// volumeMovePoolDirs is one cluster node's default and second pool directory.
type volumeMovePoolDirs struct {
	def, second string
}

// volumeMoveClusterExpectDirs requires, on every node, that the tenant's
// directory <pool>/<tenantID> exists in the default and second pools exactly
// as wanted, and that no copy-back staging directory is left behind: no
// <tenantID>.copyback in the default pool and no .qrdb-copyback inside either
// tenant directory.
func volumeMoveClusterExpectDirs(t *testing.T, proto multiProtocol, dirs [3]volumeMovePoolDirs, tenantID string, wantDefault, wantSecond bool) {
	t.Helper()
	exists := func(path string) bool {
		_, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("[%s] stat %s: %v", proto.name, path, err)
		}
		return err == nil
	}
	for i, d := range dirs {
		for _, c := range []struct {
			pool, dir string
			want      bool
		}{{"default", d.def, wantDefault}, {"second", d.second, wantSecond}} {
			tenantDir := filepath.Join(c.dir, tenantID)
			if got := exists(tenantDir); got != c.want {
				t.Fatalf("[%s] node %d: tenant %s directory in pool %s exists=%v, want %v", proto.name, i+1, tenantID, c.pool, got, c.want)
			}
			if exists(filepath.Join(tenantDir, ".qrdb-copyback")) {
				t.Fatalf("[%s] node %d: tenant %s left a .qrdb-copyback staging directory in pool %s", proto.name, i+1, tenantID, c.pool)
			}
		}
		if exists(filepath.Join(d.def, tenantID+".copyback")) {
			t.Fatalf("[%s] node %d: tenant %s left a %s.copyback staging directory in the default pool", proto.name, i+1, tenantID, tenantID)
		}
	}
}

// volumeMoveClusterSeed creates a raft tenant (placed on every node of the
// cluster) on pool default, a database in it and one key, and returns the
// tenant and its data-plane connection to each node.
func volumeMoveClusterSeed(t *testing.T, nodes [3]*multiProtoNode, proto multiProtocol, prefix string) (volumeMoveTenant, [3][]string) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", strings.ToLower(proto.name), time.Now().UnixNano())
	mt := volumeMoveTenant{
		tenantID: prefix + "-" + suffix,
		dbName:   "movedata-" + suffix,
		key:      "greeting",
		value:    "before-the-move-" + suffix,
	}
	createTenant(t, nodes[0], proto, mt.tenantID, "Moving Raft Tenant", "raft", true)
	if pool := volumeMoveTenantPool(t, nodes[0], proto, mt.tenantID); pool != "default" {
		t.Fatalf("[%s] raft tenant %s placed on pool %q, want default", proto.name, mt.tenantID, pool)
	}
	certPath, keyPath := provisionTenantIdentity(t, nodes[0], mt.tenantID, "mover", dataPlanePermissions)
	var conns [3][]string
	for i, node := range nodes {
		conns[i] = tenantCLIArgsForProto(node, proto, certPath, keyPath)
	}
	mt.conn = conns[0]
	if out, err := runQRDB(t, nodes[0].cli, mt.conn, "database", "create", mt.dbName, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant database create: %v\noutput: %s", proto.name, err, out)
	}
	volumeMoveClusterPut(t, nodes[0], proto, mt, mt.conn, mt.key, mt.value)
	volumeMoveClusterExpectEverywhere(t, nodes, proto, mt, conns, mt.key, mt.value)
	return mt, conns
}

// volumeMoveClusterStartMove starts the move of tenantID to pool second.
func volumeMoveClusterStartMove(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "volume-pool", "move-tenant", tenantID, "--to", "second")
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
}

// volumeMoveClusterWaitGone polls get-move until the tenant has no move,
// within volumeMoveDeadline.
func volumeMoveClusterWaitGone(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) {
	t.Helper()
	var out string
	var err error
	if pollUntil(volumeMoveDeadline, 200*time.Millisecond, func() bool {
		out, err = runCLIProto(t, node, proto, "--output", "json", "volume-pool", "get-move", tenantID)
		return err != nil && volumeMoveIsNoMove(out)
	}) {
		return
	}
	t.Fatalf("[%s] move of %s did not end within %s; last get-move (err=%v): %s", proto.name, tenantID, volumeMoveDeadline, err, out)
}

// volumeMoveClusterPut writes key through conn on node, retrying until the
// write is acknowledged: mid-move a replica's stores close for its copy step.
func volumeMoveClusterPut(t *testing.T, node *multiProtoNode, proto multiProtocol, mt volumeMoveTenant, conn []string, key, value string) {
	t.Helper()
	var out string
	var err error
	if pollUntil(volumeMoveDeadline, 200*time.Millisecond, func() bool {
		out, err = runQRDB(t, node.cli, conn, "--database", mt.dbName, "kv", "put", key, value)
		return err == nil
	}) {
		return
	}
	t.Fatalf("[%s] tenant %s kv put %s not acknowledged within %s: %v\noutput: %s", proto.name, mt.tenantID, key, volumeMoveDeadline, err, out)
}

// volumeMoveClusterExpectEverywhere reads key back from every node, waiting
// for a lagging replica to catch up within volumeMoveDeadline.
func volumeMoveClusterExpectEverywhere(t *testing.T, nodes [3]*multiProtoNode, proto multiProtocol, mt volumeMoveTenant, conns [3][]string, key, want string) {
	t.Helper()
	for i, node := range nodes {
		var out string
		var err error
		if !pollUntil(volumeMoveDeadline, 200*time.Millisecond, func() bool {
			out, err = runQRDB(t, node.cli, conns[i], "--database", mt.dbName, "kv", "get", key)
			return err == nil && strings.Contains(out, want)
		}) {
			t.Fatalf("[%s] node %d: tenant %s kv get %s = %q (err=%v), want %q", proto.name, i+1, mt.tenantID, key, out, err, want)
		}
	}
}
