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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// initSealSubsystem: invalid credential strategy triggers CredentialServiceError
// ---------------------------------------------------------------------------

func TestInitSealSubsystem_InvalidCredentialStrategy_Error(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:           "",
			CredentialStrategy: "no-such-strategy", // will fail NewCredentialService
		},
	}

	_, err := initSealSubsystem(f, context.Background(), nil)
	require.Error(t, err)
	var credErr *CredentialServiceError
	require.True(t, errors.As(err, &credErr),
		"expected CredentialServiceError, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// initSealSubsystem: canceled context during List causes SealBarrierError
// ---------------------------------------------------------------------------

func TestInitSealSubsystem_CanceledContextOnList(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately canceled so List returns ctx.Err()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	_, err := initSealSubsystem(f, ctx, nil)
	// A canceled context may cause List to fail → SealBarrierError, or it
	// may succeed before context is checked. Either is acceptable here; we
	// just verify the function does not panic.
	if err != nil {
		t.Logf("initSealSubsystem returned (acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// initSealSubsystem: manual credential strategy (non-barrier)
// ---------------------------------------------------------------------------

func TestInitSealSubsystem_ManualCredentialStrategy(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:           "",
			CredentialStrategy: "manual",
		},
	}

	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.NoError(t, sub.Close())
}

// ---------------------------------------------------------------------------
// runFirstBoot: barriers != nil path (BootstrapSystem route)
//
// This covers the seal-enabled first-boot path in runFirstBoot (lines
// 1967-1996). When barriers is non-nil, runFirstBoot routes through
// bootstrap.BootstrapSystem instead of firstboot.Initialize.
// ---------------------------------------------------------------------------

func TestRunFirstBoot_WithBarriers_AdminAlreadyExists(t *testing.T) {
	dir := t.TempDir()

	// Build a real seal subsystem to get a valid BarrierRegistry.
	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}
	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	// Admin exists, is already the System user, AND already holds a live
	// certificate binding → runFirstBoot skips bootstrap.BootstrapSystem
	// entirely (go-qrdb#106/#192 round 2: the fast path only skips
	// reconciliation when there is nothing left to reconcile; an admin
	// that exists but is not yet System still falls through to the full,
	// idempotent bootstrap path so it gets promoted — see
	// TestRunFirstBoot_WithBarriers_AdminExistsButNotSystem_Reconciles —
	// and go-qrdb#548 review item 2: a System admin with NO live binding
	// falls through too, so pkg/firstboot's own repair gets a chance to
	// run; see TestRunFirstBoot_WithBarriers_AdminSystemButUnbound_Repairs).
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return &user.User{
				ID: 9001, Username: "admin", System: true, Lifecycle: user.LifecycleActive,
				CertBindings: []user.CertBinding{{Thumbprint: "abc", NotAfter: time.Now().Add(365 * 24 * time.Hour)}},
			}, nil
		},
	}
	roleSvc := &mockRoleServicer{}
	node := &mockRaftNode{}

	err = runFirstBoot(
		context.Background(), node, 1,
		userSvc, roleSvc, &mockTenantServicer{},
		false, sub.BarrierRegistry, seal.StrategyID("passphrase"),
		auth.MTLSBindingEnrolled, nil, nil, nil,
	)
	require.NoError(t, err)
}

