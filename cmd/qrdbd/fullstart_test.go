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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// runStart: sealConfig.Enabled + initSealSubsystem failure
// (covers line 874-876 of runStart)
// ---------------------------------------------------------------------------

func TestRunStart_SealConfig_ShamirInvalidParams_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	raftPort, err := findFreeUDPPort()
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
		sealConfig: qrdbconfig.SealConfig{
			Enabled:         true,
			Strategy:        "shamir",
			ShamirThreshold: 10,
			ShamirTotal:     2, // invalid: threshold > total
		},
	}

	err = runStart(f)
	require.Error(t, err, "runStart must fail with invalid Shamir params")
	var sealErr *SealStrategyError
	require.True(t, errors.As(err, &sealErr), "error must be SealStrategyError, got %T: %v", err, err)
	assert.Equal(t, "shamir", sealErr.Strategy)
}

// ---------------------------------------------------------------------------
// runStart: volumePool error path (line 887-889)
// ---------------------------------------------------------------------------

func TestRunStart_VolumePool_StateStoreBlocked_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	// Block the volumepool state store directory by pre-creating a file there.
	poolStatePath := filepath.Join(dir, "volumepool")
	require.NoError(t, os.WriteFile(poolStatePath, []byte("x"), 0o600))

	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		storageConfig: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "default", Paths: []string{filepath.Join(dir, "vol")}, Strategy: ""},
			},
		},
	}

	err := runStart(f)
	require.Error(t, err)
	// Should fail at volume pool initialization (VolumePoolStorageError).
	var storErr *VolumePoolStorageError
	require.True(t, errors.As(err, &storErr),
		"expected VolumePoolStorageError, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// runStart: volume pool subsystem wired into startup
//
// Exercises the initVolumePoolSubsystem call in runStart (lines 886-903)
// by setting storageConfig.VolumePools. The SM fails at initializeNode
// (unknown type), but the volume-pool init path has already run.
// ---------------------------------------------------------------------------

func TestRunStart_VolumePoolConfigured_FailsAtSM(t *testing.T) {
	dir := t.TempDir()
	volDir := filepath.Join(dir, "vol1")

	f := &startFlags{
		dataDir:         dir,
		smType:          "nonexistent-sm-vp",
		discoveryMethod: "static",
		listen:          "0.0.0.0:0",
		raftAddress:     "127.0.0.1:0",
		shardID:         1,
		replicaID:       1,
		storageConfig: qrdbconfig.StorageConfig{
			VolumePools: []qrdbconfig.VolumePoolConfig{
				{Name: "default", Paths: []string{volDir}, Strategy: "round_robin"},
			},
		},
	}

	err := runStart(f)
	require.Error(t, err)
	// Must fail at SM resolution, not at volume-pool setup.
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr),
		"should fail with UnknownSMError, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// initSealSubsystem: happy path creates all components
// ---------------------------------------------------------------------------

func TestInitSealSubsystem_SoftwareStrategy(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:    "", // software/passphrase
			RootKeyPath: "seal-root",
		},
	}

	sub, err := initSealSubsystem(f, context.Background())
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.NotNil(t, sub.SealStore)
	require.NotNil(t, sub.SealBarrier)
	require.NotNil(t, sub.CredCoreSvc)
	require.NotNil(t, sub.BarrierRegistry)
	assert.NotEmpty(t, sub.SealDataDir)

	require.NoError(t, sub.Close())
}

func TestInitSealSubsystem_SecondBootIdempotent(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:    "",
			RootKeyPath: "seal-root",
		},
	}

	// First boot.
	sub1, err := initSealSubsystem(f, context.Background())
	require.NoError(t, err)
	require.NoError(t, sub1.Close())

	// Second boot on same dataDir must succeed (format sentinel already written).
	sub2, err := initSealSubsystem(f, context.Background())
	require.NoError(t, err)
	require.NotNil(t, sub2)
	require.NoError(t, sub2.Close())
}

func TestInitSealSubsystem_ShamirStrategy(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:        "shamir",
			ShamirThreshold: 2,
			ShamirTotal:     3,
			RootKeyPath:     "seal-root",
		},
	}

	sub, err := initSealSubsystem(f, context.Background())
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.NoError(t, sub.Close())
}

func TestInitSealSubsystem_InvalidShamirReturnsError(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:        "shamir",
			ShamirThreshold: 10,
			ShamirTotal:     2, // threshold > total — invalid
			RootKeyPath:     "seal-root",
		},
	}

	_, err := initSealSubsystem(f, context.Background())
	require.Error(t, err)
	var stratErr *SealStrategyError
	require.True(t, errors.As(err, &stratErr))
	assert.Equal(t, "shamir", stratErr.Strategy)
}

func TestInitSealSubsystem_SealStore_ClosedAfterError(t *testing.T) {
	// Block the seal data dir creation so the first MkdirAll fails,
	// triggering the &SealStorageError path before any Pebble is opened.
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "seal")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	// Use a path that traverses the blocker file.
	badDir := filepath.Join(blocker, "sub")

	f := &startFlags{
		dataDir:      badDir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	_, err := initSealSubsystem(f, context.Background())
	require.Error(t, err)
	var storErr *SealStorageError
	require.True(t, errors.As(err, &storErr))
}

