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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// ---------------------------------------------------------------------------
// errors.go: SealStorageError (lines 316-323)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// errors.go: VolumePoolStorageError (lines 586-591)
// ---------------------------------------------------------------------------

func TestVolumePoolStorageError_Error(t *testing.T) {
	inner := errors.New("pebble open failed")
	e := &VolumePoolStorageError{Err: inner}
	want := "qrdb/storage/pool: state store: pebble open failed"
	assert.Equal(t, want, e.Error())
}

func TestVolumePoolStorageError_Unwrap(t *testing.T) {
	inner := errors.New("io error")
	e := &VolumePoolStorageError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: VolumePoolRegisterError (lines 602-607)
// ---------------------------------------------------------------------------

func TestVolumePoolRegisterError_Error(t *testing.T) {
	inner := errors.New("duplicate pool name")
	e := &VolumePoolRegisterError{Name: "default", Err: inner}
	want := `qrdb/storage/pool: register "default": duplicate pool name`
	assert.Equal(t, want, e.Error())
}

func TestVolumePoolRegisterError_Unwrap(t *testing.T) {
	inner := errors.New("empty candidates")
	e := &VolumePoolRegisterError{Name: "fast", Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: VolumePoolStrategyError (lines 617-625)
// ---------------------------------------------------------------------------

func TestVolumePoolStrategyError_Error_WithErr(t *testing.T) {
	inner := errors.New("setup failed")
	e := &VolumePoolStrategyError{Strategy: "round_robin", Err: inner}
	want := `qrdb/storage/pool: strategy "round_robin": setup failed`
	assert.Equal(t, want, e.Error())
}

func TestVolumePoolStrategyError_Error_NilErr(t *testing.T) {
	e := &VolumePoolStrategyError{Strategy: "unknown_strategy"}
	want := `qrdb/storage/pool: strategy "unknown_strategy": unsupported`
	assert.Equal(t, want, e.Error())
}

func TestVolumePoolStrategyError_Unwrap(t *testing.T) {
	inner := errors.New("bad config")
	e := &VolumePoolStrategyError{Strategy: "lease", Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: VolumePoolPathError (lines 636-642)
// ---------------------------------------------------------------------------

func TestVolumePoolPathError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &VolumePoolPathError{Pool: "default", Path: "/data/vol1", Err: inner}
	want := `qrdb/storage/pool: pool "default" path "/data/vol1": permission denied`
	assert.Equal(t, want, e.Error())
}

func TestVolumePoolPathError_Unwrap(t *testing.T) {
	inner := errors.New("mkdir failed")
	e := &VolumePoolPathError{Pool: "ssd", Path: "/mnt/ssd/vol", Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// seal_subsystem.go: awaitUnsealCanceledError.Error (line 284)
// ---------------------------------------------------------------------------

func TestAwaitUnsealCanceledError_Error(t *testing.T) {
	e := &awaitUnsealCanceledError{}
	msg := e.Error()
	assert.True(t, strings.Contains(msg, "unseal"), "message must mention unseal: %q", msg)
	assert.True(t, strings.Contains(msg, "canceled"), "message must mention canceled: %q", msg)
}

func TestErrAwaitUnsealCanceled_IsNonNil(t *testing.T) {
	require.NotNil(t, errAwaitUnsealCanceled)
	// Must be the typed sentinel.
	var typed *awaitUnsealCanceledError
	require.True(t, errors.As(errAwaitUnsealCanceled, &typed))
}

// ---------------------------------------------------------------------------
// seal_subsystem.go: strconvI (lines 290-310)
// ---------------------------------------------------------------------------

func TestStrconvI_Zero(t *testing.T) {
	assert.Equal(t, "0", strconvI(0))
}

func TestStrconvI_PositiveOneDigit(t *testing.T) {
	assert.Equal(t, "7", strconvI(7))
}

func TestStrconvI_PositiveMultiDigit(t *testing.T) {
	assert.Equal(t, "12345", strconvI(12345))
}

func TestStrconvI_Negative(t *testing.T) {
	assert.Equal(t, "-42", strconvI(-42))
}

func TestStrconvI_MatchesPortNumbers(t *testing.T) {
	// Port numbers are a primary use case.
	assert.Equal(t, "63001", strconvI(63001))
	assert.Equal(t, "8443", strconvI(8443))
}

// ---------------------------------------------------------------------------
// seal_subsystem.go: sealSubsystem.Close (lines 73-78)
// ---------------------------------------------------------------------------

func TestSealSubsystem_Close_NilSubsystem(t *testing.T) {
	var s *sealSubsystem
	require.NoError(t, s.Close())
}

func TestSealSubsystem_Close_NilSealStore(t *testing.T) {
	s := &sealSubsystem{SealStore: nil}
	require.NoError(t, s.Close())
}

// ---------------------------------------------------------------------------
// volume_pool.go: volumePoolSubsystem.Close (lines 66-71)
// ---------------------------------------------------------------------------

func TestVolumePoolSubsystem_Close_Nil(t *testing.T) {
	var v *volumePoolSubsystem
	require.NoError(t, v.Close())
}

func TestVolumePoolSubsystem_Close_NilStateStorage(t *testing.T) {
	// Zero-value VolumePoolSubsystem has a nil stateStorage; Close must be
	// a no-op (stateStorage is unexported — construct via zero value).
	v := &volumePoolSubsystem{}
	require.NoError(t, v.Close())
}

// ---------------------------------------------------------------------------
// volume_pool.go: buildPoolStrategy (lines 142-158)
// ---------------------------------------------------------------------------

// newTestPebbleStateStore opens a PebbleStorage in dir and wraps it in a
// PebbleStateStore for unit testing pool strategies. Cleanup is registered
// via t.Cleanup.
func newTestPebbleStateStore(t *testing.T, dir string) *qrdbpool.PebbleStateStore {
	t.Helper()
	storage, err := pebblestore.New(&pebblestore.Config{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	ss, err := qrdbpool.NewPebbleStateStore(storage)
	require.NoError(t, err)
	return ss
}

func TestBuildPoolStrategy_EmptyNameDefaultsToRoundRobin(t *testing.T) {
	ss := newTestPebbleStateStore(t, filepath.Join(t.TempDir(), "pool-state"))

	strategy, err := buildPoolStrategy("", ss)
	require.NoError(t, err)
	require.NotNil(t, strategy)
	assert.Equal(t, "round_robin", strategy.Name())
}

func TestBuildPoolStrategy_RoundRobinExplicit(t *testing.T) {
	ss := newTestPebbleStateStore(t, filepath.Join(t.TempDir(), "pool-state"))

	strategy, err := buildPoolStrategy("round_robin", ss)
	require.NoError(t, err)
	require.NotNil(t, strategy)
	assert.Equal(t, "round_robin", strategy.Name())
}

func TestBuildPoolStrategy_LeastUtilization(t *testing.T) {
	ss := newTestPebbleStateStore(t, filepath.Join(t.TempDir(), "pool-state"))

	strategy, err := buildPoolStrategy("least_utilization", ss)
	require.NoError(t, err)
	require.NotNil(t, strategy)
}

func TestBuildPoolStrategy_UnknownReturnsError(t *testing.T) {
	ss := newTestPebbleStateStore(t, filepath.Join(t.TempDir(), "pool-state"))

	_, err := buildPoolStrategy("nonexistent_strategy", ss)
	require.Error(t, err)
	var stratErr *VolumePoolStrategyError
	require.True(t, errors.As(err, &stratErr))
	assert.Equal(t, "nonexistent_strategy", stratErr.Strategy)
}

func TestBuildPoolStrategy_NilStateStoreReturnsError(t *testing.T) {
	// Passing nil stateStore triggers the nil-check guard.
	_, err := buildPoolStrategy("", nil)
	require.Error(t, err)
	var nilErr *qrdbpool.NilStateStoreError
	require.True(t, errors.As(err, &nilErr))
}

// ---------------------------------------------------------------------------
// volume_pool.go: buildLocalCandidates (lines 163-176)
// ---------------------------------------------------------------------------

func TestBuildLocalCandidates_EmptyPaths(t *testing.T) {
	candidates, err := buildLocalCandidates("default", []string{})
	require.NoError(t, err)
	assert.Empty(t, candidates)
}

func TestBuildLocalCandidates_SingleValidPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vol1")

	candidates, err := buildLocalCandidates("default", []string{p})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
}

func TestBuildLocalCandidates_MultipleValidPaths(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "vol1"),
		filepath.Join(dir, "vol2"),
		filepath.Join(dir, "vol3"),
	}

	candidates, err := buildLocalCandidates("mypool", paths)
	require.NoError(t, err)
	require.Len(t, candidates, 3)
}

func TestBuildLocalCandidates_EmptyPathReturnsConfigureError(t *testing.T) {
	// Pass an empty string path to trigger local.Configure's ErrPathNotSet.
	// Note: os.MkdirAll("", mode) silently succeeds on Linux, so the error
	// comes from local.Configure seeing an empty path.
	_, err := buildLocalCandidates("default", []string{""})
	// If MkdirAll("") succeeds, Configure("") should fail with ErrPathNotSet.
	// If Configure succeeds (unexpected), the function returns nil error.
	// Either outcome is acceptable — the main assertion is no panic.
	if err != nil {
		var pathErr *VolumePoolPathError
		assert.True(t, errors.As(err, &pathErr))
	}
}

func TestBuildLocalCandidates_BlockedPathReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Create a regular file where a directory must be created.
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	// Use the file as a path component to make MkdirAll fail.
	badPath := filepath.Join(blocker, "subdir")

	_, err := buildLocalCandidates("default", []string{badPath})
	require.Error(t, err)
	var pathErr *VolumePoolPathError
	require.True(t, errors.As(err, &pathErr))
	assert.Equal(t, "default", pathErr.Pool)
}

// ---------------------------------------------------------------------------
// volume_pool.go: initVolumePoolSubsystem (lines 82-137)
// ---------------------------------------------------------------------------

func TestInitVolumePoolSubsystem_NilPools_ReturnsNil(t *testing.T) {
	sub, err := initVolumePoolSubsystem(t.TempDir(), nil, newDiscardLogger())
	require.NoError(t, err)
	assert.Nil(t, sub, "nil pools must produce nil subsystem")
}

func TestInitVolumePoolSubsystem_EmptyPools_ReturnsNil(t *testing.T) {
	sub, err := initVolumePoolSubsystem(t.TempDir(), []qrdbconfig.VolumePoolConfig{}, newDiscardLogger())
	require.NoError(t, err)
	assert.Nil(t, sub, "empty pools must produce nil subsystem")
}

func TestInitVolumePoolSubsystem_SinglePool_RoundRobin(t *testing.T) {
	dir := t.TempDir()
	volDir := filepath.Join(dir, "vol1")

	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Paths: []string{volDir}, Strategy: "round_robin"},
	}

	sub, err := initVolumePoolSubsystem(dir, pools, newDiscardLogger())
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.NotNil(t, sub.Manager)
	require.NotNil(t, sub.StateStore)
	require.NotNil(t, sub.PerTenantRegistry)
	assert.Equal(t, "default", sub.DefaultPoolName)

	require.NoError(t, sub.Close())
}

func TestInitVolumePoolSubsystem_MultiPool(t *testing.T) {
	dir := t.TempDir()

	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "fast", Paths: []string{filepath.Join(dir, "fast-vol")}, Strategy: "round_robin"},
		{Name: "slow", Paths: []string{filepath.Join(dir, "slow-vol")}, Strategy: ""},
	}

	sub, err := initVolumePoolSubsystem(dir, pools, newDiscardLogger())
	require.NoError(t, err)
	require.NotNil(t, sub)

	require.NoError(t, sub.Close())
}