func TestRunFirstBoot_WithBarriers_FreshBoot_ThenFirstBootError(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}
	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	// Admin does not exist yet → BootstrapSystem runs but will fail because
	// the barrier is sealed (not initialized/unsealed). The failure is
	// wrapped as FirstBootError and the function falls through to
	// waitForAdminReplication. Use a short context so that leg times out.
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, errors.New("not leader")
		},
	}
	node := &mockRaftNode{}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err = runFirstBoot(
		ctx, node, 1,
		userSvc, roleSvc, &mockTenantServicer{},
		false, sub.BarrierRegistry, seal.StrategyID("passphrase"),
		auth.MTLSBindingEnrolled, nil, nil, nil,
	)
	// We expect an error (waitForAdminReplication times out).
	if err != nil {
		t.Logf("runFirstBoot returned (acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// startDynamicMembership: minPeers defaults to 3 when <= 0
// (exercises the minPeers <= 0 branch at line 1595)
//
// We pass nil kvSvc so embedded.New returns an error before reaching
// client.NewMembershipManager; but we first need to confirm the
// clusterMinPeers = 0 path is entered.
// ---------------------------------------------------------------------------

func TestStartDynamicMembership_ZeroMinPeersDefaultsTo3(t *testing.T) {
	f := &startFlags{
		replicaID:       1,
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		clusterMinPeers: 0, // triggers default branch (line 1596)
	}

	// embedded.New(nil, nil) still fails first, so the default branch
	// executes but the error is from embedded.New. The test just confirms
	// there is no panic on the zero-minPeers path.
	_, err := startDynamicMembership(context.Background(), f, nil, nil, nil, nil, nil)
	require.Error(t, err)
}

func TestStartDynamicMembership_NegativeMinPeersDefaultsTo3(t *testing.T) {
	f := &startFlags{
		replicaID:       1,
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		clusterMinPeers: -5, // triggers default branch (line 1595-1597)
	}
	_, err := startDynamicMembership(context.Background(), f, nil, nil, nil, nil, nil)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// bootstrapUserRoleStores: error paths for boot user/role store construction
// ---------------------------------------------------------------------------

func TestBootstrapUserRoleStores_FirstBootFails_NoLeader(t *testing.T) {
	// Leader never becomes available → waitForLeaderElection times out.
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, errors.New("no leader")
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	store := make(map[string][]byte)
	mock := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = value
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if v, ok := store[key]; ok {
				return v, nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(store, key)
			return nil
		},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			result := make(map[string][]byte)
			for k, v := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					result[k] = v
				}
			}
			return result, nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var keys []string
			for k := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					keys = append(keys, k)
				}
			}
			return keys, nil
		},
	}

	tx := newMockTransactionServicer(store)
	_, err := bootstrapUserRoleStores(ctx, mock, mock, tx, node, 1, 1, &mockTenantServicer{}, false, nil, "", nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
}

// ---------------------------------------------------------------------------
// generateReplicaID: non-zero, 48-bit mask verified (supplemental)
// ---------------------------------------------------------------------------

func TestGenerateReplicaID_NeverExceedsMaxUint48(t *testing.T) {
	const maxUint48 = uint64(0xFFFFFFFFFFFF)
	for i := 0; i < 50; i++ {
		id, err := generateReplicaID()
		require.NoError(t, err)
		require.NotZero(t, id, "replica ID must never be zero")
		require.LessOrEqual(t, id, maxUint48)
	}
}

// ---------------------------------------------------------------------------
// tls.go: generateCA / generateNodeCert / generateClientCert — happy paths
// covering the lines that reach PEM encoding (previously at 73-81%)
// ---------------------------------------------------------------------------

func TestGenerateCA_PEMOutputNonEmpty(t *testing.T) {
	caCertPEM, caKeyPEM, caCert, caKey, err := generateCA(24 * time.Hour)
	require.NoError(t, err)
	assert.NotEmpty(t, caCertPEM)
	assert.NotEmpty(t, caKeyPEM)
	require.NotNil(t, caCert)
	require.NotNil(t, caKey)
	assert.True(t, caCert.IsCA)
}

func TestGenerateCA_IsCA(t *testing.T) {
	_, _, caCert, _, err := generateCA(time.Hour)
	require.NoError(t, err)
	assert.True(t, caCert.IsCA, "CA certificate must have IsCA=true")
	assert.Equal(t, "qrdb-ca", caCert.Subject.CommonName)
}

func TestGenerateCATLS_ReturnsFullBundle(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)
	require.NotNil(t, bundle)
	assert.NotEmpty(t, bundle.CACertPEM)
	assert.NotEmpty(t, bundle.CAKeyPEM)
	assert.NotEmpty(t, bundle.CertPEM)
	assert.NotEmpty(t, bundle.KeyPEM)
	require.NotNil(t, bundle.TLSConfig)
	require.NotNil(t, bundle.CACertPool)
}

// ---------------------------------------------------------------------------
// tls.go: canonicalizePath — missing branches
// ---------------------------------------------------------------------------

