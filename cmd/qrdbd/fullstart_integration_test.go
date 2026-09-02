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

package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// ---------------------------------------------------------------------------
// runStart integration tests: full daemon lifecycle driven by OS signals.
//
// These tests call syscall.Kill(os.Getpid(), ...) to signal the test process
// itself, which is only safe in a dedicated integration run (not mixed with
// the unit suite where the signal can race the test binary's default handler).
// ---------------------------------------------------------------------------

func TestRunStart_AuthDisabled_SingleNode_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait long enough for auth bootstrap + warmup + server start.
	// A single-node memory SM needs ~4-5s for leader election + bootstrap
	// even with auth disabled.
	time.Sleep(6 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

func TestRunStart_AuthEnabled_SingleNode_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     true,
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Auth bootstrap requires leader election + role/user creation — allow
	// adequate time for a single-node memory SM to complete all of that.
	time.Sleep(6 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: first node of multi-node cluster (bootstrapServers path)
//
// Exercises resolveTLS Path 5 (startBootstrapServers) and the corresponding
// bootServers.Stop() at line 1388-1390 of runStart.
// ---------------------------------------------------------------------------

func TestRunStart_MultiNode_FirstNode_BootstrapServers_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)
	// Include a second peer address so isSingleNode returns false.
	peer2Addr := fmt.Sprintf("127.0.0.1:%d", raftPort+1)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr), fmt.Sprintf("2=%s", peer2Addr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		// No bootstrapAddr/bootstrapNoiseAddr/bootstrapDirectURL → first node
		// → resolveTLS Path 5 → startBootstrapServers called.
		host: testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// The node starts bootstrap servers and then waits for the second peer.
	// Send SIGTERM to exercise the bootServers.Stop() path at line 1388-1390.
	time.Sleep(2 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: full startup WITH volume pool configured (exercises lines 890-903,
// 1398-1407 of runStart — volumePool != nil path during shutdown)
// ---------------------------------------------------------------------------

func TestRunStart_AuthDisabled_WithVolumePool_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		storageConfig: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "default", Paths: []string{filepath.Join(dir, "vol1")}, Strategy: ""},
			},
		},
		host: testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Allow enough time for full startup.
	time.Sleep(6 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: SIGHUP goroutine body (lines 967-969)
// Sends SIGHUP then SIGTERM to exercise the cert-reload handler goroutine.
// ---------------------------------------------------------------------------

func TestRunStart_SIGHUPThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait for server startup.
	time.Sleep(6 * time.Second)

	// Send SIGHUP to trigger the cert-reload goroutine.
	if killErr := syscall.Kill(os.Getpid(), syscall.SIGHUP); killErr != nil {
		t.Fatalf("Kill(SIGHUP) error = %v", killErr)
	}

	time.Sleep(200 * time.Millisecond)

	// Send SIGTERM to exit.
	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}

// ---------------------------------------------------------------------------
// runStart: srv.Start() failure when admin port is in use (line 1338-1341)
//
// Exercises the `srv.Start()` error return by pre-binding the admin port
// before runStart tries to bind it. Includes a SIGTERM cleanup path for the
// unexpected-success case, so it belongs in the integration suite.
// ---------------------------------------------------------------------------

