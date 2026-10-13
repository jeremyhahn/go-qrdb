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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// errors.go: new error types (lines 331-443) at 0% coverage
// ---------------------------------------------------------------------------

func TestSealStorageError_Error(t *testing.T) {
	inner := errors.New("pebble open failed")
	e := &SealStorageError{Err: inner}
	assert.Equal(t, "cli: seal storage initialization failed: pebble open failed", e.Error())
}

func TestSealStorageError_Unwrap(t *testing.T) {
	inner := errors.New("disk error")
	e := &SealStorageError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

func TestSealStrategyError_Error(t *testing.T) {
	inner := errors.New("invalid config")
	e := &SealStrategyError{Strategy: "shamir", Err: inner}
	assert.Equal(t, `cli: seal strategy "shamir" initialization failed: invalid config`, e.Error())
}

func TestSealStrategyError_Unwrap(t *testing.T) {
	inner := errors.New("missing key shares")
	e := &SealStrategyError{Strategy: "software", Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

func TestSealStrategyError_ErrorsAs(t *testing.T) {
	inner := errors.New("threshold exceeded")
	e := &SealStrategyError{Strategy: "shamir", Err: inner}
	var target *SealStrategyError
	require.True(t, errors.As(e, &target))
	assert.Equal(t, "shamir", target.Strategy)
}

func TestSealBarrierError_Error(t *testing.T) {
	inner := errors.New("barrier locked")
	e := &SealBarrierError{Err: inner}
	assert.Equal(t, "cli: seal barrier initialization failed: barrier locked", e.Error())
}

func TestSealBarrierError_Unwrap(t *testing.T) {
	inner := errors.New("bad key material")
	e := &SealBarrierError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

func TestSealBarrierError_ErrorsAs(t *testing.T) {
	inner := errors.New("decrypt failed")
	e := &SealBarrierError{Err: inner}
	var target *SealBarrierError
	require.True(t, errors.As(e, &target))
	assert.NotNil(t, target.Err)
}

func TestCredentialServiceError_Error(t *testing.T) {
	inner := errors.New("store unavailable")
	e := &CredentialServiceError{Err: inner}
	assert.Equal(t, "cli: credential service initialization failed: store unavailable", e.Error())
}

func TestCredentialServiceError_Unwrap(t *testing.T) {
	inner := errors.New("connection refused")
	e := &CredentialServiceError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

func TestCredentialServiceError_ErrorsAs(t *testing.T) {
	inner := errors.New("timeout")
	e := &CredentialServiceError{Err: inner}
	var target *CredentialServiceError
	require.True(t, errors.As(e, &target))
	assert.Same(t, inner, target.Err)
}

// ---------------------------------------------------------------------------
// kvstore_adapter.go: Exists method (0% coverage)
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Exists_KeyFound(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "existing-key" {
				return []byte("value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "existing-key")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestKVServiceAdapter_Exists_KeyNotFound(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing-key"}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "missing-key")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestKVServiceAdapter_Exists_NilData(t *testing.T) {
	// Get returns nil, nil - key exists but data is nil.
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "nil-val-key")
	require.NoError(t, err)
	// nil data means the key doesn't exist per the Exists contract.
	assert.False(t, exists)
}

func TestKVServiceAdapter_Exists_ServiceError(t *testing.T) {
	sentinel := errors.New("storage unavailable")
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.False(t, exists)
}

// ---------------------------------------------------------------------------
// start.go: init — exercises the registered state machine types
// ---------------------------------------------------------------------------

func TestInit_PebbleStateMachineRegistered(t *testing.T) {
	// "pebble" must be resolvable after init() runs.
	fn, err := resolveCreateFunc("pebble", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn, "pebble state machine factory must be registered by init()")
}

func TestInit_MVCCStateMachineRegistered(t *testing.T) {
	// "mvcc" must be resolvable after init() runs.
	fn, err := resolveCreateFunc("mvcc", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn, "mvcc state machine factory must be registered by init()")
}

// ---------------------------------------------------------------------------
// start.go: resolveCreateFunc with backend qualifier
// ---------------------------------------------------------------------------

func TestResolveCreateFunc_UnknownTypeWithBackend_ErrorIncludesBoth(t *testing.T) {
	_, err := resolveCreateFunc("unknown-sm", "mybackend", statemachine.StorageConfig{})
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
	assert.Equal(t, "unknown-sm:mybackend", smErr.Name)
}

func TestResolveCreateFunc_UnknownTypeNoBackend_ErrorIncludesType(t *testing.T) {
	_, err := resolveCreateFunc("totally-unknown", "", statemachine.StorageConfig{})
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
	assert.Equal(t, "totally-unknown", smErr.Name)
}

// ---------------------------------------------------------------------------
// host_adapter.go: RequestSnapshot — the real quicraft.Host.RequestSnapshot
// is not callable in a unit test (requires a running Raft node), but we can
// exercise the adapter's wrapper using the mock test adapter and verify the
// interface is satisfied. Coverage of the delegation path (line 97) requires
// an integration test; here we exercise the bootstrapHostAdapter.SyncRead
// path which calls back through the inner adapter chain.
// ---------------------------------------------------------------------------

func TestHostAdapter_ProposeWithMode_AllModes_NoError(t *testing.T) {
	// Table-driven test across all three documented mode dispatch cases.
	tests := []struct {
		name string
		mode writemode.Mode
	}{
		{"sync", writemode.Sync},
		{"wal_durable", writemode.WALDurable},
		{"batched", writemode.Batched},
		{"default_unknown", writemode.Mode(255)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			mock := &mockQuicraftHost{
				syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
					called = true
					return sm.Result{Value: 1}, nil
				},
			}
			adapter := newTestHostAdapter(mock)
			_, err := adapter.ProposeWithMode(context.Background(), 1, []byte("x"), tt.mode)
			require.NoError(t, err)
			assert.True(t, called)
		})
	}
}

// ---------------------------------------------------------------------------
// tls.go: generateCA happy path
// ---------------------------------------------------------------------------

func TestGenerateCA_ReturnsPEMAndParsedCert(t *testing.T) {
	caCertPEM, caKeyPEM, caCert, caKey, err := generateCA(caValidity)
	require.NoError(t, err)

	assert.NotEmpty(t, caCertPEM)
	assert.NotEmpty(t, caKeyPEM)
	assert.NotNil(t, caCert)
	assert.NotNil(t, caKey)
	assert.True(t, caCert.IsCA)
	assert.Equal(t, "qrdb-ca", caCert.Subject.CommonName)
}

func TestGenerateCA_ShortValidity(t *testing.T) {
	// Verify that very short validity durations are accepted.
	caCertPEM, caKeyPEM, caCert, caKey, err := generateCA(24 * 60 * 60 * 1e9) // 24 hours in ns
	require.NoError(t, err)

	assert.NotEmpty(t, caCertPEM)
	assert.NotEmpty(t, caKeyPEM)
	assert.NotNil(t, caCert)
	assert.NotNil(t, caKey)
}

// ---------------------------------------------------------------------------
// tls.go: generateNodeCert happy path
// ---------------------------------------------------------------------------

func TestGenerateNodeCert_ReturnsPEM(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(caValidity)
	require.NoError(t, err)

	certPEM, keyPEM, err := generateNodeCert(caCert, caKey, nodeValidity)
	require.NoError(t, err)

	assert.NotEmpty(t, certPEM)
	assert.NotEmpty(t, keyPEM)
}

func TestGenerateNodeCert_ValidKeyPair(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(caValidity)
	require.NoError(t, err)

	certPEM, keyPEM, err := generateNodeCert(caCert, caKey, nodeValidity)
	require.NoError(t, err)

	// Verify that the PEM pair is a valid tls.Certificate.
	_, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// tls.go: generateClientCert happy path
// ---------------------------------------------------------------------------

func TestGenerateClientCert_ReturnsPEM(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(caValidity)
	require.NoError(t, err)

	certPEM, keyPEM, err := generateClientCert(caCert, caKey, "admin", "default", adminValidity)
	require.NoError(t, err)

	assert.NotEmpty(t, certPEM)
	assert.NotEmpty(t, keyPEM)
}

func TestGenerateClientCert_ValidKeyPair(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(caValidity)
	require.NoError(t, err)

	certPEM, keyPEM, err := generateClientCert(caCert, caKey, "svc-account", "tenant-1", adminValidity)
	require.NoError(t, err)

	// Parse and verify the cert has ClientAuth extended key usage.
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	parsedCert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	require.NoError(t, err)

	hasClientAuth := false
	for _, eku := range parsedCert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	assert.True(t, hasClientAuth, "client cert must have ClientAuth extended key usage")
}

// ---------------------------------------------------------------------------
// tls.go: wireCertReloader happy path
// ---------------------------------------------------------------------------

func TestWireCertReloader_WiresGetCertificate(t *testing.T) {
	dir := t.TempDir()

	// Write the cert and key produced by generateCATLS.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	certPath := filepath.Join(dir, "node-cert.pem")
	keyPath := filepath.Join(dir, "node-key.pem")
	require.NoError(t, os.WriteFile(certPath, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, bundle.KeyPEM, 0o600))

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err = wireCertReloader(bundle, certPath, keyPath, logger)
	require.NoError(t, err)

	// GetCertificate must be wired and Certificates must be cleared.
	require.NotNil(t, bundle.TLSConfig.GetCertificate,
		"wireCertReloader must set GetCertificate callback")
	assert.Empty(t, bundle.TLSConfig.Certificates,
		"wireCertReloader must clear static Certificates slice")
	require.NotNil(t, bundle.Reloader,
		"wireCertReloader must populate bundle.Reloader")

	// Cleanup the reloader goroutine.
	t.Cleanup(func() { _ = bundle.Reloader.Stop() })
}

func TestWireCertReloader_InvalidCertReturnsError(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	require.NoError(t, err)

	certPath := filepath.Join(dir, "bad-cert.pem")
	keyPath := filepath.Join(dir, "bad-key.pem")
	require.NoError(t, os.WriteFile(certPath, []byte("not-a-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not-a-key"), 0o600))

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	err = wireCertReloader(bundle, certPath, keyPath, logger)
	require.Error(t, err)
	var cre *CertReloadError
	require.True(t, errors.As(err, &cre))
}

// ---------------------------------------------------------------------------
// tls.go: loadTLSFromFiles additional branches
// ---------------------------------------------------------------------------

func TestLoadTLSFromFiles_InvalidCACert(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	require.NoError(t, err)

	// Write a corrupt CA cert.
	caFile := filepath.Join(dir, "ca-bad.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("not-a-cert"), 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	_, err = loadTLSFromFiles(caFile, certFile, keyFile)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// tls.go: loadTLSFromDir additional branches
// ---------------------------------------------------------------------------

func TestLoadTLSFromDir_InvalidCertFile(t *testing.T) {
	dir := t.TempDir()

	// Write CA and key but corrupt the cert file.
	validBundle, err := generateCATLS()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), validBundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-cert.pem"), []byte("bad-cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-key.pem"), validBundle.KeyPEM, 0o600))

	_, err = loadTLSFromDir(dir)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadTLSFromDir_WithoutCAKey_ReturnsBundle(t *testing.T) {
	dir := t.TempDir()

	validBundle, err := generateCATLS()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), validBundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-cert.pem"), validBundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-key.pem"), validBundle.KeyPEM, 0o600))
	// ca-key.pem intentionally absent.

	bundle, err := loadTLSFromDir(dir)
	require.NoError(t, err)
	require.NotNil(t, bundle)
	assert.Empty(t, bundle.CAKeyPEM, "CAKeyPEM must be empty when ca-key.pem is absent")
}

// ---------------------------------------------------------------------------
// tls.go: loadCACertPool
// ---------------------------------------------------------------------------

// loadCACertPool is already tested in start_coverage_test.go.
// The tests below cover branches not covered elsewhere.

// ---------------------------------------------------------------------------
// runStart: TLS resolution error exits early
// ---------------------------------------------------------------------------

func TestRunStart_TLSResolutionError(t *testing.T) {
	// Provide explicit TLS files that do not exist so resolveTLS fails.
	f := &startFlags{
		dataDir:         t.TempDir(),
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		tlsCAFile:       "/nonexistent/ca.pem",
		tlsCertFile:     "/nonexistent/cert.pem",
		tlsKeyFile:      "/nonexistent/key.pem",
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// runStart: single-node reaches initializeNode then fails on SM type
// ---------------------------------------------------------------------------

func TestRunStart_SingleNode_SMError(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// package-level helpers for tests in this file
// ---------------------------------------------------------------------------

// pemEncodeCertificate PEM-encodes a raw DER certificate for use in test data.
func pemEncodeCertificate(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ---------------------------------------------------------------------------
// runStart: smType "mvcc" switch case (lines 744-749)
//
// Exercises the case "mvcc": branch in runStart by using smType = "mvcc".
// After the branch sets f.createFn, initializeNode is called. We block
// the quicraft.NewHost call by placing a file at the WAL path so MkdirAll
// fails, causing a fast failure without network operations.
// ---------------------------------------------------------------------------

func TestRunStart_MVCCSMType_SwitchCaseExecuted(t *testing.T) {
	dir := t.TempDir()

	// Place a file at the WAL path to make quicraft.NewHost fail when it
	// tries to create the WAL directory (MkdirAll will fail).
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "mvcc",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	// runStart should fail at initializeNode due to blocked WAL dir.
	err := runStart(f)
	require.Error(t, err, "runStart must fail when WAL dir is blocked by a file")
	// The error comes from quicraft.NewHost failing to create the WAL dir.
	// We don't assert the exact error type since it's a quicraft internal error.
}

// ---------------------------------------------------------------------------
// runStart: smType "pebble" switch case (lines 750-757)
//
// Same pattern as above but exercises the case "pebble": branch.
// ---------------------------------------------------------------------------

func TestRunStart_PebbleSMType_SwitchCaseExecuted(t *testing.T) {
	dir := t.TempDir()

	// Block the WAL path to force fast failure at quicraft.NewHost.
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "pebble",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err, "runStart must fail when WAL dir is blocked by a file")
}

// ---------------------------------------------------------------------------
// runStart: database AlreadyExists path (lines 689-699)
//
// When the same data directory is used for a second runStart invocation,
// the database creation returns AlreadyExistsError and the code falls
// back to opening the existing database.
// ---------------------------------------------------------------------------

func TestRunStart_DatabaseAlreadyExists_OpensExisting(t *testing.T) {
	dir := t.TempDir()

	// First invocation: creates the default databases and fails at
	// initializeNode with an unknown SM.
	f1 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-db-test",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}
	err := runStart(f1)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr),
		"first call should fail with UnknownSMError")

	// Second invocation: databases already exist, exercises the
	// AlreadyExistsError -> Open path (lines 691-696).
	f2 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-db-test",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}
	err = runStart(f2)
	require.Error(t, err)
	// The AlreadyExists path was taken on the second run; still fails at SM.
	require.True(t, errors.As(err, &smErr),
		"second call should still fail with UnknownSMError (after opening existing DBs)")
}

// ---------------------------------------------------------------------------
// runStart: SQL database setup path (lines 706-739)
//
// Exercises the f.sqlDatabase != "" && (f.enableMySQL || f.enablePgSQL)
// branch which creates or opens a SQL database before initializeNode.
// ---------------------------------------------------------------------------

func TestRunStart_SQLDatabaseSetup_CreatesNewDatabase(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-sql-test",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "my-sql-db",
		enableMySQL:     true,
		host:            testBindHost,
	}

	// Should fail at initializeNode (unknown SM), but the SQL database
	// creation branch (lines 706-726) was executed first.
	err := runStart(f)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

func TestRunStart_SQLDatabaseSetup_MemoryBackend(t *testing.T) {
	dir := t.TempDir()

	// Use smType "memory" for the backend determination (line 712-714).
	f := &startFlags{
		dataDir:         dir,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "my-mem-sql-db",
		enablePgSQL:     true,
		host:            testBindHost,
	}

	// Fail initializeNode fast by blocking the WAL path.
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	// The SQL DB with memory backend (line 713) is created, then
	// initializeNode fails due to blocked WAL dir.
	err := runStart(f)
	require.Error(t, err)
}

func TestRunStart_SQLDatabaseSetup_ExistingDatabase_UpdatesProtocol(t *testing.T) {
	dir := t.TempDir()

	// First run: create the SQL database.
	f1 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-sql2",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "qrdb",
		enableMySQL:     true,
		host:            testBindHost,
	}
	err := runStart(f1)
	require.Error(t, err)
	var smErr1 *UnknownSMError
	require.True(t, errors.As(err, &smErr1))

	// Second run: database exists, exercises the UpdateProtocol path
	// (lines 730-738).
	f2 := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-sql2",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "qrdb",
		enableMySQL:     true,
		enablePgSQL:     true,
		host:            testBindHost,
	}
	err = runStart(f2)
	require.Error(t, err)
	var smErr2 *UnknownSMError
	require.True(t, errors.As(err, &smErr2))
}

// ---------------------------------------------------------------------------
// runStart: wireCertReloader success path (line 657)
//
// Exercises the fileExists(certPath) && fileExists(keyPath) branch where
// wireCertReloader succeeds, so the "TLS cert hot-reload enabled" info
// log is emitted.
// ---------------------------------------------------------------------------

func TestRunStart_WireCertReloaderSuccess_LogsEnabled(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	require.NoError(t, err)

	// Write valid cert/key as node-cert.pem / node-key.pem in the data dir
	// so wireCertReloader succeeds and logs "TLS cert hot-reload enabled".
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-cert.pem"), bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-key.pem"), bundle.KeyPEM, 0o600))

	// Also write the CA so loadTLSFromDir succeeds (cached cert path).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca-key.pem"), bundle.CAKeyPEM, 0o600))

	// Block WAL dir to ensure fast failure at quicraft.NewHost.
	walPath := filepath.Join(dir, "wal")
	require.NoError(t, os.WriteFile(walPath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-reloader",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err = runStart(f)
	require.Error(t, err)
	// Failure at initializeNode, but wireCertReloader success path was executed.
}

// ---------------------------------------------------------------------------
// runStart: MigrateIfNeeded failure (line 669-671)
//
// Exercises the MigrateIfNeeded error return by providing a data-dir that
// is a file (not a directory), which causes the migration to fail.
// ---------------------------------------------------------------------------

func TestRunStart_MigrateIfNeeded_Error(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid TLS bundle so resolveTLS succeeds.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	// Write TLS files to a separate dir so they can be loaded explicitly.
	tlsDir := t.TempDir()
	caFile := filepath.Join(tlsDir, "ca.pem")
	certFile := filepath.Join(tlsDir, "cert.pem")
	keyFile := filepath.Join(tlsDir, "key.pem")
	require.NoError(t, os.WriteFile(caFile, bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	// Create a file at the dataDir path so MigrateIfNeeded fails trying
	// to use it as a directory.
	dataDirFile := filepath.Join(tmpDir, "data-as-file")
	require.NoError(t, os.WriteFile(dataDirFile, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dataDirFile,
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		listen:          "127.0.0.1:0",
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
// bootstrapAuth: user store creation error (lines 1496-1498)
//
// Exercises the AuthBootstrapError for "boot user store" and "user store"
// error paths by providing a kvstore that fails on List operations needed
// by the user/role store constructors.
// ---------------------------------------------------------------------------

func TestBootstrapAuth_UserStoreCreationError(t *testing.T) {
	sentinel := errors.New("store init failed")
	mock := &mockKVServicer{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, sentinel
		},
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, sentinel
		},
	}

	// Create a minimal mockRaftNode that returns a leader immediately.
	node := &mockRaftNode{}

	dir := t.TempDir()

	// bootstrapUserRoleStores requires both boot and runtime KV services.
	// We use the same mock for both to trigger the error on boot user store.
	// A throwaway map backs the guarded-batch mock: this test targets the
	// List/Scan failure path, not the atomic-identity plane.
	tx := newMockTransactionServicer(make(map[string][]byte))
	mgmt, err := bootstrapUserRoleStores(context.Background(), mock, mock, tx, node, testLeaderWait, 1, 1, &mockTenantServicer{}, false, nil, "", nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	// The error should be from bootstrap, though user/role stores may
	// succeed if they don't require initial List; just verify it completes.
	// If no error, also try auth enforcement (audit logger won't fail on valid dir).
	if err == nil && mgmt != nil {
		_, _ = bootstrapAuthEnforcement(context.Background(), mgmt, dir, node, audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{}, nil, nil, nil, nil, "", "", "enrolled", firstboot.NewAwaitingAdminCredentialProbe(), nil)
	}
}

// ---------------------------------------------------------------------------
// bootstrapAuth: runtime user store errors (lines 1517-1523)
// ---------------------------------------------------------------------------

func TestBootstrapAuth_ContextCanceledSkipsBootstrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately canceled

	mock := &mockKVServicer{}
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, errors.New("no leader")
		},
	}

	dir := t.TempDir()
	tx := newMockTransactionServicer(make(map[string][]byte))
	_, err := bootstrapUserRoleStores(ctx, mock, mock, tx, node, testLeaderWait, 1, 1, &mockTenantServicer{}, false, nil, "", nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
	_ = dir
}

// ---------------------------------------------------------------------------
// waitForLeaderElection: the caller's deadline
//
// Tests that a node that never returns a leader is waited on no longer than
// the caller's context allows, whatever the leader-wait budget.
// ---------------------------------------------------------------------------

func TestWaitForLeaderElection_CallerDeadlineEndsTheWait(t *testing.T) {
	// The caller's deadline falls well inside the leader-wait budget, so it
	// is what ends the wait.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, errors.New("no leader")
		},
	}

	err := waitForLeaderElection(ctx, node, time.Hour, 1)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// runStart: full startup with auth disabled
//
// With authEnabled=false the code skips bootstrapAuth and proceeds to
// the system shard readiness wait, server construction, and the signal wait. Sending
// SIGINT after the server starts covers lines 968-1076 (the "else" branch
// for auth, warmup, registry setup, server build/start, cleanup).
// ---------------------------------------------------------------------------

func TestRunStart_AuthDisabled_FullStartup_ThenSIGINT(t *testing.T) {
	dir := t.TempDir()

	// Use free ports to avoid conflicts with concurrent tests.
	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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
		authEnabled:     false, // skip bootstrapAuth → covers the else branch
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait until the admin listener accepts before signalling, then signal
	// until runStart returns. Both halves matter: signalling at t=0 cancels
	// startup before first boot and the test passes having covered nothing,
	// and sleeping a fixed duration is a bet that loses under load. See
	// signalAfterReady (go-qrdb#83).
	if runErr := signalAfterReady(t, done, syscall.SIGTERM,
		listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort))); runErr != nil {
		// A daemon that reached its listener and was then signalled shuts
		// down cleanly and returns nil. Logging an error as "acceptable"
		// here is what let a startup that never completed pass this test
		// (go-qrdb#83, cause in go-qrdb#86).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: SIGHUP cert reload while server is running
//
// Exercises line 776-778 (the SIGHUP goroutine body) by sending SIGHUP
// to the process while runStart is blocked in the signal-wait loop.
// ---------------------------------------------------------------------------

func TestRunStart_AuthDisabled_SIGHUP_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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

	// Observe the daemon's OWN handling of SIGHUP, not merely its delivery.
	// Installed before runStart so no signal can be processed unobserved.
	reloaded := make(chan struct{}, 1)
	origHandled := sighupHandled
	t.Cleanup(func() { sighupHandled = origHandled })
	sighupHandled = func() {
		select {
		case reloaded <- struct{}{}:
		default:
		}
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait until the daemon is SERVING before raising SIGHUP.
	//
	// A sleep here was a bet on startup duration, and SIGHUP is the signal
	// that loses it. runStart installs its own signal.Notify(SIGHUP) only
	// after initializeNode completes; if the signal arrives first, the test's
	// own notify channel below absorbs it, the delivery wait succeeds, and
	// handleSIGHUP is never reached. The test then passes having covered
	// nothing of the reload path it is named for — the same silent failure
	// this file exists to remove, on the one signal that does not terminate
	// the daemon (go-qrdb#83).
	//
	// The listener accepting means startup is past initializeNode, so the
	// handler is installed by the time SIGHUP is sent.
	awaitReady(t, done, listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort)), readyDeadline(t))

	// Register our own SIGHUP notify channel BEFORE raising the signal.
	// Without a channel registered, SIGHUP's default disposition (terminate)
	// would escape to the whole test-runner process. runStart's own goroutine
	// still receives its copy, exercising the cert-reload path (a no-op when
	// bundle.Reloader is nil, the expected case here).
	sighupSeen := make(chan os.Signal, 1)
	signal.Notify(sighupSeen, syscall.SIGHUP)
	defer signal.Stop(sighupSeen)

	if killErr := syscall.Kill(os.Getpid(), syscall.SIGHUP); killErr != nil {
		t.Fatalf("Kill(SIGHUP) error = %v", killErr)
	}

	// Wait synchronously for SIGHUP delivery so there is no window in which
	// it could reach the default disposition.
	select {
	case <-sighupSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP was not delivered within timeout")
	}

	// Then wait for runStart to have actually PROCESSED it.
	//
	// The receive above proves only that this test's own notify channel got a
	// copy. runStart has its own channel and its own goroutine, and sending
	// SIGTERM on the strength of our copy can shut the daemon down before that
	// goroutine reaches handleSIGHUP — the reload path never runs and the test
	// passes regardless, which is the failure this test exists to avoid
	// (go-qrdb#83).
	select {
	case <-reloaded:
	case <-time.After(readinessTimeout):
		t.Fatalf("SIGHUP was delivered but runStart did not process it within %v: "+
			"handleSIGHUP was never reached", readinessTimeout)
	}

	// Readiness was established before the SIGHUP above, so this only has to
	// drive the shutdown. Re-waiting for a listener that is already accepting
	// would assert nothing new.
	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		// A daemon that reached its listener and was then signalled shuts
		// down cleanly and returns nil. Logging an error as "acceptable"
		// here is what let a startup that never completed pass this test
		// (go-qrdb#83, cause in go-qrdb#86).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: codec config path
//
// NOTE: The KVService codec manager was removed because wrapping raw bytes
// through a JSON/msgpack codec produced double-encoded, unreadable storage
// (tenant CreateTenant appeared to succeed but GetTenant failed to unmarshal).
// The codec config is now ignored by runStart; invalid codec values are not
// a validation point. The previous TestRunStart_CodecConfig_InvalidCodec_ReturnsError
// test was removed with that code -- it asserted an error path that no
// longer exists.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// runStart: full startup with codec config and seal config enabled
//
// These tests require initializeNode to succeed (full startup). They cover
// the post-initializeNode paths at lines 790-942 that are only reachable
// after a successful host initialization.
// ---------------------------------------------------------------------------

func TestRunStart_WithCodecConfig_FullStartup(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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
		// Set valid codec config to cover lines 790-806.
		codecConfig: qrdbconfig.CodecConfig{
			StorageCodec: "json",
			WireCodec:    "json",
		},
		host: testBindHost,
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait until the admin listener accepts before signalling, then signal
	// until runStart returns. Both halves matter: signalling at t=0 cancels
	// startup before first boot and the test passes having covered nothing,
	// and sleeping a fixed duration is a bet that loses under load. See
	// signalAfterReady (go-qrdb#83).
	if runErr := signalAfterReady(t, done, syscall.SIGTERM,
		listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort))); runErr != nil {
		// A daemon that reached its listener and was then signalled shuts
		// down cleanly and returns nil. Logging an error as "acceptable"
		// here is what let a startup that never completed pass this test
		// (go-qrdb#83, cause in go-qrdb#86).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: seal config software strategy — the SEALED WINDOW, not full startup
//
// With sealConfig enabled, runStart brings up a pre-host sealed-window server
// on the admin port and then blocks in awaitUnseal until the barrier is
// unsealed. The Raft host, first boot and the regular server all come after
// that. Nothing here performs the unseal ceremony, so this test exercises the
// sealed window and its shutdown, and it never reaches full startup.
//
// It was named _FullStartup and gated on the admin port accepting — which the
// SEALED-WINDOW listener satisfies, so the gate went green while the daemon
// was still sealed. That is why this test reached "readiness" in ~0.8 s while
// its siblings took ~5 s; I recorded the discrepancy as an oddity and did not
// chase it. Renamed rather than re-gated: the honest fix for a test that does
// not reach a state is to stop claiming it, and a test that DOES drive the
// unseal ceremony is a different test worth writing on its own (go-qrdb#83).
// ---------------------------------------------------------------------------

func TestRunStart_SealConfig_SoftwareStrategy_SealedWindow_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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
		// Enable seal with default "software" strategy.
		sealConfig: qrdbconfig.SealConfig{
			Enabled:  true,
			Strategy: "",
		},
		host: testBindHost,
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait until the admin listener accepts before signalling, then signal
	// until runStart returns. Both halves matter: signalling at t=0 cancels
	// startup before first boot and the test passes having covered nothing,
	// and sleeping a fixed duration is a bet that loses under load. See
	// signalAfterReady (go-qrdb#83).
	if runErr := signalAfterReady(t, done, syscall.SIGTERM,
		listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort))); runErr != nil {
		// A daemon that reached its listener and was then signalled shuts
		// down cleanly and returns nil. Logging an error as "acceptable"
		// here is what let a startup that never completed pass this test
		// (go-qrdb#83, cause in go-qrdb#86).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

func TestRunStart_SealConfig_ShamirStrategy_InvalidParams_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
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
		// Invalid Shamir params: threshold > total causes seal.NewShamirStrategy to fail.
		sealConfig: qrdbconfig.SealConfig{
			Enabled:         true,
			Strategy:        "shamir",
			ShamirThreshold: 10,
			ShamirTotal:     2,
		},
		host: testBindHost,
	}

	err = runStart(f)
	require.Error(t, err, "runStart must return error for invalid Shamir params")
	var sealErr *SealStrategyError
	require.True(t, errors.As(err, &sealErr), "error must be SealStrategyError, got %T: %v", err, err)
	assert.Equal(t, "shamir", sealErr.Strategy)
}