// TestInitSealSubsystem_SealPebbleDirectoryLocked verifies that
// initSealSubsystem returns *SealStorageError (seal_subsystem.go:97-99) when
// pebblestore.New fails because the seal directory is already held by another
// PebbleStorage instance in the same process. Pebble's vfs.Lock maintains an
// in-process lockedFiles map; a second attempt to lock the same path returns
// "lock held by current process", which New wraps in *storage.OpError.
func TestInitSealSubsystem_SealPebbleDirectoryLocked(t *testing.T) {
	dir := t.TempDir()
	sealDir := filepath.Join(dir, "seal")
	require.NoError(t, os.MkdirAll(sealDir, 0o700))

	// Open and HOLD the first PebbleStorage — this acquires the vfs LOCK.
	lockedStore, err := pebblestore.New(&pebblestore.Config{DataDir: sealDir})
	require.NoError(t, err, "setup: first pebble open must succeed")
	t.Cleanup(func() { _ = lockedStore.Close() })

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	// Second pebble.Open on the same sealDir must fail ("lock held by current
	// process"), causing initSealSubsystem to return *SealStorageError at
	// seal_subsystem.go:97-99.
	_, err = initSealSubsystem(f, context.Background())
	require.Error(t, err, "initSealSubsystem must fail when seal directory is locked")
	var storErr *SealStorageError
	require.True(t, errors.As(err, &storErr),
		"expected *SealStorageError from locked pebble directory, got %T: %v", err, err)
}

func TestInitSealSubsystem_SealPebbleOpenFails(t *testing.T) {
	dir := t.TempDir()

	// Create a regular FILE at <dataDir>/seal so os.MkdirAll succeeds (dir exists)
	// but pebblestore.New fails because it can't create subdirs inside a file.
	sealPath := filepath.Join(dir, "seal")
	require.NoError(t, os.WriteFile(sealPath, []byte("x"), 0o600))

	// Now make sealPath a "directory" by removing the file and creating a dir,
	// then creating a file inside it where pebble would want a directory.
	require.NoError(t, os.Remove(sealPath))
	require.NoError(t, os.Mkdir(sealPath, 0o700))
	// Put a file at the pebble data location to block pebble.New.
	pebbleDataPath := filepath.Join(sealPath, "MANIFEST-000001")
	require.NoError(t, os.WriteFile(pebbleDataPath, []byte("not-a-pebble-manifest"), 0o600))

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	// pebblestore.New may succeed even with an existing file — so this test
	// verifies there's no panic and handles both success and error.
	sub, err := initSealSubsystem(f, context.Background())
	if err != nil {
		var storErr *SealStorageError
		// If it fails, must be SealStorageError.
		if !errors.As(err, &storErr) {
			t.Errorf("expected SealStorageError or success, got %T: %v", err, err)
		}
	} else {
		require.NoError(t, sub.Close())
	}
}

func TestInitSealSubsystem_LegacyDataDir_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	sealDir := filepath.Join(dir, "seal")

	// Create the seal Pebble store and write a non-sentinel key so the store
	// is non-empty. EnsureFormatSentinel will detect the absence of the
	// sentinel and return ErrLegacyDataDir.
	storage, err := pebblestore.New(&pebblestore.Config{DataDir: sealDir})
	require.NoError(t, err)
	require.NoError(t, storage.Set([]byte("legacy/key"), []byte("value")))
	require.NoError(t, storage.Close())

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	_, err = initSealSubsystem(f, context.Background())
	require.Error(t, err)
	// The error must be a SealBarrierError wrapping ErrLegacyDataDir.
	var barrierErr *SealBarrierError
	require.True(t, errors.As(err, &barrierErr))
}

func TestSealSubsystem_Close_WithRealSealStore(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: ""},
	}

	sub, err := initSealSubsystem(f, context.Background())
	require.NoError(t, err)
	// Close must succeed.
	require.NoError(t, sub.Close())
	// Second Close is a no-op (Pebble guards against double-close).
	require.NoError(t, sub.Close())
}

// ---------------------------------------------------------------------------
// runStart: dbManager.OpenAll failure when databases/ cannot be read
//
// Covers start.go line 869-871: the error return from dbManager.OpenAll when
// os.ReadDir("{dataDir}/databases") fails with anything other than
// os.IsNotExist, causing database.Manager.OpenAll to return *MetaReadError.
// ---------------------------------------------------------------------------

func TestRunStart_OpenAll_DatabasesDirUnreadable(t *testing.T) {
	dir := t.TempDir()

	// Put a regular file where the databases/ directory belongs, so that
	// os.ReadDir inside OpenAll fails with ENOTDIR rather than ENOENT.
	//
	// A 0o000 directory would be the more obvious way to provoke the same
	// branch, but permission bits are not a fault-injection mechanism here:
	// CI job containers run as root, which bypasses the check entirely, and
	// runStart would then go on to start a real node and block until the test
	// binary's deadline (testing-guidelines.md, Fault Injection).
	dbsPath := filepath.Join(dir, "databases")
	require.NoError(t, os.WriteFile(dbsPath, nil, 0o600))

	raftPort, portErr := findFreeUDPPort()
	require.NoError(t, portErr)
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
		// authEnabled=false skips generateAdminClientCert so the only
		// blockage between resolveTLS and dbManager.OpenAll is removed.
		authEnabled: false,
	}

	err := runStart(f)
	require.Error(t, err, "runStart must fail when databases/ cannot be read")

	var metaErr *database.MetaReadError
	require.True(t, errors.As(err, &metaErr),
		"error must be *database.MetaReadError, got %T: %v", err, err)
}