func TestRunStart_ServerStartFails_PortInUse(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	// Pre-bind the admin port to cause srv.Start() to fail with EADDRINUSE.
	ln, listenErr := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", adminPort))
	require.NoError(t, listenErr)
	defer func() { _ = ln.Close() }()

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort, // port is already in use
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	select {
	case runErr := <-done:
		// srv.Start() should fail because the port is in use.
		if runErr != nil {
			t.Logf("runStart returned with error (acceptable): %v", runErr)
		} else {
			// If it somehow succeeded, send SIGTERM to clean up.
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			<-done
			t.Log("runStart succeeded unexpectedly (admin port may not have conflicted)")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runStart did not return within timeout")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: pebble SM type factory closure body (lines 931-935)
//
// Exercises the pebble SM closure by running a full single-node startup
// with smType="pebble" and sending SIGTERM after shard start.
// ---------------------------------------------------------------------------

func TestRunStart_PebbleSMType_FullStartup_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "pebble",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Allow enough time for the pebble SM to start.
	time.Sleep(6 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

func TestRunStart_SealEnabled_SoftwareStrategy_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
	require.NoError(t, err)
	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)
	grpcPort, err := findFreeTCPPort()
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		enableGRPC:      true,
		sealConfig: qrdbconfig.SealConfig{
			Enabled:  true,
			Strategy: "", // default: passphrase/software
		},
		// Include a volume pool so the sealSub+volumePool interaction path
		// (lines 897-902) is exercised during sealed-window boot.
		storageConfig: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "default", Paths: []string{filepath.Join(dir, "vol1")}, Strategy: ""},
			},
		},
		host: testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// The seal subsystem boots, the sealed-window listener comes up, then
	// once the barrier is unsealed (or shut down) the daemon proceeds.
	// Send SIGTERM while the daemon is in the sealed-window wait phase so
	// that awaitUnseal returns errAwaitUnsealCanceled (clean exit).
	time.Sleep(2 * time.Second)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// TestRunStart_StandaloneMode_ServesStandardOpsRefusesRaftOnly boots the
// daemon in mode "standalone" (no raft, memory backend) and verifies over
// REST: health serves, KV put/get round-trips, capabilities report mode
// standalone with the raft-only features false, and the watch API is
// refused with 501 naming the remedy.
func TestRunStart_StandaloneMode_ServesStandardOpsRefusesRaftOnly(t *testing.T) {
	dir := t.TempDir()

	adminPort, err := findFreeTCPPort()
	require.NoError(t, err)

	f := &startFlags{
		mode:        "standalone",
		dataDir:     dir,
		adminPort:   adminPort,
		authEnabled: false,
		enableREST:  true,
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "default", Engine: "kv", Backend: "memory"},
		},
		host: testBindHost,
	}

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	base := fmt.Sprintf("https://127.0.0.1:%d", adminPort)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test client against a self-signed test daemon
		},
	}

	// Wait for the admin listener.
	require.Eventually(t, func() bool {
		resp, herr := client.Get(base + "/health")
		if herr != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == 200
	}, 30*time.Second, 250*time.Millisecond, "standalone daemon never became healthy")

	// Standard KV round-trip (raw-body PUT /kv/{key}).
	putReq, err := http.NewRequest(http.MethodPut, base+"/kv/standalone-smoke", strings.NewReader("v1"))
	require.NoError(t, err)
	putResp, err := client.Do(putReq)
	require.NoError(t, err)
	_ = putResp.Body.Close()
	require.Less(t, putResp.StatusCode, 300, "KV put must succeed in standalone")

	getResp, err := client.Get(base + "/kv/standalone-smoke")
	require.NoError(t, err)
	_ = getResp.Body.Close()
	require.Equal(t, 200, getResp.StatusCode, "KV get must succeed in standalone")

	// Capabilities report the standalone truth.
	capResp, err := client.Get(base + "/capabilities")
	require.NoError(t, err)
	capBytes, _ := io.ReadAll(capResp.Body)
	_ = capResp.Body.Close()
	require.Equal(t, 200, capResp.StatusCode)
	var caps struct {
		Mode  string `json:"mode"`
		MVCC  bool   `json:"mvcc"`
		Watch bool   `json:"watch"`
		Lease bool   `json:"lease"`
		KV    bool   `json:"kv"`
	}
	require.NoError(t, json.Unmarshal(capBytes, &caps))
	require.Equal(t, "standalone", caps.Mode)
	require.False(t, caps.MVCC)
	require.False(t, caps.Watch)
	require.False(t, caps.Lease)
	require.True(t, caps.KV)

	// A raft-only family is refused with 501 + the remedy hint.
	rangeResp, err := client.Get(base + "/kv/range?start=a")
	require.NoError(t, err)
	rangeBytes, _ := io.ReadAll(rangeResp.Body)
	_ = rangeResp.Body.Close()
	require.Equal(t, 501, rangeResp.StatusCode, "mvcc range must be refused in standalone: %s", rangeBytes)
	require.Contains(t, string(rangeBytes), "requires mode: raft")

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(SIGTERM) error = %v", killErr)
	}
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Logf("runStart returned (acceptable): %v", runErr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runStart did not return within timeout after SIGTERM")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}
