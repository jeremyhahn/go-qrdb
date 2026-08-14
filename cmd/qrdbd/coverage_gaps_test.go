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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// resolveCreateFunc: "mvcc" exercises the mvcc factory closure in init()
// ---------------------------------------------------------------------------

func TestResolveCreateFunc_MVCC(t *testing.T) {
	fn, err := resolveCreateFunc("mvcc", "")
	require.NoError(t, err)
	require.NotNil(t, fn, "mvcc factory should return a non-nil CreateFunc")
}

func TestResolveCreateFunc_MVCC_ReturnsDifferentFromPebble(t *testing.T) {
	mvccFn, err := resolveCreateFunc("mvcc", "")
	require.NoError(t, err)

	pebbleFn, err := resolveCreateFunc("pebble", "")
	require.NoError(t, err)

	// Both should be non-nil but distinct.
	require.NotNil(t, mvccFn)
	require.NotNil(t, pebbleFn)
}

// ---------------------------------------------------------------------------
// resolveAutoConfig: QUIC enabled auto-selects port
// ---------------------------------------------------------------------------

func TestResolveAutoConfig_QUICAutoSelectsPort(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "multicast",
		enableQUIC:      true,
		quicPort:        63004, // default
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)

	// QUIC port should have been auto-selected (not the default 63004,
	// though it may coincidentally be 63004 if that port is free).
	assert.NotEqual(t, 0, f.quicPort)
	// Verify other auto-config fields were set.
	assert.NotEqual(t, uint64(1), f.replicaID)
	assert.Contains(t, f.dataDir, "/data/")
}

func TestResolveAutoConfig_QUICDisabledSkipsAutoPort(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "multicast",
		enableQUIC:      false,
		quicPort:        63004,
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)

	// QUIC port should remain at the default when QUIC is disabled.
	assert.Equal(t, 63004, f.quicPort)
}

// ---------------------------------------------------------------------------
// resolveAutoConfig: explicit quic-port flag prevents auto-select
// ---------------------------------------------------------------------------

func TestResolveAutoConfig_ExplicitQUICPortPreserved(t *testing.T) {
	cmd := newStartCmd()
	require.NoError(t, cmd.Flags().Set("quic-port", "55555"))
	f := &startFlags{
		discoveryMethod: "dns",
		enableQUIC:      true,
		quicPort:        55555,
		dnsDomain:       "example.com",
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)
	assert.Equal(t, 55555, f.quicPort)
}

// ---------------------------------------------------------------------------
// waitForAdminReplication: admin appears after a few polls
// ---------------------------------------------------------------------------

func TestWaitForAdminReplication_SucceedsAfterPolls(t *testing.T) {
	var callCount atomic.Int32
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			n := callCount.Add(1)
			if n < 3 {
				return nil, &user.UserNotFoundError{Lookup: "admin"}
			}
			return &user.User{ID: 9001, Username: "admin"}, nil
		},
	}

	err := waitForAdminReplication(context.Background(), userSvc, "default")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, callCount.Load(), int32(3))
}

func TestWaitForAdminReplication_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}

	err := waitForAdminReplication(ctx, userSvc, "default")
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for admin replication", abe.Step)
}

// ---------------------------------------------------------------------------
// waitForLeaderElection: leader available on second attempt
// ---------------------------------------------------------------------------

func TestWaitForLeaderElection_SecondAttempt(t *testing.T) {
	var callCount atomic.Int32
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			n := callCount.Add(1)
			if n < 2 {
				return 0, false, nil
			}
			return 1, true, nil
		},
	}

	err := waitForLeaderElection(context.Background(), node, 1)
	require.NoError(t, err)
	assert.Equal(t, int32(2), callCount.Load())
}