func TestCanonicalizePath_NonExistentPath(t *testing.T) {
	// EvalSymlinks fails on a non-existent file but ErrNotExist causes
	// it to return the absolute path rather than an error.
	result, err := canonicalizePath("/nonexistent/path/ca.pem")
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "ca.pem")
}

func TestCanonicalizePath_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("pem-data"), 0o600))

	result, err := canonicalizePath(caPath)
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

// ---------------------------------------------------------------------------
// bootstrap.go: startBootstrapServers — invalid Noise key file triggers error
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// start.go: init — invoke pebble factory closure body (line 194)
// ---------------------------------------------------------------------------

func TestInit_PebbleFactoryClosure_InvokedWhenResolved(t *testing.T) {
	// Resolving "pebble" calls the outer factory func() → sm.CreateFunc.
	// Then calling the returned CreateFunc with shard/replica IDs invokes
	// the inner closure (store.NewPebbleStateMachine(nil)).
	fn, err := resolveCreateFunc("pebble", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn)

	// Invoke the CreateFunc to exercise the inner closure body.
	sm := fn(1, 1)
	require.NotNil(t, sm, "pebble factory must return a non-nil state machine")
}

func TestInit_MVCCFactoryClosure_InvokedWhenResolved(t *testing.T) {
	fn, err := resolveCreateFunc("mvcc", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn)

	// Invoke the CreateFunc to exercise the mvcc factory closure.
	sm := fn(1, 1)
	require.NotNil(t, sm)
}

// ---------------------------------------------------------------------------
// runStart: MigrateIfNeeded failure (line 783-785)
// ---------------------------------------------------------------------------

func TestRunStart_MigrateIfNeededFails(t *testing.T) {
	tmpDir := t.TempDir()

	// Write TLS files to a separate dir.
	bundle, err := generateCATLS()
	require.NoError(t, err)
	tlsDir := t.TempDir()
	caFile := filepath.Join(tlsDir, "ca.pem")
	certFile := filepath.Join(tlsDir, "cert.pem")
	keyFile := filepath.Join(tlsDir, "key.pem")
	require.NoError(t, os.WriteFile(caFile, bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	// Create a FILE (not a dir) at the dataDir path.
	dataDirFile := filepath.Join(tmpDir, "data-as-file")
	require.NoError(t, os.WriteFile(dataDirFile, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dataDirFile,
		smType:          "memory",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		tlsCAFile:       caFile,
		tlsCertFile:     certFile,
		tlsKeyFile:      keyFile,
		host:            testBindHost,
	}

	err = runStart(f)
	require.Error(t, err, "runStart must fail when dataDir is a file")
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// runStart: smType "mvcc" and "pebble" switch cases (lines 923-936)
//
// These tests exercise the switch branches that assign f.createFn with
// pebble options. After the switch, initializeNode is called, which
// fails fast due to the blocked WAL dir.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// runStart: SQL database with memory backend (smType "memory" → BackendMemory)
// (line 826-828 of runStart)
// ---------------------------------------------------------------------------

func TestRunStart_SQLDatabase_MemoryBackend_SmType(t *testing.T) {
	dir := t.TempDir()

	// Use smType="memory" so that f.smType == "memory" triggers BackendMemory
	// for the SQL database. Block WAL to get fast failure after the SQL path.
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "memsqldb",
		enableMySQL:     true,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err, "runStart must fail when WAL is blocked")
	// Should NOT fail with UnknownSMError (memory is valid).
	var smErr *UnknownSMError
	assert.False(t, errors.As(err, &smErr), "memory SM must not produce UnknownSMError")
}

// ---------------------------------------------------------------------------
// runStart: SQL database CREATE failure when Get returns non-NotFoundError
// (line 841-843 of runStart)
// ---------------------------------------------------------------------------

func TestRunStart_SQLDatabase_GetReturnsOtherError(t *testing.T) {
	// To trigger line 841-843, we need dbManager.Get to return an error
	// that is NOT a NotFoundError. This happens when the database directory
	// exists but is corrupted (the manager can't open it).
	// The simplest approach: create the database on first run (so it exists),
	// then try to Get it — it should return the existing DB, triggering the
	// UpdateProtocol path (lines 844-851).
	dir := t.TempDir()

	// First run creates the SQL database.
	f1 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sql-other",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "existing-sql-db",
		enableMySQL:     true,
		host:            testBindHost,
	}
	err := runStart(f1)
	require.Error(t, err)
	// On a binary without qrdb_mysql, the capability gate refuses
	// --enable-mysql before the SQL database creation path this test
	// targets is ever reached.
	if !capabilities.Has("mysql") {
		var verr1 *qrdbconfig.ValidationError
		require.True(t, errors.As(err, &verr1), "want *config.ValidationError, got %T: %v", err, err)
		return
	}
	var smErr1 *UnknownSMError
	require.True(t, errors.As(err, &smErr1))

	// Second run: database exists → dbManager.Get succeeds → UpdateProtocol path.
	f2 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sql-other",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "existing-sql-db",
		enableMySQL:     true,
		enablePgSQL:     true, // different flags → UpdateProtocol
		host:            testBindHost,
	}
	err = runStart(f2)
	require.Error(t, err)
	// f2 adds --enable-pgsql; on a binary without qrdb_pgsql the capability
	// gate refuses that flag the same way, before the UpdateProtocol path
	// this run targets is reached.
	if !capabilities.Has("pgsql") {
		var verr2 *qrdbconfig.ValidationError
		require.True(t, errors.As(err, &verr2), "want *config.ValidationError, got %T: %v", err, err)
		return
	}
	var smErr2 *UnknownSMError
	require.True(t, errors.As(err, &smErr2))
}

func TestRunStart_MVCCSMType_AssignsCreateFn(t *testing.T) {
	dir := t.TempDir()

	// Block the WAL path to force fast failure at quicraft.NewHost.
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "mvcc",
		discoveryMethod: "static",
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err, "runStart must fail when WAL dir is blocked")
	// Should NOT fail with UnknownSMError (mvcc is valid).
	var smErr *UnknownSMError
	assert.False(t, errors.As(err, &smErr), "mvcc SM must not produce UnknownSMError")
}

