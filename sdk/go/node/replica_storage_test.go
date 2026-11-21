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
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// replicaOpens is a composer's replica storage factory: it opens memory
// storage for each replica and records the directory it was handed.
type replicaOpens struct {
	mu   sync.Mutex
	dirs []string
}

func (r *replicaOpens) open(_, _ uint64, dir string, _ filesys.FS) (storage.Storage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append(r.dirs, dir)
	return memstore.NewStorage(), nil
}

func (r *replicaOpens) opened() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dirs...)
}

// fsOpens is a composer's replica storage factory that does its I/O under the
// data-plane file system it is handed: it writes a sentinel through that FS
// into the replica directory and records the FS.
type fsOpens struct {
	mu  sync.Mutex
	fss []filesys.FS
}

const replicaSentinel = "REPLICA-SENTINEL-PLAINTEXT"

func (r *fsOpens) open(_, _ uint64, dir string, dataPlaneFS filesys.FS) (storage.Storage, error) {
	r.mu.Lock()
	r.fss = append(r.fss, dataPlaneFS)
	r.mu.Unlock()
	fs := dataPlaneFS
	if fs == nil {
		fs = filesys.Default
	}
	f, err := fs.Create(filepath.Join(dir, "sentinel"))
	if err != nil {
		return nil, err
	}
	if _, err := f.Write([]byte(replicaSentinel)); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return memstore.NewStorage(), nil
}

func (r *fsOpens) handed() []filesys.FS {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]filesys.FS(nil), r.fss...)
}

func TestBindReplicaStorage_HandsOverTheBarrierFS(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	fo := &fsOpens{}
	open := bindReplicaStorage(fo.open, sealed)
	require.NotNil(t, open)

	dir := t.TempDir()
	s, err := open(3, 1, dir)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Equal(t, []filesys.FS{sealed.FS()}, fo.handed())

	raw, err := os.ReadFile(filepath.Join(dir, "sentinel"))
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.False(t, bytes.Contains(raw, []byte(replicaSentinel)), "the replica's storage I/O must be encrypted by the barrier")
}

func TestBindReplicaStorage_BarrierOffHandsOverNil(t *testing.T) {
	for _, enc := range []*atrest.Encryptor{nil, atrest.New(nil)} {
		fo := &fsOpens{}
		dir := t.TempDir()
		_, err := bindReplicaStorage(fo.open, enc)(3, 1, dir)
		require.NoError(t, err)
		require.Equal(t, []filesys.FS{nil}, fo.handed())

		raw, err := os.ReadFile(filepath.Join(dir, "sentinel"))
		require.NoError(t, err)
		require.Equal(t, replicaSentinel, string(raw))
	}
	require.Nil(t, bindReplicaStorage(nil, atrest.New(sealtest.UnsealedBarrier(t))), "a nil factory keeps raft.sm_storage")
}

func TestBindReplicaStorage_PassesTheFactoryError(t *testing.T) {
	want := errors.New("replica storage unavailable")
	open := bindReplicaStorage(func(_, _ uint64, _ string, _ filesys.FS) (storage.Storage, error) {
		return nil, want
	}, nil)
	s, err := open(1, 1, t.TempDir())
	require.ErrorIs(t, err, want)
	require.Nil(t, s)
}

func TestWithReplicaStorageFactory_ReachesTheClusteredReplicas(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	cfg.Raft.SMType = "disk"
	ro := &replicaOpens{}
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites(), WithReplicaStorageFactory(ro.open))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	awaitWritable(ctx, t, n)

	dirs := ro.opened()
	require.NotEmpty(t, dirs, "the composer's factory must open the clustered replica storage")
	for _, d := range dirs {
		require.True(t, strings.HasPrefix(d, filepath.Join(cfg.Raft.DataDir, "sm", "shard-")), "replica directory %q", d)
	}
	require.NoError(t, n.Client().KV().Put(ctx, "replica/storage", []byte("v")))
	got, err := n.Client().KV().Get(ctx, "replica/storage")
	require.NoError(t, err)
	require.Equal(t, []byte("v"), got)
}

func TestClusteredCreateFunc_ReplicaStorage(t *testing.T) {
	ro := &replicaOpens{}
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.SMType = "fast"
	fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil, ro.open)
	require.NoError(t, err)
	machine := fn(7, 2)
	closer, ok := machine.(interface{ Close(context.Context) error })
	require.True(t, ok, "fast replica %T", machine)
	t.Cleanup(func() { _ = closer.Close(context.Background()) })
	require.Equal(t, []string{filepath.Join(cfg.Raft.DataDir, "sm", "shard-7-replica-2")}, ro.opened())
}

func TestClusteredCreateFunc_RefusesReplicaStorage(t *testing.T) {
	ro := &replicaOpens{}
	for smType, named := range map[string]string{"": "pebble", "pebble": "pebble", "mvcc": "mvcc", "memory": "memory", "highperf": "highperf"} {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Raft.DataDir = t.TempDir()
		cfg.Raft.SMType = smType
		fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil, ro.open)
		var na *statemachine.ReplicaStorageNotApplicableError
		require.True(t, errors.As(err, &na), "sm_type %q: err = %v, want ReplicaStorageNotApplicableError", smType, err)
		require.Equal(t, named, na.SMType)
		require.Nil(t, fn)
	}
	require.Empty(t, ro.opened(), "a refused factory must never be called")
}

func TestNewClusteredNode_MVCCRefusesReplicaStorage(t *testing.T) {
	cfg := newRaftIsolationConfig(t)
	cfg.Raft.SMType = "mvcc"
	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil, WithNoSyncWrites(), WithReplicaStorageFactory((&replicaOpens{}).open))
	if n != nil {
		t.Cleanup(func() { _ = n.Close() })
	}
	var na *statemachine.ReplicaStorageNotApplicableError
	require.ErrorAs(t, err, &na)
	require.Equal(t, "mvcc", na.SMType)
}