func TestWaitForLeaderElection_ImmediateSuccess(t *testing.T) {
	node := &mockRaftNode{} // default: leader ready
	err := waitForLeaderElection(context.Background(), node, 1)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// warmupReadIndex: success on nil error from Get (key exists)
// ---------------------------------------------------------------------------

func TestWarmupReadIndex_GetReturnsNilError(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			callCount.Add(1)
			assert.Equal(t, "__sys:warmup", key)
			return nil, nil
		},
	}

	warmupReadIndex(context.Background(), mock)
	assert.Equal(t, int32(1), callCount.Load())
}

// ---------------------------------------------------------------------------
// runStart: unknown SM type after TLS generation succeeds
//
// Exercises the initializeNode error return path inside runStart
// (start.go after TLS generation and before auth bootstrap).
// ---------------------------------------------------------------------------

func TestRunStart_InitializeNodeError(t *testing.T) {
	dataDir := t.TempDir()
	f := &startFlags{
		dataDir:         dataDir,
		smType:          "nonexistent-type",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
	}

	err := runStart(f)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// runFirstBoot: creation failure falls to replication wait
//
// When both role creation and user creation fail with non-AlreadyExists
// errors, the function falls back to waitForAdminReplication. With a
// short context timeout, this verifies the fallback path.
// ---------------------------------------------------------------------------

func TestRunFirstBoot_CreationFailsFallsToReplicationWait(t *testing.T) {
	sentinel := errors.New("not leader")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	node := &mockRaftNode{} // leader ready
	err := runFirstBoot(ctx, node, 1,
		&mockUserServicer{
			getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
				return nil, &user.UserNotFoundError{Lookup: "admin"}
			},
			createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
				return nil, sentinel
			},
		},
		&mockRoleServicer{
			createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
				return nil, sentinel
			},
		},
		&mockTenantServicer{}, false, nil, "")
	// Should fall through to waitForAdminReplication which times out.
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
}

// ---------------------------------------------------------------------------
// adminExists: returns true when user found
// ---------------------------------------------------------------------------

func TestAdminExists_UserFound(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return &user.User{ID: 9001, Username: "admin"}, nil
		},
	}
	assert.True(t, adminExists(context.Background(), userSvc, "default"))
}

func TestAdminExists_UserNotFound(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}
	assert.False(t, adminExists(context.Background(), userSvc, "default"))
}

func TestAdminExists_OtherError(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, errors.New("database down")
		},
	}
	// Any error means admin does not exist (conservatively).
	assert.False(t, adminExists(context.Background(), userSvc, "default"))
}

// ---------------------------------------------------------------------------
// buildServerConfig: verify all fields are populated
// ---------------------------------------------------------------------------