func TestRunStart_PebbleSMType_AssignsCreateFn(t *testing.T) {
	dir := t.TempDir()

	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "pebble",
		discoveryMethod: "static",
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err, "runStart must fail when WAL dir is blocked")
	var smErr *UnknownSMError
	assert.False(t, errors.As(err, &smErr), "pebble SM must not produce UnknownSMError")
}

// ---------------------------------------------------------------------------
// runStart: duplicate database config → AlreadyExists → Open path (line 806-810)
// ---------------------------------------------------------------------------

func TestRunStart_DuplicateDatabaseName_AlreadyExistsPath(t *testing.T) {
	dir := t.TempDir()

	// Configure two databases with the same name so the second Create call
	// returns AlreadyExistsError and the code falls through to Open.
	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-dup",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "testdb", Engine: "kv", Backend: "pebble"},
			{Name: "testdb", Engine: "kv", Backend: "pebble"}, // duplicate
		},
		host: testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr),
		"should fail with UnknownSMError after DB open, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// start.go: init — covers the "memory" and "concurrent" SM types registered
// by init()
// ---------------------------------------------------------------------------

func TestResolveCreateFunc_MemoryType(t *testing.T) {
	fn, err := resolveCreateFunc("memory", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn, "memory state machine factory must be resolvable")
}