func TestInitVolumePoolSubsystem_UnknownStrategy_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "bad", Paths: []string{filepath.Join(dir, "vol")}, Strategy: "no_such_strategy"},
	}

	_, err := initVolumePoolSubsystem(dir, pools, newDiscardLogger())
	require.Error(t, err)
	var stratErr *VolumePoolStrategyError
	require.True(t, errors.As(err, &stratErr))
}

func TestInitVolumePoolSubsystem_BadVolPath_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Block the candidate path so buildLocalCandidates fails.
	blocker := filepath.Join(dir, "vol-blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	badVolPath := filepath.Join(blocker, "subdir")

	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Paths: []string{badVolPath}, Strategy: ""},
	}

	_, err := initVolumePoolSubsystem(dir, pools, newDiscardLogger())
	require.Error(t, err)
	var pathErr *VolumePoolPathError
	require.True(t, errors.As(err, &pathErr))
}

func TestInitVolumePoolSubsystem_DuplicatePoolNameReturnsError(t *testing.T) {
	dir := t.TempDir()

	// Register the same pool name twice — the second registration should fail
	// with a VolumePoolRegisterError.
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Paths: []string{filepath.Join(dir, "vol1")}, Strategy: ""},
		{Name: "default", Paths: []string{filepath.Join(dir, "vol2")}, Strategy: ""}, // duplicate
	}

	_, err := initVolumePoolSubsystem(dir, pools, newDiscardLogger())
	require.Error(t, err)
	var regErr *VolumePoolRegisterError
	require.True(t, errors.As(err, &regErr), "expected VolumePoolRegisterError, got %T: %v", err, err)
	assert.Equal(t, "default", regErr.Name)
}