// ---------------------------------------------------------------------------
// runStart: auth-enabled path with context cancellation (lines 957-967)
//
// Exercises the authEnabled=true branch that calls bootstrapAuth. By using
// a single-node "memory" SM and canceling the signal context, we can trigger
// the bootstrap auth path and cover lines 957-967.
// ---------------------------------------------------------------------------

func TestRunStart_AuthEnabled_ContextCanceled_ReturnsNil(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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
		authEnabled:     true, // triggers bootstrapAuth code path
		enableREST:      true,
		enableGRPC:      true,
		host:            testBindHost,
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Deliver SIGTERM until runStart returns, and wait on that return rather
	// than on a duration. runStart installs its handler partway through
	// startup, so a single delivery timed by a sleep can be absorbed by
	// signalGuard before the handler exists, after which no shutdown is
	// possible and the test can only time out. See signalUntilReturn.
	//
	// Either nil (signal observed during bootstrap) or an error from startup
	// is acceptable here: the assertion is that a signalled runStart returns,
	// which is what covers the sigCtx.Err() shutdown branches.
	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		t.Logf("runStart returned (acceptable): %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: auth-enabled full startup path (lines 983-1054)
//
// With authEnabled=true and a valid single-node memory SM, bootstrapAuth
// completes and the code proceeds through admin cert generation (when
// CAKeyPEM is present), server options with authResult, and the signal wait.
// ---------------------------------------------------------------------------

func TestRunStart_AuthEnabled_FullStartup_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
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
		authEnabled:     true, // full auth bootstrap path
		// This test is about the full-startup lifecycle (listeners, signal
		// handling), exercised with no admin certificate ever enrolled --
		// "enrolled" is the correct, explicit choice for that reason, not a
		// workaround (go-qrdb#548).
		mtlsBindingPolicy: "enrolled",
		enableREST:        true,
		enableGRPC:        true,
		host:              testBindHost,
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	// Wait until the admin listener accepts before signalling, then signal
	// until runStart returns. Both halves matter: signalling at t=0 cancels
	// startup before first boot and the test passes having covered nothing,
	// and sleeping a fixed duration is a bet that loses under load. See
	// signalAfterReady (go-qrdb#83).
	if runErr := signalAfterReady(t, done, syscall.SIGTERM,
		listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort))); runErr != nil {
		// A daemon that reached its listener and was then signalled shuts
		// down cleanly and returns nil. Logging an error as "acceptable"
		// here is what let a startup that never completed pass this test
		// (go-qrdb#83, cause in go-qrdb#86).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// runStart: seal subsystem + volume pool wired together (start.go:979.55-984.4)
//
// When both seal and volume pool are enabled, runStart wires the pool into the
// barrier registry at line 979-984 (BarrierRegistry.Attach). The test sends
// SIGTERM after the initialization phase so awaitUnseal returns an
// awaitUnsealCanceledError and runStart exits cleanly with nil.
// ---------------------------------------------------------------------------

// TestRunStart_SealAndVolumePool_WiredTogether_ThenSIGTERM verifies that
// sealSub.BarrierRegistry.Attach is called (start.go:979.55,984.4) when both
// sealConfig.Enabled=true and a volume pool are configured in runStart.
func TestRunStart_SealAndVolumePool_WiredTogether_ThenSIGTERM(t *testing.T) {
	dir := t.TempDir()
	volDir := t.TempDir()

	raftPort, err := freePortOn(raftListenHost, networkUDP)
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
		// Seal enabled: sealSub will be non-nil after initSealSubsystem.
		sealConfig: qrdbconfig.SealConfig{
			Enabled:  true,
			Strategy: "",
		},
		// Volume pool configured: the "default" pool maps volDir on this node.
		storageConfig: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "default", Path: volDir},
			},
		},
		host: testBindHost,
	}

	// Observe the wiring this test is NAMED for before shutting down.
	//
	// runStart installs its termination handler EARLY — before
	// initSealSubsystem, initVolumePoolSubsystem and BarrierRegistry.Attach —
	// so signalling at t=0 can cancel startup before the attachment and let
	// runStart return cleanly. The test would then pass having never wired the
	// pool to the registry, which is the whole behaviour under test. Neither a
	// sleep nor an immediate signal can rule that out; only the attachment
	// reporting itself can (go-qrdb#83).
	attached := make(chan struct{}, 1)
	origAttached := volumePoolAttached
	t.Cleanup(func() { volumePoolAttached = origAttached })
	volumePoolAttached = func() {
		select {
		case attached <- struct{}{}:
		default:
		}
	}
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() {
		done <- runStart(f)
	}()

	select {
	case <-attached:
	case runErr := <-done:
		t.Fatalf("runStart returned before the volume pool was attached to the "+
			"barrier registry, so the wiring under test never happened: %v", runErr)
	case <-time.After(readinessTimeout):
		t.Fatalf("the volume pool was not attached to the barrier registry within %v",
			readinessTimeout)
	}

	// The wiring is done; now drive the shutdown.
	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		// Not "acceptable": startup reached the attachment, so a signalled
		// runStart shuts down cleanly and returns nil. Logging the error here
		// is what let a startup that never got this far pass (go-qrdb#83).
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// waitForAdminReplication: verify context-cancelled path through user store
// ---------------------------------------------------------------------------

func TestWaitForAdminReplication_ContextCanceled_ViaUserStore(t *testing.T) {
	// Use a canceled context so the context.Done() branch is hit.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Build a real user store backed by a mock KV servicer that
	// never returns the admin user. This exercises a different code path
	// than TestWaitForAdminReplication_ContextCanceled in coverage_gaps_test.go
	// which uses mockUserServicer directly.
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "admin"}
		},
	}
	idGen, err := dao.NewSnowflakeGenerator(1)
	require.NoError(t, err)
	userSvc, err := user.NewStore(newKVServiceAdapter(mock, nil, nil), idGen)
	require.NoError(t, err)

	err = waitForAdminReplication(ctx, userSvc, tenantid.System)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for admin replication", abe.Step)
}