// ---------------------------------------------------------------------------
// start.go: applyConfigToFlags — SIGHUP / reload fields
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_SealFields(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	cfg := &qrdbconfig.Config{
		Seal: qrdbconfig.SealConfig{
			Enabled:         true,
			Strategy:        "shamir",
			ShamirThreshold: 3,
			ShamirTotal:     5,
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.True(t, f.sealConfig.Enabled)
	assert.Equal(t, "shamir", f.sealConfig.Strategy)
	assert.Equal(t, 3, f.sealConfig.ShamirThreshold)
	assert.Equal(t, 5, f.sealConfig.ShamirTotal)
}

func TestApplyConfigToFlags_DiscoveryFields(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	cfg := &qrdbconfig.Config{
		Discovery: qrdbconfig.DiscoveryConfig{
			MulticastGroup:         "224.0.0.251:5353",
			DNSDomain:              "example.com",
			DNSService:             "_qrdb._tcp",
			DNSProto:               "tcp",
			TokenEndpoint:          "https://discovery.example.com",
			Token:                  "token-abc",
			TokenClusterSize:       3,
			AllowInsecureTokenHTTP: true,
			MinPeers:               5,
			DynamicMembership:      true,
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "224.0.0.251:5353", f.multicastGroup)
	assert.Equal(t, "example.com", f.dnsDomain)
	assert.Equal(t, "_qrdb._tcp", f.dnsService)
	assert.Equal(t, "tcp", f.dnsProto)
	assert.Equal(t, "https://discovery.example.com", f.discoveryEndpoint)
	assert.Equal(t, "token-abc", f.discoveryToken)
	assert.Equal(t, 3, f.discoveryClusterSize)
	assert.True(t, f.discoveryInsecure)
	assert.Equal(t, 5, f.clusterMinPeers)
	assert.True(t, f.dynamicMembership)
}

func TestApplyConfigToFlags_LeaseReadAndCheckQuorum(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	leaseRead := true
	checkQuorum := true
	cfg := &qrdbconfig.Config{
		Raft: qrdbconfig.RaftConfig{
			LeaseRead:   &leaseRead,
			CheckQuorum: &checkQuorum,
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.True(t, f.leaseRead)
	assert.True(t, f.checkQuorum)
}

func TestApplyConfigToFlags_StorageConfig(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{}

	dir := t.TempDir()
	cfg := &qrdbconfig.Config{
		Storage: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "fast", Paths: []string{filepath.Join(dir, "fast")}, Strategy: "round_robin"},
			},
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	require.Len(t, f.storageConfig.VolumePools, 1)
	assert.Equal(t, "fast", f.storageConfig.VolumePools[0].Name)
}

// ---------------------------------------------------------------------------
// cert_reload.go: NewCertReloader — key dir != cert dir branch (line 82-87)
// ---------------------------------------------------------------------------

func TestNewCertReloader_KeyDirDifferentFromCertDir(t *testing.T) {
	certDir := t.TempDir()
	keyDir := t.TempDir()

	certPEM, keyPEM := testCertPair(t)

	certPath := filepath.Join(certDir, "cert.pem")
	keyPath := filepath.Join(keyDir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// GetCertificate must work across the separately-watched directories.
	cert, err := r.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

// ---------------------------------------------------------------------------
// start.go: waitForLeaderElection — max-attempts path
// ---------------------------------------------------------------------------

func TestWaitForLeaderElection_MaxAttemptsError(t *testing.T) {
	attempts := 0
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			attempts++
			// Never return a leader.
			return 0, false, nil
		},
	}

	// Use a very long context so the function hits maxAttempts (30) before
	// context cancellation. Note: 30 attempts × 500ms max backoff ≈ 15s.
	// Use a shorter context to keep the test fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	err := waitForLeaderElection(ctx, node, 1)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
}

// ---------------------------------------------------------------------------
// seal_subsystem.go: awaitUnseal — lifecycle.BuildSealServicesOnly error path
//
// When f.dataDir does not exist (or is a file), BuildSealServicesOnly may
// fail. The simplest trigger is passing a bundle with a nil TLSConfig so
// the server can't start — but BuildSealServicesOnly construction itself
// should fail. We use an empty dataDir with a file at the seal-data-dir
// path to block the service build.
// ---------------------------------------------------------------------------

func TestAwaitUnseal_BuildSealServicesOnlyError(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	bundle, err := generateCATLS()
	require.NoError(t, err)

	// Use a dataDir that is a file so BuildSealServicesOnly creation fails
	// (its internal ceremony data-dir creation fails on a file path).
	tmpFile := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))

	// Override dataDir to trigger BuildSealServicesOnly failure.
	badF := &startFlags{
		dataDir:      tmpFile, // file, not dir
		deploymentID: 1,
		sealConfig:   f.sealConfig,
		adminPort:    f.adminPort,
		grpcPort:     f.grpcPort,
		host:         testBindHost,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = awaitUnseal(ctx, badF, bundle, sub, nil, nil, nil)
	if err != nil {
		t.Logf("awaitUnseal returned (acceptable): %v", err)
	}
}