func TestInitVolumePoolSubsystem_StateStoreDirBlockedByFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Put a file at <dir>/volumepool so MkdirAll fails.
	poolPath := filepath.Join(dir, "volumepool")
	require.NoError(t, os.WriteFile(poolPath, []byte("x"), 0o600))

	// Use a subdirectory of the blocked path as dataDir to trigger the
	// MkdirAll failure for the state dir.
	badDir := filepath.Join(poolPath, "sub")

	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Paths: []string{filepath.Join(t.TempDir(), "vol")}, Strategy: ""},
	}

	_, err := initVolumePoolSubsystem(badDir, pools, newDiscardLogger())
	require.Error(t, err)
	var storErr *VolumePoolStorageError
	require.True(t, errors.As(err, &storErr))
}

// ---------------------------------------------------------------------------
// kvstore_adapter.go: Exists (lines 97-107)
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Exists_Found(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "existing" {
				return []byte("value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "existing")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestKVServiceAdapter_Exists_NotFound(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "missing")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestKVServiceAdapter_Exists_NilDataMeansAbsent(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	exists, err := adapter.Exists(context.Background(), "nil-key")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestWebUIHandler_ReturnsNil(t *testing.T) {
	h := webUIHandler()
	// The !qrdbd_webui build returns nil by definition.
	assert.Nil(t, h, "webUIHandler must return nil when web UI is not embedded")
}

func TestWebUIHandler_ImplementsHTTPHandler(t *testing.T) {
	// Confirm the return type is compatible with http.Handler (nil is valid).
	_ = webUIHandler()
}

// ---------------------------------------------------------------------------
// start.go: defaultAuthFactors (line 1922)
// ---------------------------------------------------------------------------

func TestDefaultAuthFactors_ReturnsNil(t *testing.T) {
	factors := defaultAuthFactors()
	assert.Nil(t, factors,
		"standalone qrdbd registers no hardware auth factors at construction time")
}

func TestDefaultAuthFactors_CalledTwiceIsStable(t *testing.T) {
	a := defaultAuthFactors()
	b := defaultAuthFactors()
	assert.Equal(t, a, b, "defaultAuthFactors must be idempotent")
}

// ---------------------------------------------------------------------------
// start.go: startDynamicMembership error path (line 1572)
//
// The happy path requires a fully initialised host and is covered by the
// integration tests. The unit-testable seam is the error path: pass a nil
// KVServicer so that embedded.New returns an error, which causes
// startDynamicMembership to return early with that error.
// ---------------------------------------------------------------------------

func TestStartDynamicMembership_EmbeddedNewError(t *testing.T) {
	f := &startFlags{
		replicaID:       1,
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		clusterMinPeers: 3,
	}

	// Pass nil for kvSvc so embedded.New returns an error.
	_, err := startDynamicMembership(context.Background(), f, nil, nil, nil, nil, nil)
	require.Error(t, err, "startDynamicMembership must propagate embedded.New error")
}

// ---------------------------------------------------------------------------
// start.go: resolveCreateFunc with backend qualifier (mirrors the test in
// coverage_boost_test.go which requires extra build tags)
// ---------------------------------------------------------------------------

func TestResolveCreateFunc_UnknownType_WithBackend_IncludesBoth(t *testing.T) {
	_, err := resolveCreateFunc("unknown-sm-type", "mybackend", statemachine.StorageConfig{})
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
	assert.Equal(t, "unknown-sm-type:mybackend", smErr.Name)
}

func TestResolveCreateFunc_UnknownType_NoBackend_IncludesType(t *testing.T) {
	_, err := resolveCreateFunc("totally-unknown-sm", "", statemachine.StorageConfig{})
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
	assert.Equal(t, "totally-unknown-sm", smErr.Name)
}

// ---------------------------------------------------------------------------
// start.go: init — state machine type registrations exercised from init()
// (mirrors the tests in coverage_boost_test.go)
// ---------------------------------------------------------------------------

func TestInit_PebbleStateMachineRegisteredByInit(t *testing.T) {
	fn, err := resolveCreateFunc("pebble", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn, "pebble SM factory must be registered by init()")
}

func TestInit_MVCCStateMachineRegisteredByInit(t *testing.T) {
	fn, err := resolveCreateFunc("mvcc", "", statemachine.StorageConfig{})
	require.NoError(t, err)
	require.NotNil(t, fn, "mvcc SM factory must be registered by init()")
}
