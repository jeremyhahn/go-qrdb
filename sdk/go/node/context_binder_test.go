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
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// errNodeBinderRefused is the refusing binder's sentinel.
var errNodeBinderRefused = errors.New("node binder refused")

func TestWithContextBinder_SetsOption(t *testing.T) {
	o := &nodeOptions{}
	WithContextBinder(func(ctx context.Context) (context.Context, error) { return ctx, nil })(o)
	assert.NotNil(t, o.contextBinder)
}

func TestWithContextBinder_UnsetByDefault(t *testing.T) {
	assert.Nil(t, (&nodeOptions{}).contextBinder)
}

// TestWithContextBinder_BindsTheClient: the composer's binder runs on the
// node's Client and its refusal fails the call; the node's own services are
// not behind it.
func TestWithContextBinder_BindsTheClient(t *testing.T) {
	var runs atomic.Int32
	n := newMemoryNodeWithUser(t, WithContextBinder(func(ctx context.Context) (context.Context, error) {
		runs.Add(1)
		return ctx, errNodeBinderRefused
	}))
	err := n.Client().KV().Put(context.Background(), "k", []byte("v"))
	require.ErrorIs(t, err, errNodeBinderRefused)
	assert.Equal(t, int32(1), runs.Load())
	require.NoError(t, n.Services().KV.Put(context.Background(), "k", []byte("v")),
		"the node's own KV service must not run the composer's binder")
	assert.Equal(t, int32(1), runs.Load())
}

// TestWithContextBinder_NilFailsNode: a nil binder fails node construction
// with the embedded transport's typed error inside *ClientCreateError.
func TestWithContextBinder_NilFailsNode(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithContextBinder(nil))
	if n != nil {
		t.Cleanup(func() { _ = n.Close() })
	}
	var createErr *ClientCreateError
	require.ErrorAs(t, err, &createErr)
	var nilBinder *embedded.NilContextBinderError
	require.ErrorAs(t, err, &nilBinder)
}
