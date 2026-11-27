//go:build integration

package integration_test

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// volumeMoveShutdownGrace bounds how long a SIGTERMed qrdbd may take to
// exit cleanly while a tenant move step is in flight: it waits for the step
// before it closes the stores.
const volumeMoveShutdownGrace = 15 * time.Second

// TestVolumePool_CLI_MoveTenant_ShutdownDuringMove starts a move of a
// volume tenant from "default" to "second" over every CLI transport and
// SIGTERMs qrdbd right after `qrdb volume-pool move-tenant` returns. The
// daemon must exit cleanly within volumeMoveShutdownGrace: it stops the
// move worker and waits for its in-flight step before any store closes.
// After a restart the move finishes (the tenant is on "second"), every
// value written before the move reads back, and a new write lands.
func TestVolumePool_CLI_MoveTenant_ShutdownDuringMove(t *testing.T) {
	t.Parallel()
	owner := t
	node := startVolumeMoveNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			volumeMoveShutdownOverProtocol(owner, t, node, proto)
		})
	}
}

func volumeMoveShutdownOverProtocol(owner, t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	mt := seedVolumeMoveTenant(t, node, proto, "vs", 0)

	if out, err := runCLIProto(t, node, proto, "--output", "json", "volume-pool", "move-tenant", mt.tenantID, "--to", "second"); err != nil {
		t.Fatalf("[%s] volume-pool move-tenant: %v\noutput: %s", proto.name, err, out)
	}
	volumeMoveShutdownTerminate(t, node, proto)
	volumeMoveShutdownRestart(owner, t, node)

	volumeMoveShutdownAwaitPool(t, node, proto, mt.tenantID, "second")
	volumeMoveExpectValue(t, node, proto, mt, mt.key, mt.value)
	afterKey, afterValue := "after", "after-the-restart-"+mt.tenantID
	if out, err := runQRDB(t, node.cli, mt.conn, "--database", mt.dbName, "kv", "put", afterKey, afterValue); err != nil {
		t.Fatalf("[%s] tenant kv put after the restart: %v\noutput: %s", proto.name, err, out)
	}
	volumeMoveExpectValue(t, node, proto, mt, afterKey, afterValue)
}

// volumeMoveShutdownTerminate SIGTERMs node's qrdbd and fails unless it
// exits cleanly within volumeMoveShutdownGrace. Unlike stopNode, a daemon
// that hangs or exits non-zero fails the test.
func volumeMoveShutdownTerminate(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	daemon := spawnedDaemon(t, node.proc)
	if err := node.proc.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("[%s] SIGTERM qrdbd: %v", proto.name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), volumeMoveShutdownGrace)
	defer cancel()
	err := daemon.WaitContext(ctx)
	var waitErr *qrdbdharness.DaemonWaitError
	if errors.As(err, &waitErr) {
		t.Fatalf("[%s] qrdbd did not exit within %s of SIGTERM during a move: %v", proto.name, volumeMoveShutdownGrace, err)
	}
	if err != nil {
		t.Fatalf("[%s] qrdbd exited uncleanly after SIGTERM during a move: %v", proto.name, err)
	}
}

// volumeMoveShutdownRestart relaunches node's exited qrdbd with its start
// arguments and waits the way multiProtoNode.restart does. The daemon is
// started under owner, the test that owns node, so its cleanup does not
// kill it when the subtest t ends and the next transport's subtest finds it
// serving.
func volumeMoveShutdownRestart(owner, t *testing.T, node *multiProtoNode) {
	t.Helper()
	node.proc = startNode(owner, node.bin, node.startArgs, "[volume-move-restart] ")

	waitForBarrierStatusReachable(t, node.ca, node.restPort, 90*time.Second)
	unsealOnlyBarrierWithSecret(t, node.ca, node.restPort, []byte(node.passphrase))
	waitForHealthy(t, node.dataDir, 60*time.Second)
	waitForAdmin(t, node.ca.Pool, node.restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, node.ca, []int{node.restPort}, 60*time.Second)
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, node, proto, 60*time.Second)
	}
}

// volumeMoveShutdownAwaitPool polls `qrdb tenant get` until tenantID is
// placed on want, for up to volumeMoveDeadline.
func volumeMoveShutdownAwaitPool(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID, want string) {
	t.Helper()
	deadline := time.Now().Add(volumeMoveDeadline)
	for {
		pool := volumeMoveTenantPool(t, node, proto, tenantID)
		if pool == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] tenant %s is on pool %q %s after the restart, want %s", proto.name, tenantID, pool, volumeMoveDeadline, want)
		}
	}
}