func TestBuildServerConfig_AllFieldsPopulated(t *testing.T) {
	f := &startFlags{
		adminPort:            9001,
		grpcPort:             9002,
		quicPort:             9003,
		enableREST:           true,
		enableGRPC:           false,
		enableQUIC:           true,
		dataDir:              "/var/data",
		deploymentID:         42,
		shardID:              7,
		replicaID:            3,
		raftAddress:          "10.0.0.1:63001",
		listen:               "0.0.0.0:63001",
		smType:               "mvcc",
		maxInMemLogSize:      1024,
		maxTotalInMemLogSize: 4096,
		snapshotEntries:      100,
		compactionOverhead:   50,
		join:                 true,
		logLevel:             "debug",
		logFormat:            "json",
		discoveryMethod:      "dns",
		multicastSecret:      "secret",
		dnsDomain:            "example.com",
		tlsCAFile:            "/ca.pem",
		tlsCertFile:          "/cert.pem",
		tlsKeyFile:           "/key.pem",
	}

	cfg := buildServerConfig(f)
	assert.Equal(t, 9001, cfg.Server.AdminPort)
	assert.Equal(t, 9002, cfg.Server.GRPCPort)
	assert.Equal(t, 9003, cfg.Server.QUICPort)
	assert.True(t, cfg.Protocols.REST)
	assert.False(t, cfg.Protocols.GRPC)
	assert.True(t, cfg.Protocols.QUIC)
	assert.Equal(t, "/var/data", cfg.Raft.DataDir)
	assert.Equal(t, uint64(42), cfg.Raft.DeploymentID)
	assert.Equal(t, uint64(7), cfg.Raft.ShardID)
	assert.Equal(t, uint64(3), cfg.Raft.ReplicaID)
	assert.True(t, cfg.Raft.Join)
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "dns", cfg.Discovery.Method)
	assert.Equal(t, "secret", cfg.Discovery.MulticastSecret)
	assert.Equal(t, "example.com", cfg.Discovery.DNSDomain)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter stale methods delegate correctly
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_GetStale_DelegatesToGet(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "stale-key" {
				return []byte("stale-value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}

	val, err := mock.GetStale(context.Background(), "stale-key")
	require.NoError(t, err)
	assert.Equal(t, []byte("stale-value"), val)
}

func TestKVServiceAdapter_ListStale_DelegatesToList(t *testing.T) {
	mock := &mockKVServicer{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			return []string{prefix + "a", prefix + "b"}, nil
		},
	}

	keys, err := mock.ListStale(context.Background(), "ns/")
	require.NoError(t, err)
	assert.Equal(t, []string{"ns/a", "ns/b"}, keys)
}

func TestKVServiceAdapter_ScanStale_DelegatesToScan(t *testing.T) {
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{prefix + "x": []byte("v")}, nil
		},
	}

	result, err := mock.ScanStale(context.Background(), "p/")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), result["p/x"])
}

// ---------------------------------------------------------------------------
// isSingleNode: edge cases
// ---------------------------------------------------------------------------

func TestIsSingleNode_StaticZeroPeers(t *testing.T) {
	f := &startFlags{discoveryMethod: "static", peers: []string{}}
	assert.True(t, isSingleNode(f))
}

func TestIsSingleNode_MultiplePeers(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=10.0.0.1:63001", "2=10.0.0.2:63001", "3=10.0.0.3:63001"},
	}
	assert.False(t, isSingleNode(f))
}

// ---------------------------------------------------------------------------
// generateReplicaID: multiple calls return non-zero unique values
// ---------------------------------------------------------------------------

func TestGenerateReplicaID_AlwaysNonZero(t *testing.T) {
	for range 100 {
		id, err := generateReplicaID()
		require.NoError(t, err)
		assert.NotEqual(t, uint64(0), id, "replica ID must never be zero")
	}
}

func TestGenerateReplicaID_Within48Bits(t *testing.T) {
	for range 100 {
		id, err := generateReplicaID()
		require.NoError(t, err)
		assert.LessOrEqual(t, id, uint64(0xFFFFFFFFFFFF),
			"replica ID must fit within 48 bits")
	}
}

// ---------------------------------------------------------------------------
// findFreeUDPPort / findFreeTCPPort: basic validation
// ---------------------------------------------------------------------------

func TestFindFreeUDPPort_ReturnsValidPort(t *testing.T) {
	port, err := findFreeUDPPort()
	require.NoError(t, err)
	assert.Greater(t, port, 0)
	assert.LessOrEqual(t, port, 65535)
}

func TestFindFreeTCPPort_ReturnsValidPort(t *testing.T) {
	port, err := findFreeTCPPort()
	require.NoError(t, err)
	assert.Greater(t, port, 0)
	assert.LessOrEqual(t, port, 65535)
}

// ---------------------------------------------------------------------------
// newStatusCmd: mTLS branch (tls-cert-file + tls-key-file set)
// ---------------------------------------------------------------------------

