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

//go:build !integration

package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestNewClusteredNode_CancelledContextBuildsNoHost: a clustered boot whose
// ctx is already done returns the cancellation as a *HostCreateError and
// builds nothing -- no WAL, no node-host directory, no Raft transport, no
// shard. Without the check the boot opens the host and starts the shard, and
// only the wait for the shard that follows notices ctx, after a host was built
// and torn down: on a loaded runner, seconds of work for a caller that already
// left.
func TestNewClusteredNode_CancelledContextBuildsNoHost(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	r := replicas[0]
	cfg := formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas))
	mtls := &RaftMTLSConfig{CACert: mustReadCAPEM(t, ca), Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := NewClusteredNode(ctx, cfg, members, nil, WithRaftMTLS(mtls))
	if n != nil {
		_ = n.Close()
		t.Fatal("NewClusteredNode returned a node for a cancelled ctx")
	}
	require.ErrorIs(t, err, context.Canceled)
	var hostErr *HostCreateError
	require.ErrorAs(t, err, &hostErr)
	for _, dir := range []string{"nodehost", "wal"} {
		_, statErr := os.Stat(filepath.Join(cfg.Raft.DataDir, dir))
		require.True(t, os.IsNotExist(statErr), "a cancelled boot created %s/ (stat err %v)", dir, statErr)
	}
}

// TestNewStandaloneNode_FailedBootLeavesNoReclaimSweeper: a standalone boot
// that fails after its services are built -- here in wireClientAndServer, on
// an unparseable auth.revocation_cache_ttl -- stops and closes the backend it
// built, and must not leave a reclaim sweeper behind on it. The failed
// constructor returns no Node, so nothing would ever cancel such a sweeper:
// it would log "backend: closed" every DefaultReclaimInterval for the rest of
// the process. Both standalone boot paths are covered: seal disabled, and the
// sealed path's auto-unseal (completeStandaloneBoot).
func TestNewStandaloneNode_FailedBootLeavesNoReclaimSweeper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sealed bool
	}{
		{name: "seal-disabled", sealed: false},
		{name: "auto-unsealed", sealed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sealedStandaloneConfig(t, t.TempDir())
			cfg.Seal.Enabled = tc.sealed
			cfg.Auth.RevocationCacheTTL = "not-a-duration"
			var opts []NodeOption
			if tc.sealed {
				opts = append(opts, WithSealPassphrase([]byte("failed-boot-sweeper")))
			}

			before := standaloneReclaimSweepers()
			n, err := NewStandaloneNode(context.Background(), cfg, nil, opts...)
			if n != nil {
				_ = n.Close()
				t.Fatal("NewStandaloneNode returned a node for an unparseable revocation cache TTL")
			}
			var wiringErr *AuthMiddlewareWiringError
			require.ErrorAs(t, err, &wiringErr)
			requireStandaloneReclaimSweepers(t, before)
		})
	}
}

// TestNode_TeardownDataPlaneStopsReclaimSweeper: the in-process recycle of an
// encryption migration (teardownDataPlane) stops the reclaim sweeper of the
// data plane it closes, as it stops the lease-expiry sweeper. Running on the
// node-lifetime context instead, the old sweeper outlives its backend and
// keeps sweeping it -- "backend: closed" every DefaultReclaimInterval --
// beside the one the rebuild starts, until the node closes.
func TestNode_TeardownDataPlaneStopsReclaimSweeper(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	before := standaloneReclaimSweepers()
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("teardown-sweeper")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.Equal(t, before+1, standaloneReclaimSweepers(), "the booted data plane runs one reclaim sweeper")

	n.teardownDataPlane()
	requireStandaloneReclaimSweepers(t, before)
}

// TestNode_TeardownDataPlaneStopsVolumeReporter pins that the teardown a
// system-scope encryption migration runs stops the volume-pool Reporter
// publishing through the data plane being torn down. Left running, its
// refresh keeps proposing into an engine whose storage teardownDataPlane has
// closed ("lease table write failed: storage is closed"), and the rebuild's
// startVolumeReporter replaces the field without closing it, leaking it for
// the life of the process.
func TestNode_TeardownDataPlaneStopsVolumeReporter(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("teardown-reporter")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.NotNil(t, n.volumeReporter, "a node with a volume pool runs a volume-pool Reporter")

	n.teardownDataPlane()
	require.Nil(t, n.volumeReporter, "teardownDataPlane left the volume-pool Reporter running over the closed data plane")
}

// standaloneReclaimSweeperOrigin is the stack line every goroutine
// startReclaimSweepStandalone starts carries from the moment it exists,
// whether or not it has reached ReclaimSweeper.Run yet.
const standaloneReclaimSweeperOrigin = "created by github.com/jeremyhahn/go-qrdb/sdk/go/node.(*Node).startReclaimSweepStandalone"

// standaloneReclaimSweepers counts the live goroutines started by
// startReclaimSweepStandalone.
func standaloneReclaimSweepers() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), standaloneReclaimSweeperOrigin)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// sweeperExitBound bounds the wait for a stopped sweeper's goroutine to leave
// the scheduler. A stopped sweeper has already returned from Run when its stop
// function returns; this covers only the goroutine's own exit after that, so a
// correct stop meets it at once and the bound matters only to a sweeper that
// was never stopped.
const sweeperExitBound = 5 * time.Second

// requireStandaloneReclaimSweepers waits, yielding rather than sleeping, until
// no more than want standalone reclaim sweepers are live.
func requireStandaloneReclaimSweepers(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(sweeperExitBound)
	for {
		got := standaloneReclaimSweepers()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d standalone reclaim sweepers still running, want %d: one outlived the data plane it sweeps", got, want)
		}
		runtime.Gosched()
	}
}
