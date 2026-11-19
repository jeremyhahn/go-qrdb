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

package raftharness

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// TestNewCluster_CommitsEveryHostIdentity pins what callers rely on: every
// host is a system-shard voter at its address, with the identity of its own
// certificate committed, and the membership is readable once NewCluster and
// WaitReadable return.
func TestNewCluster_CommitsEveryHostIdentity(t *testing.T) {
	c := NewCluster(t, 2)
	if len(c.Hosts) != 2 || len(c.Addrs) != 2 || len(c.Identities) != 2 || c.CreateFn() == nil {
		t.Fatalf("cluster = %d hosts, %d addrs, %d identities", len(c.Hosts), len(c.Addrs), len(c.Identities))
	}
	c.WaitReadable(t, shard.ShardKV, c.Hosts...)

	ctx, cancel := context.WithTimeout(context.Background(), Bound)
	defer cancel()
	m, err := c.Hosts[0].SyncGetShardMembership(ctx, shard.ShardKV)
	if err != nil {
		t.Fatalf("SyncGetShardMembership: %v", err)
	}
	if !maps.Equal(m.Identities, c.Identities) {
		t.Fatalf("committed identities %v, want %v", m.Identities, c.Identities)
	}
	for i, host := range c.Hosts {
		id := uint64(i + 1)
		if m.Nodes[id] != c.Addrs[i] || host.NodeIdentity() != c.Identities[id] {
			t.Fatalf("host %d: address %q identity %q, membership says %q %q",
				i, c.Addrs[i], host.NodeIdentity(), m.Nodes[id], c.Identities[id])
		}
	}
}

// WithHostOptions runs once per host with that host's replica ID and WAL
// directory, and the options it returns reach the host: a LogDB injected over
// a StallFS is the WAL the system shard's bootstrap fsyncs through.
func TestNewCluster_WithHostOptionsReachesEveryHost(t *testing.T) {
	var mu sync.Mutex
	walDirs := map[uint64]string{}
	fss := map[uint64]*StallFS{}
	c := NewCluster(t, 2, WithHostOptions(func(t testing.TB, replicaID uint64, walDir string) []quicraft.HostOption {
		fs := NewStallFS()
		db, err := waldb.Open(walDir, waldb.WithFS(fs))
		if err != nil {
			t.Fatalf("waldb.Open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mu.Lock()
		defer mu.Unlock()
		walDirs[replicaID], fss[replicaID] = walDir, fs
		return []quicraft.HostOption{quicraft.WithLogDB(db)}
	}))
	if len(c.Hosts) != 2 || len(walDirs) != 2 || walDirs[1] == "" || walDirs[1] == walDirs[2] {
		t.Fatalf("host options ran for %v, want replicas 1 and 2 with distinct WAL dirs", walDirs)
	}
	for id, fs := range fss {
		if fs.Syncs() == 0 {
			t.Fatalf("replica %d: no WAL fsync went through the injected LogDB", id)
		}
	}
}

func openStallFile(t *testing.T) (*StallFS, waldb.File) {
	t.Helper()
	fs := NewStallFS()
	f, err := fs.OpenFile(filepath.Join(t.TempDir(), "seg"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return fs, f
}

func TestStallFS_SyncParksUntilRelease(t *testing.T) {
	fs, f := openStallFile(t)
	s := fs.Arm(Bound)
	done := make(chan error, 1)
	go func() { done <- f.Sync() }()
	<-s.Entered()
	select {
	case <-done:
		t.Fatal("Sync returned while the stall was armed")
	default:
	}
	s.Release()
	s.Release()
	if err := <-done; err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fs.Syncs() != 1 {
		t.Fatalf("Syncs = %d, want 1", fs.Syncs())
	}
	if err := f.Sync(); err != nil || fs.Syncs() != 2 {
		t.Fatalf("Sync after release: err %v, Syncs %d", err, fs.Syncs())
	}
}

func TestStallFS_BoundReleasesTheStall(t *testing.T) {
	fs, f := openStallFile(t)
	fs.Arm(time.Millisecond)
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fs.Syncs() != 1 {
		t.Fatalf("Syncs = %d, want 1", fs.Syncs())
	}
}

func TestStallFS_OpenPassesThrough(t *testing.T) {
	fs := NewStallFS()
	if _, err := fs.Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("Open of a missing file succeeded")
	}
	if _, err := fs.OpenFile(filepath.Join(t.TempDir(), "no", "dir"), os.O_RDONLY, 0); err == nil {
		t.Fatal("OpenFile of a missing file succeeded")
	}
	_, f := openStallFile(t)
	if err := f.Sync(); err != nil {
		t.Fatalf("unarmed Sync: %v", err)
	}
	path := filepath.Join(t.TempDir(), "r")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := fs.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = r.Close()
}