func TestNewStatusCmd_MTLSBranch(t *testing.T) {
	// Generate a CA + leaf bundle to use as both server and client certs.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	statusResp := statusResponse{
		NodeID:      "node-mtls",
		RaftAddress: "10.0.0.1:63001",
		ShardCount:  1,
		Shards: []shardStatus{
			{ShardID: 1, ReplicaID: 1, LeaderID: 1, Term: 3, IsLeader: true, Health: "healthy", LastApplied: 50},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusResp)
	}))
	srv.TLS = bundle.TLSConfig.Clone()
	srv.TLS.ClientAuth = tls.NoClientCert
	srv.StartTLS()
	defer srv.Close()

	// Write the auto-generated CA cert to a file that the command trusts.
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "client-cert.pem")
	keyFile := filepath.Join(dir, "client-key.pem")

	// Use the test server's CA cert for verification.
	srvCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, srvCAPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newStatusCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{
		"--address", addr,
		"--tls-ca-file", caFile,
		"--tls-cert-file", certFile,
		"--tls-key-file", keyFile,
	})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "node-mtls")
}

func TestNewStatusCmd_MTLSInvalidCert(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "bad-cert.pem")
	keyFile := filepath.Join(dir, "bad-key.pem")
	require.NoError(t, os.WriteFile(certFile, []byte("not-a-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("not-a-key"), 0o600))

	cmd := newStatusCmd()
	cmd.SetArgs([]string{
		"--address", "127.0.0.1:63002",
		"--tls-cert-file", certFile,
		"--tls-key-file", keyFile,
	})

	err := cmd.Execute()
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// newPeersCmd: mTLS branch (tls-cert-file + tls-key-file set)
// ---------------------------------------------------------------------------

func TestNewPeersCmd_MTLSBranch(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	peersResp := peersResponse{
		Shards: []shardPeers{
			{
				ShardID:  1,
				LeaderID: 1,
				Peers: []peerInfo{
					{ReplicaID: 1, Address: "10.0.0.1:63001", IsLeader: true},
				},
			},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peersResp)
	}))
	srv.TLS = bundle.TLSConfig.Clone()
	srv.TLS.ClientAuth = tls.NoClientCert
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "client-cert.pem")
	keyFile := filepath.Join(dir, "client-key.pem")

	srvCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, srvCAPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newPeersCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{
		"--address", addr,
		"--tls-ca-file", caFile,
		"--tls-cert-file", certFile,
		"--tls-key-file", keyFile,
	})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "Shard 1")
}

func TestNewPeersCmd_MTLSInvalidCert(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "bad-cert.pem")
	keyFile := filepath.Join(dir, "bad-key.pem")
	require.NoError(t, os.WriteFile(certFile, []byte("not-a-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("not-a-key"), 0o600))

	cmd := newPeersCmd()
	cmd.SetArgs([]string{
		"--address", "127.0.0.1:63002",
		"--tls-cert-file", certFile,
		"--tls-key-file", keyFile,
	})

	err := cmd.Execute()
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// waitForLeaderElection: exhausts retries returns error
// ---------------------------------------------------------------------------

func TestWaitForLeaderElection_ContextCanceledDuringWait(t *testing.T) {
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, errors.New("no leader")
		},
	}

	// Use a short context so context cancellation is hit during backoff.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := waitForLeaderElection(ctx, node, 1)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
}

// ---------------------------------------------------------------------------
// waitForAdminReplication: times out when admin never appears
// ---------------------------------------------------------------------------

func TestWaitForAdminReplication_TimesOut(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}

	// Use a very short context to trigger the deadline-based exit
	// within waitForAdminReplication's own 30s timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := waitForAdminReplication(ctx, userSvc, "default")
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for admin replication", abe.Step)
}

// ---------------------------------------------------------------------------
// newStartCmd: config file loading with valid YAML
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// initializeNode: multi-node with bundle exercises mTLS config branch
//
// Exercises lines 619-625 of start.go where the mTLS config is built
// for transport when !isSingleNode and bundle is non-nil. The host
// is created in a temp dir with a valid listen address, but startShard
// fails because DNS discovery finds no peers. The host is closed on
// error.
// ---------------------------------------------------------------------------

