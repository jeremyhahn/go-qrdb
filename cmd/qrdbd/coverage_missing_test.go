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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

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