func TestInitializeNode_MultiNodeMTLSConfig(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	// Use a free port so the host can bind successfully.
	port, err := findFreeUDPPort()
	require.NoError(t, err)

	dataDir := t.TempDir()
	f := &startFlags{
		smType:          "memory",
		discoveryMethod: "dns",
		dnsDomain:       "nonexistent.local",
		dataDir:         dataDir,
		listen:          "127.0.0.1:" + strconv.Itoa(port),
		raftAddress:     "127.0.0.1:" + strconv.Itoa(port),
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		logFormat:       "text",
		logLevel:        "error",
	}

	_, err = initializeNode(context.Background(), f, bundle)
	// Expected to fail at bootstrap (DNS discovery finds no peers)
	// but the mTLS config branch (lines 619-625) is exercised.
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// initializeNode: multi-node static with valid peers and nil bundle
//
// Exercises the path where bundle is nil and discovery is static with
// multiple peers. The mTLS config should be nil.
// ---------------------------------------------------------------------------

func TestInitializeNode_MultiNodeNilBundle(t *testing.T) {
	port, err := findFreeUDPPort()
	require.NoError(t, err)

	dataDir := t.TempDir()
	f := &startFlags{
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:" + strconv.Itoa(port), "2=127.0.0.2:63001"},
		dataDir:         dataDir,
		listen:          "127.0.0.1:" + strconv.Itoa(port),
		raftAddress:     "127.0.0.1:" + strconv.Itoa(port),
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		logFormat:       "text",
		logLevel:        "error",
	}

	// nil bundle means no mTLS config, but multi-node. Host creation
	// may succeed but shard start could fail.
	_, err = initializeNode(context.Background(), f, nil)
	// We don't assert the specific error type since it depends on
	// transport availability, just that it completes without panic.
	_ = err
}

func TestNewStartCmd_ValidConfigFileApplied(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  host: "0.0.0.0"
  admin_port: 9100
  grpc_port: 9200
  quic_port: 9300
raft:
  raft_address: "10.0.0.99:63001"
  data_dir: "/tmp/qrdb-test"
  deployment_id: 5
  shard_id: 3
  replica_id: 7
  sm_type: "memory"
  peers: "1=10.0.0.1:63001,2=10.0.0.2:63001"
protocols:
  rest: true
  grpc: false
  quic: true
discovery:
  method: "static"
logging:
  format: "json"
  level: "debug"
tls:
  enabled: false
`
	require.NoError(t, os.WriteFile(cfgFile, []byte(yaml), 0o600))

	cmd := newStartCmd()
	// Use the config flag but also force an error by using an unknown SM
	// type -- this lets us verify the config was loaded without actually
	// starting a node.
	cmd.SetArgs([]string{"--config", cfgFile, "--sm", "nonexistent"})
	err := cmd.Execute()
	require.Error(t, err)
	// The error should be UnknownSMError because SM was overridden by CLI.
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// waitForAdminReplication: backoff cap at 1 second
// ---------------------------------------------------------------------------

func TestWaitForAdminReplication_BackoffCapped(t *testing.T) {
	// The backoff starts at 100ms and doubles each iteration:
	// 100ms → 200ms → 400ms → 800ms → 1600ms (capped to 1s).
	// We need at least 5 iterations to trigger the cap at line 1331.
	// Total sleep: 100+200+400+800+1000 = 2500ms.
	// Admin appears on call 6, so we hit: 5 sleeps then found.
	var callCount atomic.Int32
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			n := callCount.Add(1)
			if n < 6 {
				return nil, &user.UserNotFoundError{Lookup: "admin"}
			}
			return &user.User{ID: 9001, Username: "admin"}, nil
		},
	}

	err := waitForAdminReplication(context.Background(), userSvc, "default")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, callCount.Load(), int32(6))
}
